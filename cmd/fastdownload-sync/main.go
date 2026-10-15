package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"fastdownload-sync/internal/config"
	"github.com/fsnotify/fsnotify"
	_ "modernc.org/sqlite"
)

const version = "0.1.0"

type fileItem struct {
	Source     string      `json:"source"`
	Rel        string      `json:"relative_path"`
	Path       string      `json:"-"`
	Size       int64       `json:"size"`
	Mode       os.FileMode `json:"-"`
	Hash       string      `json:"sha256"`
	CachedHash string      `json:"-"`
	ModTimeNS  int64       `json:"-"`
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	configPath := flag.String("config", "/etc/fastdownload-sync/config.yaml", "path to YAML config")
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	if args[0] == "version" {
		fmt.Printf("fastdownload-sync %s\n", version)
		fmt.Printf("Author: phenom | Url: https://t.me/phenom1337\n")
		return
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}
	switch args[0] {
	case "validate":
		fmt.Printf("Config OK: %d source(s), destination=%s, state=%s\n", len(cfg.Sources), cfg.Destination, cfg.StateFile)
	case "scan":
		db, err := openState(cfg.StateFile)
		if err != nil {
			log.Fatal(err)
		}
		defer db.Close()
		items, err := scan(cfg, db)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Found %d eligible file(s)\n", len(items))
		for _, f := range items {
			fmt.Printf("%-18s %8d  %s  %s\n", f.Source, f.Size, f.Hash[:12], f.Rel)
		}
	case "sync":
		db, err := openState(cfg.StateFile)
		if err != nil {
			log.Fatal(err)
		}
		defer db.Close()
		items, err := scan(cfg, db)
		if err != nil {
			log.Fatal(err)
		}
		ctx := context.Background()
		if err := syncFiles(ctx, db, cfg.Destination, items); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Sync complete: processed %d file(s); automatic deletion is disabled.\n", len(items))
	case "watch":
		if err := watch(cfg); err != nil {
			log.Fatal(err)
		}
	case "status":
		db, err := openState(cfg.StateFile)
		if err != nil {
			log.Fatal(err)
		}
		defer db.Close()
		var files int
		var last sql.NullString
		if err := db.QueryRow("SELECT COUNT(*) FROM synced_files").Scan(&files); err != nil {
			log.Fatal(err)
		}
		_ = db.QueryRow("SELECT MAX(completed_at) FROM sync_runs").Scan(&last)
		fmt.Printf("State database: %s\nDestination: %s\nTracked files: %d\nLast successful run: %s\n", cfg.StateFile, cfg.Destination, files, nullText(last))
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Println("Fast Download Sync — configured file scanner and synchronizer")
	fmt.Println("Usage: fastdownload-sync [-config path] <validate|scan|sync|watch|status|version>")
}

func scan(cfg config.Config, db *sql.DB) ([]fileItem, error) {
	cache := make(map[string]cachedFile)
	if db != nil {
		rows, err := db.Query("SELECT source_name,relative_path,sha256,size,mtime_ns FROM synced_files")
		if err != nil {
			return nil, fmt.Errorf("load scan cache: %w", err)
		}
		for rows.Next() {
			var source, rel, hash string
			var size, mtime int64
			if err := rows.Scan(&source, &rel, &hash, &size, &mtime); err != nil {
				rows.Close()
				return nil, err
			}
			cache[source+"\x00"+rel] = cachedFile{Hash: hash, Size: size, ModTimeNS: mtime}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	var out []fileItem
	for _, src := range cfg.Sources {
		rootInfo, err := os.Lstat(src.Path)
		if err != nil {
			return nil, fmt.Errorf("source %q (%s): %w", src.Name, src.Path, err)
		}
		if rootInfo.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("source %q is a symlink; configure its real directory path", src.Name)
		}
		if !rootInfo.IsDir() {
			return nil, fmt.Errorf("source %q is not a directory", src.Name)
		}
		err = filepath.WalkDir(src.Path, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path == src.Path {
				return nil
			}
			rel, err := filepath.Rel(src.Path, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if excluded(rel, src.Exclude) {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || excluded(rel, src.Exclude) {
				return nil
			}
			if !allowedExtension(path, src.Extensions) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			mtime := info.ModTime().UnixNano()
			entry, found := cache[src.Name+"\x00"+rel]
			h := ""
			if found && entry.Size == info.Size() && entry.ModTimeNS == mtime {
				h = entry.Hash
			} else {
				h, err = hashFile(path)
				if err != nil {
					return fmt.Errorf("hash %s: %w", path, err)
				}
			}
			cachedHash := ""
			if found {
				cachedHash = entry.Hash
			}
			out = append(out, fileItem{Source: src.Name, Rel: rel, Path: path, Size: info.Size(), Mode: info.Mode(), Hash: h, CachedHash: cachedHash, ModTimeNS: mtime})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("scan source %q: %w", src.Name, err)
		}
	}
	return out, nil
}

func allowedExtension(path string, extensions []string) bool {
	if len(extensions) == 0 {
		return true
	}
	ext := strings.ToLower(filepath.Ext(path))
	for _, allowed := range extensions {
		if ext == strings.ToLower(allowed) {
			return true
		}
	}
	return false
}

func excluded(rel string, patterns []string) bool {
	for _, pattern := range patterns {
		pattern = filepath.ToSlash(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if ok, _ := filepath.Match(pattern, rel); ok {
			return true
		}
		if ok, _ := filepath.Match(pattern, filepath.Base(rel)); ok {
			return true
		}
		if strings.HasPrefix(pattern, "**/") {
			if ok, _ := filepath.Match(strings.TrimPrefix(pattern, "**/"), filepath.Base(rel)); ok {
				return true
			}
		}
	}
	return false
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func openState(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"CREATE TABLE IF NOT EXISTS synced_files (source_name TEXT NOT NULL, relative_path TEXT NOT NULL, sha256 TEXT NOT NULL, size INTEGER NOT NULL, mtime_ns INTEGER NOT NULL DEFAULT 0, synced_at TEXT NOT NULL, PRIMARY KEY(source_name, relative_path))",
		"CREATE TABLE IF NOT EXISTS sync_runs (id INTEGER PRIMARY KEY AUTOINCREMENT, started_at TEXT NOT NULL, completed_at TEXT NOT NULL, files_seen INTEGER NOT NULL, files_copied INTEGER NOT NULL, errors INTEGER NOT NULL DEFAULT 0)",
	} {
		if _, err := db.Exec(q); err != nil {
			db.Close()
			return nil, fmt.Errorf("initialize state database: %w", err)
		}
	}
	columns, err := db.Query("PRAGMA table_info(synced_files)")
	if err != nil {
		db.Close()
		return nil, err
	}
	hasMtime := false
	for columns.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var def any
		if err := columns.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			columns.Close()
			db.Close()
			return nil, err
		}
		if name == "mtime_ns" {
			hasMtime = true
		}
	}
	if err := columns.Err(); err != nil {
		columns.Close()
		db.Close()
		return nil, err
	}
	columns.Close()
	if !hasMtime {
		if _, err := db.Exec("ALTER TABLE synced_files ADD COLUMN mtime_ns INTEGER NOT NULL DEFAULT 0"); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

type cachedFile struct {
	Hash      string
	Size      int64
	ModTimeNS int64
}

func syncFiles(ctx context.Context, db *sql.DB, destination string, items []fileItem) error {
	if err := ensureSafeDir(destination, destination); err != nil {
		return err
	}
	started := time.Now().UTC()
	copied := 0
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		target := filepath.Join(destination, item.Source, filepath.FromSlash(item.Rel))
		rel, err := filepath.Rel(destination, target)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("refusing destination path escape for %s", item.Rel)
		}
		if err := ensureSafeDir(destination, filepath.Dir(target)); err != nil {
			return fmt.Errorf("create destination directory: %w", err)
		}
		if info, statErr := os.Lstat(target); statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return fmt.Errorf("refusing to replace non-regular destination file: %s", target)
			}
		} else if !os.IsNotExist(statErr) {
			return fmt.Errorf("inspect destination %s: %w", target, statErr)
		}
		if item.CachedHash != "" && item.CachedHash == item.Hash {
			if info, statErr := os.Stat(target); statErr == nil && info.Mode().IsRegular() && info.Size() == item.Size {
				// The source hash is unchanged and this destination file was already
				// published by us. Avoid an unnecessary SQLite write on every scan.
				continue
			}
		}
		oldHash, _ := hashFile(target)
		if oldHash == item.Hash {
			if err := saveFileState(db, item); err != nil {
				return err
			}
			continue
		}
		tmp, err := os.CreateTemp(filepath.Dir(target), ".fdsync-*.tmp")
		if err != nil {
			return fmt.Errorf("create temporary file: %w", err)
		}
		tmpName := tmp.Name()
		ok := false
		func() {
			defer func() {
				tmp.Close()
				if !ok {
					os.Remove(tmpName)
				}
			}()
			src, e := os.Open(item.Path)
			if e != nil {
				err = e
				return
			}
			defer src.Close()
			h := sha256.New()
			if _, e = io.Copy(io.MultiWriter(tmp, h), src); e != nil {
				err = e
				return
			}
			if hex.EncodeToString(h.Sum(nil)) != item.Hash {
				err = fmt.Errorf("source changed during copy: %s", item.Path)
				return
			}
			if e = tmp.Chmod(0644); e != nil {
				err = e
				return
			}
			if e = tmp.Sync(); e != nil {
				err = e
				return
			}
			if e = tmp.Close(); e != nil {
				err = e
				return
			}
			if e = os.Rename(tmpName, target); e != nil {
				err = e
				return
			}
			ok = true
		}()
		if err != nil {
			return fmt.Errorf("sync %s: %w", item.Path, err)
		}
		if err := saveFileState(db, item); err != nil {
			return err
		}
		copied++
	}
	_, err := db.ExecContext(ctx, "INSERT INTO sync_runs(started_at,completed_at,files_seen,files_copied,errors) VALUES(?,?,?,?,0)", started.Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339), len(items), copied)
	if err != nil {
		return fmt.Errorf("record sync run: %w", err)
	}
	return nil
}

func saveFileState(db *sql.DB, f fileItem) error {
	_, err := db.Exec("INSERT INTO synced_files(source_name,relative_path,sha256,size,mtime_ns,synced_at) VALUES(?,?,?,?,?,?) ON CONFLICT(source_name,relative_path) DO UPDATE SET sha256=excluded.sha256,size=excluded.size,mtime_ns=excluded.mtime_ns,synced_at=excluded.synced_at", f.Source, f.Rel, f.Hash, f.Size, f.ModTimeNS, time.Now().UTC().Format(time.RFC3339))
	return err
}

func watch(cfg config.Config) error {
	interval, err := time.ParseDuration(cfg.ScanInterval)
	if err != nil || interval < time.Second {
		return fmt.Errorf("scan_interval must be a duration of at least 1s (got %q)", cfg.ScanInterval)
	}
	db, err := openState(cfg.StateFile)
	if err != nil {
		return err
	}
	defer db.Close()
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create filesystem watcher: %w", err)
	}
	defer watcher.Close()
	for _, src := range cfg.Sources {
		if err := addWatchTree(watcher, src.Path); err != nil {
			return fmt.Errorf("watch source %q: %w", src.Name, err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	run := func() {
		started := time.Now()
		items, scanErr := scan(cfg, db)
		if scanErr != nil {
			log.Printf("scan failed: %v", scanErr)
			return
		}
		if syncErr := syncFiles(ctx, db, cfg.Destination, items); syncErr != nil {
			log.Printf("sync failed: %v", syncErr)
			return
		}
		log.Printf("sync complete: files=%d duration=%s", len(items), time.Since(started).Round(time.Millisecond))
	}
	log.Printf("watch started: interval=%s sources=%d destination=%s", interval, len(cfg.Sources), cfg.Destination)
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	debounceTimer := time.NewTimer(time.Hour)
	if !debounceTimer.Stop() {
		<-debounceTimer.C
	}
	defer debounceTimer.Stop()
	var debounce <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			log.Printf("watch stopped")
			return nil
		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if event.Op&fsnotify.Create != 0 {
				if info, statErr := os.Lstat(event.Name); statErr == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
					if err := addWatchTree(watcher, event.Name); err != nil {
						log.Printf("watch new directory %s: %v", event.Name, err)
					}
				}
			}
			if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			if !debounceTimer.Stop() {
				select {
				case <-debounceTimer.C:
				default:
				}
			}
			debounceTimer.Reset(500 * time.Millisecond)
			debounce = debounceTimer.C
		case watchErr, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			log.Printf("filesystem watcher warning: %v", watchErr)
		case <-ticker.C:
			run()
		case <-debounce:
			debounce = nil
			run()
		}
	}
}

func addWatchTree(watcher *fsnotify.Watcher, root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("source root must be a real directory")
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root {
				info, err := os.Lstat(path)
				if err != nil {
					return err
				}
				if info.Mode()&os.ModeSymlink != 0 {
					return filepath.SkipDir
				}
			}
			if err := watcher.Add(path); err != nil {
				return err
			}
		}
		return nil
	})
}

func nullText(v sql.NullString) string {
	if !v.Valid || v.String == "" {
		return "never"
	}
	return v.String
}

// ensureSafeDir creates directories beneath root while rejecting symlinks in every
// path component. This prevents a pre-existing symlink from redirecting published files.
func ensureSafeDir(root, target string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("directory %s is outside destination root %s", target, root)
	}
	current := string(os.PathSeparator)
	for _, part := range strings.Split(strings.TrimPrefix(target, string(os.PathSeparator)), string(os.PathSeparator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if os.IsNotExist(statErr) {
			if err := os.Mkdir(current, 0755); err != nil && !os.IsExist(err) {
				return err
			}
			info, statErr = os.Lstat(current)
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink directory component: %s", current)
		}
		if !info.IsDir() {
			return fmt.Errorf("path component is not a directory: %s", current)
		}
	}
	return nil
}
