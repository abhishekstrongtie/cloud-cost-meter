package main

import (
	"errors"
	"os"
	"slices"
	"strings"
	"syscall"
)

var errAlreadyRunning = errors.New("CostMeter is already running")

// instanceLock keeps two copies (say the LaunchAgent and a manual run) from running at once.
// It holds an exclusive flock on a file for as long as the process lives; the operating system
// drops the lock if the process dies, so a crash never leaves a stale lock behind.
type instanceLock struct{ f *os.File }

func acquireLock(path string) (*instanceLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errAlreadyRunning
		}
		return nil, err
	}
	return &instanceLock{f: f}, nil
}

func (l *instanceLock) Release() error { return l.f.Close() }

// toolDirs are where Homebrew puts `az`. An app started from Finder or launchd gets a minimal
// PATH that lacks them, and the Azure credential finds `az` by running it.
var toolDirs = []string{"/opt/homebrew/bin", "/usr/local/bin"}

// withToolDirs prepends the tool directories that path does not already contain.
func withToolDirs(path string) string {
	have := strings.Split(path, ":")
	var add []string
	for _, dir := range toolDirs {
		if !slices.Contains(have, dir) {
			add = append(add, dir)
		}
	}
	if len(add) == 0 {
		return path
	}
	if path == "" {
		return strings.Join(add, ":")
	}
	return strings.Join(add, ":") + ":" + path
}
