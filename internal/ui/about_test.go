package ui

import (
	"strings"
	"testing"
)

func TestAboutTextNamesTheAppVersionAndDeveloper(t *testing.T) {
	got := AboutText("0.1.0")
	for _, want := range []string{"CostMeter 0.1.0", "Built by " + Developer, "Read-only", "az login"} {
		if !strings.Contains(got, want) {
			t.Errorf("AboutText missing %q in:\n%s", want, got)
		}
	}
}

func TestAboutTextFallsBackToDevWithoutAVersion(t *testing.T) {
	if got := AboutText(""); !strings.HasPrefix(got, "CostMeter dev\n") {
		t.Errorf("AboutText(\"\") starts %q, want CostMeter dev", strings.SplitN(got, "\n", 2)[0])
	}
}

// The text goes into an AppleScript string literal, so a quote or backslash would break it.
func TestAboutTextIsSafeInAnAppleScriptString(t *testing.T) {
	if got := AboutText("1.2.3"); strings.ContainsAny(got, "\"\\") {
		t.Errorf("AboutText contains a quote or backslash:\n%s", got)
	}
}
