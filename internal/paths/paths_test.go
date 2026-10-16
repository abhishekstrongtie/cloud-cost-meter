package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestForHomeNormal(t *testing.T) {
	got := ForHome("/h", false)
	want := Paths{
		Cache:      "/h/Library/Caches/costmeter/snapshots.json",
		AlertState: "/h/Library/Application Support/costmeter/alerts.json",
		Settings:   "/h/Library/Application Support/costmeter/settings.json",
		Lock:       "/h/Library/Application Support/costmeter/costmeter.lock",
		Log:        "/h/Library/Logs/costmeter.log",
	}
	if got != want {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestForHomeFake(t *testing.T) {
	got := ForHome("/h", true)
	want := Paths{
		Cache:      "/h/Library/Caches/costmeter-fake/snapshots.json",
		AlertState: "/h/Library/Application Support/costmeter-fake/alerts.json",
		Settings:   "/h/Library/Application Support/costmeter-fake/settings.json",
		Lock:       "/h/Library/Application Support/costmeter/costmeter.lock",
		Log:        "/h/Library/Logs/costmeter-fake.log",
	}
	if got != want {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestFakeNeverSharesStateWithReal(t *testing.T) {
	real, fake := ForHome("/h", false), ForHome("/h", true)
	if real.Cache == fake.Cache || real.AlertState == fake.AlertState || real.Settings == fake.Settings || real.Log == fake.Log {
		t.Errorf("fake paths overlap real paths:\nreal %+v\nfake %+v", real, fake)
	}
	if real.Lock != fake.Lock {
		t.Errorf("lock must be shared so only one instance runs: %q vs %q", real.Lock, fake.Lock)
	}
}

func TestNewCreatesDirectories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	p, err := New(true)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{p.Cache, p.AlertState, p.Settings, p.Lock, p.Log} {
		if fi, err := os.Stat(filepath.Dir(file)); err != nil || !fi.IsDir() {
			t.Errorf("directory for %s not created: %v", file, err)
		}
	}
	if filepath.Dir(p.Cache) != filepath.Join(home, "Library", "Caches", "costmeter-fake") {
		t.Errorf("paths not rooted at HOME: %s", p.Cache)
	}
}
