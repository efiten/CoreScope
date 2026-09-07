package main

import "testing"

func TestAutoRegionKeysDefaultsOff(t *testing.T) {
	// An absent block must not enable anything. This is the whole safety
	// story: every existing deployment upgrades into unchanged behaviour.
	cfg := &Config{}
	if cfg.AutoRegionKeysEnabled() {
		t.Error("AutoRegionKeysEnabled() = true on an empty config, want false")
	}
	if got := cfg.AutoRegionKeysMaxDerived(); got != 256 {
		t.Errorf("AutoRegionKeysMaxDerived() = %d, want the 256 default", got)
	}
	if got := cfg.AutoRegionKeysRefreshMinutes(); got != 15 {
		t.Errorf("AutoRegionKeysRefreshMinutes() = %d, want the 15 default", got)
	}
}

func TestAutoRegionKeysExplicitValues(t *testing.T) {
	cfg := &Config{AutoRegionKeys: &AutoRegionKeysConfig{Enabled: true, MaxDerived: 64, RefreshMinutes: 5}}
	if !cfg.AutoRegionKeysEnabled() {
		t.Error("AutoRegionKeysEnabled() = false, want true")
	}
	if got := cfg.AutoRegionKeysMaxDerived(); got != 64 {
		t.Errorf("AutoRegionKeysMaxDerived() = %d, want 64", got)
	}
	if got := cfg.AutoRegionKeysRefreshMinutes(); got != 5 {
		t.Errorf("AutoRegionKeysRefreshMinutes() = %d, want 5", got)
	}
}

func TestAutoRegionKeysRejectsNonPositiveOverrides(t *testing.T) {
	// A zero is indistinguishable from "absent" after json.Unmarshal, and a
	// negative is a typo. Both fall back to the default rather than silently
	// disabling derivation or panicking time.NewTicker.
	cfg := &Config{AutoRegionKeys: &AutoRegionKeysConfig{Enabled: true, MaxDerived: 0, RefreshMinutes: -1}}
	if got := cfg.AutoRegionKeysMaxDerived(); got != 256 {
		t.Errorf("AutoRegionKeysMaxDerived() = %d, want the 256 default", got)
	}
	if got := cfg.AutoRegionKeysRefreshMinutes(); got != 15 {
		t.Errorf("AutoRegionKeysRefreshMinutes() = %d, want the 15 default", got)
	}
}
