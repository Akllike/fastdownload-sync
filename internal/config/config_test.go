package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadNormalizesExtensionsAndDefaultsState(t *testing.T) {
	root := t.TempDir()
	cfg := writeConfig(t, "sources:\n  - name: css-main\n    path: "+root+"\n    extensions: [bsp, .VTF]\ndestination: "+filepath.Join(t.TempDir(), "dest")+"\n")
	got, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sources[0].Extensions[0] != ".bsp" || got.Sources[0].Extensions[1] != ".vtf" {
		t.Fatalf("extensions not normalized: %#v", got.Sources[0].Extensions)
	}
	if got.StateFile != "/var/lib/fastdownload-sync/state.sqlite" {
		t.Fatalf("unexpected state default: %s", got.StateFile)
	}
}

func TestLoadRequiresExplicitExtensions(t *testing.T) {
	root := t.TempDir()
	cfg := writeConfig(t, "sources:\n  - name: css-main\n    path: "+root+"\ndestination: "+filepath.Join(t.TempDir(), "dest")+"\n")
	_, err := Load(cfg)
	if err == nil || !strings.Contains(err.Error(), "must define one or more extensions") {
		t.Fatalf("expected extension validation error, got %v", err)
	}
}

func TestLoadRejectsUnsafeSourceName(t *testing.T) {
	root := t.TempDir()
	cfg := writeConfig(t, "sources:\n  - name: ../escape\n    path: "+root+"\n    extensions: [.bsp]\ndestination: "+filepath.Join(t.TempDir(), "dest")+"\n")
	_, err := Load(cfg)
	if err == nil || !strings.Contains(err.Error(), "source name") {
		t.Fatalf("expected source name validation error, got %v", err)
	}
}

func TestLoadRejectsDestinationInsideSource(t *testing.T) {
	root := t.TempDir()
	cfg := writeConfig(t, "sources:\n  - name: css-main\n    path: "+root+"\n    extensions: [.bsp]\ndestination: "+filepath.Join(root, "fastdownload")+"\n")
	_, err := Load(cfg)
	if err == nil || !strings.Contains(err.Error(), "must not be inside source") {
		t.Fatalf("expected nested destination error, got %v", err)
	}
}
