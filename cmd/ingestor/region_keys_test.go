package main

import (
	"strings"
	"testing"
)

func TestRegionNameAcceptable(t *testing.T) {
	cases := []struct {
		name string
		want bool
		why  string
	}{
		{"be", true, "ordinary short name"},
		{"nl-li-sit", true, "hyphenated hierarchical name"},
		{"fm-112", true, "digits are fine"},
		{"null", true, "looks like junk but is a legal name — no value blocklist"},
		{"", false, "empty"},
		{strings.Repeat("a", 33), false, "over the 32-char limit"},
		{strings.Repeat("a", 32), true, "exactly at the limit"},
		{"be,eu", false, "a comma is the regions_csv delimiter and would split on reload"},
		{"#be", false, "the firmware strips '#', so its presence signals a malformed entry"},
		{"be\x00", false, "NUL padding that a stale client failed to trim"},
		{"be eu", false, "whitespace inside a region name is never emitted by firmware"},
		{"bé", false, "non-ASCII: the key is SHA256 over bytes, so encoding drift would silently mismatch"},
	}
	for _, c := range cases {
		if got := regionNameAcceptable(c.name); got != c.want {
			t.Errorf("regionNameAcceptable(%q) = %v, want %v — %s", c.name, got, c.want, c.why)
		}
	}
}

func TestRankDeclaredRegionsPrefersWidelyDeclared(t *testing.T) {
	// The cap must drop the long tail of one-off local names, never a region
	// half the network declares. On live data "be" is declared by 127
	// repeaters and "behss" by 3.
	stats := []declaredRegionStat{
		{Name: "behss", Declarers: 3, LastSeen: "2026-09-07T10:00:00Z"},
		{Name: "be", Declarers: 127, LastSeen: "2026-09-01T10:00:00Z"},
		{Name: "sol3", Declarers: 1, LastSeen: "2026-09-07T11:00:00Z"},
	}
	got := rankDeclaredRegions(stats, 2)
	want := []string{"be", "behss"}
	if len(got) != len(want) {
		t.Fatalf("rankDeclaredRegions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rankDeclaredRegions = %v, want %v — declarer count must dominate recency", got, want)
		}
	}
}

func TestRankDeclaredRegionsIsDeterministic(t *testing.T) {
	// Equal declarer counts and equal timestamps must still produce a stable
	// order, or the derived tier churns between refreshes and the add/drop
	// logging becomes noise.
	stats := []declaredRegionStat{
		{Name: "zz", Declarers: 2, LastSeen: "2026-09-07T10:00:00Z"},
		{Name: "aa", Declarers: 2, LastSeen: "2026-09-07T10:00:00Z"},
	}
	for i := 0; i < 20; i++ {
		got := rankDeclaredRegions(stats, 10)
		if got[0] != "aa" || got[1] != "zz" {
			t.Fatalf("run %d: rankDeclaredRegions = %v, want [aa zz]", i, got)
		}
	}
}

func TestRankDeclaredRegionsBreaksTiesOnRecency(t *testing.T) {
	stats := []declaredRegionStat{
		{Name: "old", Declarers: 2, LastSeen: "2026-01-01T00:00:00Z"},
		{Name: "new", Declarers: 2, LastSeen: "2026-09-07T00:00:00Z"},
	}
	got := rankDeclaredRegions(stats, 1)
	if len(got) != 1 || got[0] != "new" {
		t.Fatalf("rankDeclaredRegions = %v, want [new] — equal declarers break on recency", got)
	}
}

func TestRankDeclaredRegionsDropsUnacceptableNames(t *testing.T) {
	stats := []declaredRegionStat{
		{Name: "be", Declarers: 5, LastSeen: "2026-09-07T10:00:00Z"},
		{Name: "#bad", Declarers: 99, LastSeen: "2026-09-07T10:00:00Z"},
	}
	got := rankDeclaredRegions(stats, 10)
	if len(got) != 1 || got[0] != "be" {
		t.Fatalf("rankDeclaredRegions = %v, want [be] — an unacceptable name must be dropped however widely declared", got)
	}
}

func TestSplitDeclaredRegionsCSV(t *testing.T) {
	// Exact inverse of the strings.Join the ingestor writes the column with.
	got := splitDeclaredRegionsCSV(" be , eu ,, nl ")
	want := []string{"be", "eu", "nl"}
	if len(got) != len(want) {
		t.Fatalf("splitDeclaredRegionsCSV = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("splitDeclaredRegionsCSV = %v, want %v", got, want)
		}
	}
	if n := len(splitDeclaredRegionsCSV("")); n != 0 {
		t.Errorf("empty csv produced %d entries, want 0", n)
	}
}
