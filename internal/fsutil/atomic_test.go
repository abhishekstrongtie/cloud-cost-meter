package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomicCreatesReplacesAndLeavesNoTempFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "state")
	path := filepath.Join(dir, "data.json")

	if err := WriteFileAtomic(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "two" {
		t.Errorf("content = %q", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "data.json" {
		t.Errorf("directory holds %v, want only data.json", entries)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestFailedWriteKeepsTheOldFileAndLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	if err := WriteFileAtomic(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(dir, 0o500); err != nil { // no new files can be created
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	if err := WriteFileAtomic(path, []byte("new"), 0o600); err == nil {
		t.Skip("directory permissions are not enforced here (running as root?)")
	}
	os.Chmod(dir, 0o700)
	if got, _ := os.ReadFile(path); string(got) != "original" {
		t.Errorf("old file damaged: %q", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("directory holds %v, want only data.json", entries)
	}
}
