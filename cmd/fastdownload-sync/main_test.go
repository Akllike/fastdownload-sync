package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"fastdownload-sync/internal/config"
)

func TestSyncCopiesChangesAndDoesNotDeleteRemovedAssets(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	dest := filepath.Join(root, "dest")
	state := filepath.Join(root, "state.sqlite")
	if err := os.MkdirAll(filepath.Join(source, "maps"), 0755); err != nil {
		t.Fatal(err)
	}
	srcFile := filepath.Join(source, "maps", "custom.bsp")
	if err := os.WriteFile(srcFile, []byte("first version"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		Sources:     []config.Source{{Name: "css-test", Path: source, Extensions: []string{".bsp"}}},
		Destination: dest, StateFile: state,
	}
	db, err := openState(state)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	items, err := scan(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 asset, got %d", len(items))
	}
	if err := syncFiles(context.Background(), db, dest, items); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dest, "css-test", "maps", "custom.bsp")
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "first version" {
		t.Fatalf("initial copy mismatch: %q, %v", got, err)
	}

	items, err = scan(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].CachedHash == "" || items[0].CachedHash != items[0].Hash {
		t.Fatalf("expected cached source hash on second scan: %#v", items)
	}
	if err := syncFiles(context.Background(), db, dest, items); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(srcFile, []byte("second version is longer"), 0644); err != nil {
		t.Fatal(err)
	}
	items, err = scan(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	if err := syncFiles(context.Background(), db, dest, items); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(target)
	if err != nil || string(got) != "second version is longer" {
		t.Fatalf("updated copy mismatch: %q, %v", got, err)
	}

	if err := os.Remove(srcFile); err != nil {
		t.Fatal(err)
	}
	items, err = scan(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	if err := syncFiles(context.Background(), db, dest, items); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("source removal should not delete published asset: %v", err)
	}
}
