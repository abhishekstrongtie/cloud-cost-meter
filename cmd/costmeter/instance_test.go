package main

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestSecondInstanceIsRefusedUntilTheFirstReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "costmeter.lock")

	first, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireLock(path); !errors.Is(err, errAlreadyRunning) {
		t.Errorf("second acquire: err = %v, want errAlreadyRunning", err)
	}

	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	again, err := acquireLock(path)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	again.Release()
}

func TestLockInAMissingDirectoryIsAnErrorNotAnotherInstance(t *testing.T) {
	_, err := acquireLock(filepath.Join(t.TempDir(), "no", "such", "dir", "x.lock"))
	if err == nil || errors.Is(err, errAlreadyRunning) {
		t.Errorf("err = %v", err)
	}
}

func TestWithToolDirs(t *testing.T) {
	tests := []struct{ name, path, want string }{
		{"minimal launchd path", "/usr/bin:/bin:/usr/sbin:/sbin", "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"},
		{"both already there", "/opt/homebrew/bin:/usr/bin:/usr/local/bin", "/opt/homebrew/bin:/usr/bin:/usr/local/bin"},
		{"one missing", "/opt/homebrew/bin:/usr/bin", "/usr/local/bin:/opt/homebrew/bin:/usr/bin"},
		{"empty", "", "/opt/homebrew/bin:/usr/local/bin"},
		{"a longer path is not a match", "/opt/homebrew/bin2:/usr/local/bin", "/opt/homebrew/bin:/opt/homebrew/bin2:/usr/local/bin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := withToolDirs(tt.path); got != tt.want {
				t.Errorf("withToolDirs(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
