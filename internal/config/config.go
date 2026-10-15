package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Sources      []Source `yaml:"sources"`
	Destination  string   `yaml:"destination"`
	StateFile    string   `yaml:"state_file"`
	ScanInterval string   `yaml:"scan_interval"`
}

type Source struct {
	Name       string   `yaml:"name"`
	Path       string   `yaml:"path"`
	Extensions []string `yaml:"extensions"`
	Exclude    []string `yaml:"exclude"`
}

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func Load(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("parse config: %w", err)
	}
	if len(c.Sources) == 0 {
		return c, errors.New("config must define at least one source")
	}
	if strings.TrimSpace(c.Destination) == "" {
		return c, errors.New("destination is required")
	}
	if c.StateFile == "" {
		c.StateFile = "/var/lib/fastdownload-sync/state.sqlite"
	}
	if c.ScanInterval == "" {
		c.ScanInterval = "5m"
	}
	interval, err := time.ParseDuration(c.ScanInterval)
	if err != nil || interval < time.Second {
		return c, fmt.Errorf("scan_interval must be a duration of at least 1s (got %q)", c.ScanInterval)
	}
	p, err := filepath.Abs(c.Destination)
	if err != nil {
		return c, fmt.Errorf("destination: %w", err)
	}
	c.Destination = filepath.Clean(p)
	seen := make(map[string]bool)
	for i := range c.Sources {
		s := &c.Sources[i]
		if strings.TrimSpace(s.Name) == "" {
			s.Name = fmt.Sprintf("source-%d", i+1)
		}
		if !safeName.MatchString(s.Name) {
			return c, fmt.Errorf("source name %q is invalid; use letters, digits, dot, underscore or hyphen", s.Name)
		}
		if seen[s.Name] {
			return c, fmt.Errorf("duplicate source name %q", s.Name)
		}
		seen[s.Name] = true
		if strings.TrimSpace(s.Path) == "" {
			return c, fmt.Errorf("sources[%d].path is required", i)
		}
		p, err := filepath.Abs(s.Path)
		if err != nil {
			return c, fmt.Errorf("source %q path: %w", s.Name, err)
		}
		s.Path = filepath.Clean(p)
		if len(s.Extensions) == 0 {
			return c, fmt.Errorf("source %q must define one or more extensions", s.Name)
		}
		for j, ext := range s.Extensions {
			ext = strings.ToLower(strings.TrimSpace(ext))
			if ext != "" && !strings.HasPrefix(ext, ".") {
				ext = "." + ext
			}
			if len(ext) < 2 || strings.ContainsAny(ext, `/\\*?`) {
				return c, fmt.Errorf("source %q has invalid extension %q", s.Name, ext)
			}
			s.Extensions[j] = ext
		}
		rel, err := filepath.Rel(s.Path, c.Destination)
		if err != nil {
			return c, err
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))) {
			return c, fmt.Errorf("destination must not be inside source %q", s.Name)
		}
		rel, err = filepath.Rel(c.Destination, s.Path)
		if err != nil {
			return c, err
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))) {
			return c, fmt.Errorf("source %q must not be inside destination", s.Name)
		}
	}
	return c, nil
}
