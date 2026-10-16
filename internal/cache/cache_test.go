package cache

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"costmeter/internal/provider"
	"costmeter/internal/provider/fake"
)

var now = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func fakeSnapshots(t *testing.T) []provider.Snapshot {
	t.Helper()
	p := fake.New()
	accts, _, _ := p.Accounts(context.Background())
	var out []provider.Snapshot
	for _, a := range accts {
		s, err := p.Snapshot(context.Background(), a, now)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// captureLog collects log output for the duration of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

func TestRoundTrip(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "snapshots.json"))
	want := fakeSnapshots(t)
	want[1].Stale, want[1].Err = true, "boom"
	want[1].Budget, want[1].ForecastMonth = nil, nil // nil pointers must survive too

	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	got := s.Load()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("loaded snapshots differ:\n got %+v\nwant %+v", got, want)
	}
}

func TestMissingFileIsEmptyAndQuiet(t *testing.T) {
	logs := captureLog(t)
	if got := New(filepath.Join(t.TempDir(), "nope.json")).Load(); got != nil {
		t.Errorf("Load = %+v, want nil", got)
	}
	if logs.Len() != 0 {
		t.Errorf("a missing cache is normal on first run, but it logged: %s", logs)
	}
}

func TestCorruptFileIsEmptyWithALogLine(t *testing.T) {
	logs := captureLog(t)
	path := filepath.Join(t.TempDir(), "snapshots.json")
	if err := os.WriteFile(path, []byte(`{"v":1,"snapshots":[{"Account":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := New(path).Load(); got != nil {
		t.Errorf("Load = %+v, want nil", got)
	}
	if !strings.Contains(logs.String(), "corrupt") {
		t.Errorf("log = %q, want a line saying the file is corrupt", logs)
	}
}

func TestUnknownVersionIsDiscarded(t *testing.T) {
	logs := captureLog(t)
	path := filepath.Join(t.TempDir(), "snapshots.json")
	if err := os.WriteFile(path, []byte(`{"v":99,"snapshots":[{}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := New(path).Load(); got != nil {
		t.Errorf("Load = %+v, want nil", got)
	}
	if !strings.Contains(logs.String(), "version 99") {
		t.Errorf("log = %q", logs)
	}
}

func TestSaveCreatesDirectoryOverwritesAndLeavesNoTempFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "cache")
	s := New(filepath.Join(dir, "snapshots.json"))

	if err := s.Save(fakeSnapshots(t)); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(fakeSnapshots(t)[:1]); err != nil {
		t.Fatal(err)
	}
	if got := s.Load(); len(got) != 1 {
		t.Errorf("second save did not replace the first: %d snapshots", len(got))
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "snapshots.json" {
		t.Errorf("directory holds %v, want only snapshots.json", entries)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "snapshots.json")); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestSavingNothingRoundTripsAsEmpty(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "snapshots.json"))
	if err := s.Save(nil); err != nil {
		t.Fatal(err)
	}
	if got := s.Load(); len(got) != 0 {
		t.Errorf("Load = %+v", got)
	}
}

func TestFailedSaveKeepsTheOldFile(t *testing.T) {
	dir := t.TempDir()
	s := New(filepath.Join(dir, "snapshots.json"))
	original := fakeSnapshots(t)
	if err := s.Save(original); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(dir, 0o500); err != nil { // no new files can be created
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	if err := s.Save(original[:1]); err == nil {
		t.Skip("directory permissions are not enforced here (running as root?)")
	}
	if got := s.Load(); !reflect.DeepEqual(got, original) {
		t.Errorf("old cache was damaged by a failed save: %+v", got)
	}
}
