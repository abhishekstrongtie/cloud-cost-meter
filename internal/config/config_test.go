package config

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestDefaultIsValidAndTracksEverything(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the defaults must be valid: %v", err)
	}
	if cfg.Refresh != 4*time.Hour || cfg.Azure.MaxServices != 5 || !cfg.Azure.Subscriptions.Auto {
		t.Errorf("defaults = %+v", cfg)
	}
}

func TestValidate(t *testing.T) {
	elevenSubs := make([]Subscription, 11)
	for i := range elevenSubs {
		elevenSubs[i] = Subscription{ID: fmt.Sprintf("sub-%02d", i)}
	}
	tests := []struct {
		name    string
		edit    func(*Config)
		wantErr string // "" = valid
	}{
		{"one hour is the minimum", func(c *Config) { c.Refresh = time.Hour }, ""},
		{"refresh too short", func(c *Config) { c.Refresh = 10 * time.Minute }, "refresh must be at least 1h"},
		{"refresh unset", func(c *Config) { c.Refresh = 0 }, "refresh must be at least 1h"},
		{"three services", func(c *Config) { c.Azure.MaxServices = 3 }, ""},
		{"too many services", func(c *Config) { c.Azure.MaxServices = 9 }, "services listed must be 1..5"},
		{"no services", func(c *Config) { c.Azure.MaxServices = 0 }, "services listed must be 1..5"},
		{"ten tracked scopes", func(c *Config) { c.Azure.Subscriptions = Subscriptions{List: elevenSubs[:10]} }, ""},
		{"eleven tracked scopes", func(c *Config) { c.Azure.Subscriptions = Subscriptions{List: elevenSubs} }, "at most 10"},
		{"nothing tracked is allowed", func(c *Config) { c.Azure.Subscriptions = Subscriptions{List: []Subscription{}} }, ""},
		{"a scope without an id", func(c *Config) { c.Azure.Subscriptions = Subscriptions{List: []Subscription{{Name: "x"}}} }, "has no id"},
		{"one subscription twice", func(c *Config) {
			c.Azure.Subscriptions = Subscriptions{List: []Subscription{{ID: "aaa", ResourceGroup: "rg-web"}, {ID: "AAA", ResourceGroup: "rg-data"}}}
		}, "only one scope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.edit(&cfg)
			err := cfg.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("err = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}
