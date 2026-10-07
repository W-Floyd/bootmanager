package main

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestDirFiles(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "hooks-cleanup"), 0o755)
	os.WriteFile(filepath.Join(dir, "hooks-cleanup", "99-diag.sh"), []byte("#!/bin/sh\n"), 0o755)
	os.WriteFile(filepath.Join(dir, "note"), []byte("x"), 0o600)
	got := dirFiles(dir)
	var paths []string
	modes := map[string]uint64{}
	for _, f := range got {
		paths = append(paths, f.Path)
		modes[f.Path] = f.Mode
	}
	sort.Strings(paths)
	if len(paths) != 2 || paths[0] != "hooks-cleanup/99-diag.sh" || paths[1] != "note" {
		t.Fatalf("paths %q", paths)
	}
	if modes["hooks-cleanup/99-diag.sh"] != 0o755 || modes["note"] != 0o644 {
		t.Errorf("modes %v", modes)
	}
	if len(dirFiles(filepath.Join(dir, "absent"))) != 0 {
		t.Error("a missing dir gave files")
	}
}
