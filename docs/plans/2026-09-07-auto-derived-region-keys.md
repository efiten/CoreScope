# Auto-Derived Region Keys (M2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let the ingestor name transport-scoped traffic for regions the operator has not listed in `hashRegions`, by deriving keys from the region names repeaters declare over RF — opt-in, capped, and with a principled tie-break when two keys collide.

**Architecture:** `loadRegionKeys` returns a flat `map[string][]byte` built once at startup and threaded through thirteen call sites. It becomes a live `*regionKeySet` holding an immutable `*regionKeySnapshot` behind an `atomic.Pointer`: the ingest hot path takes one atomic load per packet, and a background refresh builds a replacement off to the side and swaps it in. The snapshot carries a single merged key map to iterate plus a membership set naming which keys came from `hashRegions`, which is what makes the tie-break possible.

**Tech Stack:** Go 1.x (`cmd/ingestor`, stdlib `testing`, `sync/atomic`, `crypto/hmac`), SQLite via `modernc.org/sqlite`.

**Spec:** `docs/specs/2026-09-07-auto-region-keys-design.md`, sections 1–3.

**Depends on:** nothing. Independent of M1 (`docs/plans/2026-09-07-scope-audit-unmatched-caveat.md`), which touches only `cmd/server` and `public/`. The two can land in either order.

---

## Design constraints the implementer must not relax

- **Default off.** `autoRegionKeys.enabled` absent or `false` must leave behaviour byte-for-byte identical to today. This is asserted by a test, not by inspection.
- **Naming a packet wrongly is worse than not naming it.** The existing `""` (ambiguous) outcome stays the fallback. Tier 2 resolves only the case where operator config and RF hearsay disagree; it never picks between two equally-sourced candidates.
- **No schema migration, no new column.** The match reason goes to logs and an in-process counter.
- **The work is O(keys) per transport-scoped packet and cannot be indexed** — `code1` is an HMAC over the payload, so there is no payload-independent lookup key. The comment in `matchScope` suggesting a "pre-indexed lookup table" is not achievable; delete it rather than leaving a false lead. This is why the cap exists.

---

## File Structure

| File | Responsibility | Change |
|---|---|---|
| `cmd/ingestor/region_keys.go` | `regionKeySnapshot`, `regionKeySet`, candidate filter, ranking, `scopeMatch` | **Create** |
| `cmd/ingestor/region_keys_test.go` | Unit tests + benchmark for the above | **Create** |
| `cmd/ingestor/config.go` | `AutoRegionKeysConfig` + accessors | Modify |
| `cmd/ingestor/db.go` | `DeclaredRegionStats`; `BuildPacketData` / `BackfillDefaultScopeAsync` signatures | Modify |
| `cmd/ingestor/client_reception.go` | `handleClientPacket` / `buildClientRxObservation` signatures | Modify |
| `cmd/ingestor/main.go` | `matchScope` removal, startup wiring, refresh ticker | Modify |
| `cmd/ingestor/scope_repair.go` | Build the same key set before scanning | Modify |
| `config.example.json` | `autoRegionKeys` block + explainer comment | Modify |
| `docs/client-regions.md` | Document the second consumer of `node_declared_regions` | Modify |

`docs/api-spec.md` needs **no** change in this plan: M2 alters how the ingestor names a scope, not any server response shape. If you find yourself editing it, you have changed an API surface that was not in scope — stop and re-read the spec.

---

### Task 1: Config block, default off

**Files:**
- Modify: `cmd/ingestor/config.go` (`Config` struct ~line 61, accessors near `ClientRegionsEnabled` ~line 183)
- Test: `cmd/ingestor/config_test.go` (create the file if absent)

- [ ] **Step 1: Write the failing test**

```go
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
	// negative is a typo. Both fall back to the default rather than
	// silently disabling derivation or spinning the refresh ticker.
	cfg := &Config{AutoRegionKeys: &AutoRegionKeysConfig{Enabled: true, MaxDerived: 0, RefreshMinutes: -1}}
	if got := cfg.AutoRegionKeysMaxDerived(); got != 256 {
		t.Errorf("AutoRegionKeysMaxDerived() = %d, want the 256 default", got)
	}
	if got := cfg.AutoRegionKeysRefreshMinutes(); got != 15 {
		t.Errorf("AutoRegionKeysRefreshMinutes() = %d, want the 15 default", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/ingestor && go test ./... -run TestAutoRegionKeys -v`
Expected: FAIL to compile — `undefined: AutoRegionKeysConfig`

- [ ] **Step 3: Implement**

In `cmd/ingestor/config.go`, add to the `Config` struct after `ClientRegions`:

```go
	AutoRegionKeys       *AutoRegionKeysConfig       `json:"autoRegionKeys,omitempty"`
```

And after `ClientRegionsEnabled`:

```go
// AutoRegionKeysConfig controls the opt-in derivation of region keys from the
// names repeaters declare over RF (node_declared_regions), on top of the
// explicit hashRegions list.
//
// TOP-LEVEL BLOCK, a sibling of hashRegions — not nested inside it. Config
// loading is plain json.Unmarshal with no DisallowUnknownFields, so a
// mis-nested key is silently ignored and derivation stays off with no error,
// the same trap clientRxObservations documents.
type AutoRegionKeysConfig struct {
	Enabled        bool `json:"enabled"`
	MaxDerived     int  `json:"maxDerived"`
	RefreshMinutes int  `json:"refreshMinutes"`
}

// autoRegionKeysDefaultMaxDerived bounds the derived tier. Every added key
// raises the random ambiguity rate by 1/65536 per scoped packet (code1 is two
// bytes), and costs one more HMAC per transport-scoped packet — the match is
// O(keys) and cannot be indexed. 256 on top of a typical explicit set puts the
// ambiguity rate near 0.5%, which is the ceiling this design accepts.
const autoRegionKeysDefaultMaxDerived = 256

// autoRegionKeysDefaultRefreshMinutes is how often the derived tier is rebuilt
// from the database. Declared-region answers arrive at human pace (a drive-by
// with a companion app, or a 24h observer report), so minutes-scale staleness
// is irrelevant and a tighter interval only burns queries.
const autoRegionKeysDefaultRefreshMinutes = 15

// AutoRegionKeysEnabled reports whether region keys may be derived from
// declared-region answers. Default false.
func (c *Config) AutoRegionKeysEnabled() bool {
	return c.AutoRegionKeys != nil && c.AutoRegionKeys.Enabled
}

// AutoRegionKeysMaxDerived returns the derived-tier cap, falling back to the
// default for absent, zero, or negative values — a zero is indistinguishable
// from "key omitted" after json.Unmarshal, and neither should silently mean
// "derive nothing" when the operator has switched the feature on.
func (c *Config) AutoRegionKeysMaxDerived() int {
	if c.AutoRegionKeys == nil || c.AutoRegionKeys.MaxDerived <= 0 {
		return autoRegionKeysDefaultMaxDerived
	}
	return c.AutoRegionKeys.MaxDerived
}

// AutoRegionKeysRefreshMinutes returns the refresh interval in minutes,
// falling back to the default for absent, zero, or negative values — a zero
// here would otherwise panic time.NewTicker.
func (c *Config) AutoRegionKeysRefreshMinutes() int {
	if c.AutoRegionKeys == nil || c.AutoRegionKeys.RefreshMinutes <= 0 {
		return autoRegionKeysDefaultRefreshMinutes
	}
	return c.AutoRegionKeys.RefreshMinutes
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/ingestor && go test ./... -run TestAutoRegionKeys -v`
Expected: PASS (3 tests)

- [ ] **Step 5: Commit**

```bash
git add cmd/ingestor/config.go cmd/ingestor/config_test.go
git commit -m "feat(ingestor): autoRegionKeys config block, default off"
```

---

### Task 2: Candidate filter and ranking

Pure functions, no database. Built before the DB read so the ranking rules are pinned by tests independent of SQL.

**Files:**
- Create: `cmd/ingestor/region_keys.go`
- Create: `cmd/ingestor/region_keys_test.go`

- [ ] **Step 1: Write the failing test**

Create `cmd/ingestor/region_keys_test.go`:

```go
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
	// order, or the derived tier churns between refreshes and the logs become
	// unreadable.
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/ingestor && go test ./... -run 'TestRegionName|TestRankDeclared' -v`
Expected: FAIL to compile — `undefined: regionNameAcceptable`, `undefined: declaredRegionStat`

- [ ] **Step 3: Implement**

Create `cmd/ingestor/region_keys.go`:

```go
package main

import (
	"sort"
	"strings"
)

// declaredRegionStat is one region name as reported over RF, with the two
// facts the cap ranks on: how many distinct repeaters declare it, and how
// recently any of them last answered.
type declaredRegionStat struct {
	Name      string
	Declarers int
	LastSeen  string // ISO, greatest observed_at across declarers
}

// maxRegionNameLen bounds a derived region name. Firmware region names are
// short labels; anything longer is a malformed or hostile entry, and each
// accepted name costs an HMAC on every transport-scoped packet.
const maxRegionNameLen = 32

// regionNameAcceptable reports whether a declared name may become a derived
// region key.
//
// The rules are structural, never about the name's meaning. The declared set
// contains entries that look like junk ("null", "bierhuis", "sol3"), but a
// blocklist on string values is unmaintainable and the cost of one bad name is
// a single slot out of maxDerived plus a 1-in-65536 collision chance. What IS
// rejected is anything that could not have come from the firmware intact:
//
//   - a comma would split the name on the next regions_csv round-trip
//   - a '#' cannot appear (the firmware strips it), so its presence means the
//     value was mangled somewhere upstream
//   - non-ASCII or whitespace would make the key SHA256 over bytes nobody
//     intended, silently mismatching the sender
//   - a NUL is the block-cipher padding a stale client failed to trim
func regionNameAcceptable(name string) bool {
	if name == "" || len(name) > maxRegionNameLen {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c <= ' ' || c >= 0x7F || c == ',' || c == '#' {
			return false
		}
	}
	return true
}

// rankDeclaredRegions filters stats through regionNameAcceptable and returns at
// most max names, most-worth-keeping first: by declarer count descending, then
// by recency, then by name. The name tie-break is what makes the result
// deterministic — without it the derived tier would churn between refreshes on
// equally-ranked names and the add/drop logging would be noise.
func rankDeclaredRegions(stats []declaredRegionStat, max int) []string {
	kept := make([]declaredRegionStat, 0, len(stats))
	for _, s := range stats {
		if regionNameAcceptable(s.Name) {
			kept = append(kept, s)
		}
	}
	sort.Slice(kept, func(i, j int) bool {
		if kept[i].Declarers != kept[j].Declarers {
			return kept[i].Declarers > kept[j].Declarers
		}
		if kept[i].LastSeen != kept[j].LastSeen {
			return kept[i].LastSeen > kept[j].LastSeen
		}
		return kept[i].Name < kept[j].Name
	})
	if max > 0 && len(kept) > max {
		kept = kept[:max]
	}
	names := make([]string, 0, len(kept))
	for _, s := range kept {
		names = append(names, s.Name)
	}
	return names
}

// splitDeclaredRegionsCSV parses a regions_csv value into its entries. The
// ingestor writes this column with strings.Join(regions, ","), so this is its
// exact inverse. Mirrors splitRegionsCSV in cmd/server/scopes.go.
func splitDeclaredRegionsCSV(csv string) []string {
	out := []string{}
	if csv == "" {
		return out
	}
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/ingestor && go test ./... -run 'TestRegionName|TestRankDeclared' -v`
Expected: PASS (5 tests)

- [ ] **Step 5: Commit**

```bash
git add cmd/ingestor/region_keys.go cmd/ingestor/region_keys_test.go
git commit -m "feat(ingestor): candidate filter and deterministic ranking for derived region names"
```

---

### Task 3: The key set — two tiers behind an atomic snapshot

**Files:**
- Modify: `cmd/ingestor/region_keys.go`
- Test: `cmd/ingestor/region_keys_test.go`

- [ ] **Step 1: Write the failing test**

Append to `cmd/ingestor/region_keys_test.go`:

```go
func TestRegionKeySetExplicitOnlyWhenDisabled(t *testing.T) {
	// Derivation off: the snapshot must be exactly what loadRegionKeys built,
	// and refreshDerived must be a no-op rather than a quiet opt-in.
	cfg := &Config{HashRegions: []string{"#be"}}
	set := newRegionKeySet(cfg)
	set.refreshDerived([]string{"behss", "fm-112"})

	snap := set.snapshot()
	if len(snap.all) != 1 {
		t.Fatalf("len(all) = %d, want 1 — refreshDerived must not add keys when disabled", len(snap.all))
	}
	if _, ok := snap.all["#be"]; !ok {
		t.Error("want the explicit #be key present")
	}
	if !snap.isExplicit("#be") {
		t.Error("isExplicit(#be) = false, want true")
	}
}

func TestRegionKeySetMergesDerivedWhenEnabled(t *testing.T) {
	cfg := &Config{
		HashRegions:    []string{"#be"},
		AutoRegionKeys: &AutoRegionKeysConfig{Enabled: true},
	}
	set := newRegionKeySet(cfg)
	set.refreshDerived([]string{"behss", "be"}) // "be" duplicates the explicit key

	snap := set.snapshot()
	if len(snap.all) != 2 {
		t.Fatalf("len(all) = %d, want 2 (#be explicit + #behss derived), got keys %v", len(snap.all), keyNames(snap))
	}
	if _, ok := snap.all["#behss"]; !ok {
		t.Errorf("want the derived #behss key present, got %v", keyNames(snap))
	}
	if snap.isExplicit("#behss") {
		t.Error("isExplicit(#behss) = true, want false — a derived key is not operator config")
	}
	if !snap.isExplicit("#be") {
		t.Error("isExplicit(#be) = false, want true — an explicit key must not be demoted by a duplicate declaration")
	}
}

func TestRegionKeySetRefreshReplacesRatherThanAccumulates(t *testing.T) {
	// A region that stops being declared must leave the derived tier, or the
	// key set only ever grows and the cap stops meaning anything.
	cfg := &Config{AutoRegionKeys: &AutoRegionKeysConfig{Enabled: true}}
	set := newRegionKeySet(cfg)
	set.refreshDerived([]string{"aa"})
	set.refreshDerived([]string{"bb"})

	snap := set.snapshot()
	if _, ok := snap.all["#aa"]; ok {
		t.Error("want #aa gone after a refresh that no longer lists it")
	}
	if _, ok := snap.all["#bb"]; !ok {
		t.Error("want #bb present after the refresh that lists it")
	}
}

func TestRegionKeySetSnapshotIsStable(t *testing.T) {
	// A snapshot handed to a packet must not change under it mid-match.
	cfg := &Config{AutoRegionKeys: &AutoRegionKeysConfig{Enabled: true}}
	set := newRegionKeySet(cfg)
	set.refreshDerived([]string{"aa"})
	held := set.snapshot()
	set.refreshDerived([]string{"bb"})

	if _, ok := held.all["#aa"]; !ok {
		t.Error("the held snapshot lost #aa — snapshots must be immutable, not aliases of live state")
	}
}

// keyNames is a test helper for readable failure messages.
func keyNames(s *regionKeySnapshot) []string {
	out := make([]string, 0, len(s.all))
	for k := range s.all {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
```

Add `"sort"` to the test file's imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/ingestor && go test ./... -run TestRegionKeySet -v`
Expected: FAIL to compile — `undefined: newRegionKeySet`

- [ ] **Step 3: Implement**

Append to `cmd/ingestor/region_keys.go` (add `"crypto/sha256"` and `"sync/atomic"` to its imports):

```go
// regionKeySnapshot is an immutable view of the region keys in force for one
// packet. `all` is the single map matching iterates — merging at build time
// rather than per packet keeps the hot path free of allocation. `explicit`
// carries membership only, and exists so the ambiguity tie-break can tell an
// operator-configured region from one derived off the air.
type regionKeySnapshot struct {
	all      map[string][]byte
	explicit map[string]bool
}

func (s *regionKeySnapshot) isExplicit(name string) bool { return s.explicit[name] }

// regionKeySet holds the live snapshot. Readers take one atomic load; a
// refresh builds the replacement off to the side and swaps the pointer, so the
// ingest hot path never blocks on a rebuild (AGENTS.md rule 0).
type regionKeySet struct {
	cur     atomic.Pointer[regionKeySnapshot]
	enabled bool
	max     int
}

// newRegionKeySet builds the explicit tier from hashRegions. The derived tier
// starts empty; refreshDerived fills it, and does nothing at all when
// autoRegionKeys is off.
func newRegionKeySet(cfg *Config) *regionKeySet {
	explicitKeys := loadRegionKeys(cfg)
	explicitNames := make(map[string]bool, len(explicitKeys))
	all := make(map[string][]byte, len(explicitKeys))
	for name, key := range explicitKeys {
		explicitNames[name] = true
		all[name] = key
	}
	s := &regionKeySet{
		enabled: cfg.AutoRegionKeysEnabled(),
		max:     cfg.AutoRegionKeysMaxDerived(),
	}
	s.cur.Store(&regionKeySnapshot{all: all, explicit: explicitNames})
	return s
}

func (s *regionKeySet) snapshot() *regionKeySnapshot { return s.cur.Load() }

// refreshDerived rebuilds the derived tier from names (already ranked and
// capped by the caller) and swaps in a new snapshot. It REPLACES the derived
// tier rather than merging into it, so a region that stops being declared
// leaves the key set and the cap keeps meaning something.
//
// A name that duplicates an explicit key is skipped, not re-added: the
// explicit tier must stay authoritative for the tie-break, and demoting a
// configured region because a repeater also declares it would invert the whole
// rule.
//
// Returns the names actually added, for the caller to log.
func (s *regionKeySet) refreshDerived(names []string) []string {
	if !s.enabled {
		return nil
	}
	old := s.cur.Load()
	all := make(map[string][]byte, len(old.explicit)+len(names))
	for name := range old.explicit {
		all[name] = old.all[name]
	}
	added := make([]string, 0, len(names))
	for _, raw := range names {
		if !regionNameAcceptable(raw) {
			continue
		}
		name := "#" + raw
		if old.explicit[name] {
			continue
		}
		if _, exists := all[name]; exists {
			continue
		}
		h := sha256.Sum256([]byte(name))
		all[name] = h[:16]
		added = append(added, name)
	}
	s.cur.Store(&regionKeySnapshot{all: all, explicit: old.explicit})
	return added
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/ingestor && go test ./... -run TestRegionKeySet -v`
Expected: PASS (4 tests)

- [ ] **Step 5: Run the race detector**

Run: `cd cmd/ingestor && go test ./... -race -run TestRegionKeySet`
Expected: PASS with no race reported.

- [ ] **Step 6: Commit**

```bash
git add cmd/ingestor/region_keys.go cmd/ingestor/region_keys_test.go
git commit -m "feat(ingestor): two-tier regionKeySet behind an atomic snapshot"
```

---

### Task 4: Tiered matching with a reason

**Files:**
- Modify: `cmd/ingestor/region_keys.go`
- Modify: `cmd/ingestor/main.go` (delete `matchScope`, keep `matchingRegions`)
- Test: `cmd/ingestor/region_keys_test.go`

- [ ] **Step 1: Write the failing test**

Append to `cmd/ingestor/region_keys_test.go`:

```go
// codeFor derives the on-wire code1 a sender in region `name` would emit for
// this payload — the same computation matchingRegions inverts. Used to build
// packets that genuinely belong to a region rather than asserting on a
// hardcoded string.
func codeFor(name string, payloadType byte, payload []byte) string {
	if !strings.HasPrefix(name, "#") {
		name = "#" + name
	}
	sum := sha256.Sum256([]byte(name))
	mac := hmac.New(sha256.New, sum[:16])
	mac.Write([]byte{payloadType})
	mac.Write(payload)
	h := mac.Sum(nil)
	code := uint16(h[0]) | uint16(h[1])<<8
	if code == 0 {
		code = 1
	} else if code == 0xFFFF {
		code = 0xFFFE
	}
	return strings.ToUpper(hex.EncodeToString([]byte{byte(code & 0xFF), byte(code >> 8)}))
}

func TestScopeMatchUniqueNamesTheRegion(t *testing.T) {
	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	cfg := &Config{HashRegions: []string{"#be"}}
	set := newRegionKeySet(cfg)
	code := codeFor("#be", 5, payload)

	got := set.snapshot().match(5, payload, code)
	if got.Name != "#be" {
		t.Errorf("Name = %q, want %q", got.Name, "#be")
	}
	if got.Reason != scopeReasonUnique {
		t.Errorf("Reason = %q, want %q", got.Reason, scopeReasonUnique)
	}
}

func TestScopeMatchNoKeyMatches(t *testing.T) {
	cfg := &Config{HashRegions: []string{"#be"}}
	set := newRegionKeySet(cfg)

	got := set.snapshot().match(5, []byte{1, 2, 3}, "0000")
	if got.Name != "" || got.Reason != scopeReasonNone {
		t.Errorf("got %+v, want an empty name with reason %q", got, scopeReasonNone)
	}
}

func TestScopeMatchExplicitBeatsDerived(t *testing.T) {
	// The ambiguity this feature introduces: a derived key collides with an
	// operator-configured one on this payload. Operator config wins — it is
	// intent, the derived name is hearsay picked up over RF.
	payload := []byte{0x01, 0x02, 0x03, 0x04}
	cfg := &Config{HashRegions: []string{"#be"}, AutoRegionKeys: &AutoRegionKeysConfig{Enabled: true}}
	set := newRegionKeySet(cfg)
	code := codeFor("#be", 5, payload)

	// Force the collision rather than searching for a natural one: inject a
	// derived key whose bytes are the explicit key's, so both match.
	snap := set.snapshot()
	collide := make(map[string][]byte, len(snap.all)+1)
	for k, v := range snap.all {
		collide[k] = v
	}
	collide["#collider"] = snap.all["#be"]
	forced := &regionKeySnapshot{all: collide, explicit: snap.explicit}

	got := forced.match(5, payload, code)
	if got.Name != "#be" {
		t.Errorf("Name = %q, want %q — the explicit key must win", got.Name, "#be")
	}
	if got.Reason != scopeReasonExplicitOverDerived {
		t.Errorf("Reason = %q, want %q", got.Reason, scopeReasonExplicitOverDerived)
	}
	if len(got.Candidates) != 2 {
		t.Errorf("Candidates = %v, want both names recorded for the log", got.Candidates)
	}
}

func TestScopeMatchTwoExplicitKeysStayAmbiguous(t *testing.T) {
	// Two equally-sourced candidates: naming either would be a guess, and
	// naming wrongly is worse than not naming. This is #1609's rule, unchanged.
	payload := []byte{0x09, 0x08, 0x07}
	cfg := &Config{HashRegions: []string{"#be", "#eu"}}
	set := newRegionKeySet(cfg)
	code := codeFor("#be", 5, payload)

	snap := set.snapshot()
	collide := map[string][]byte{"#be": snap.all["#be"], "#eu": snap.all["#be"]}
	forced := &regionKeySnapshot{all: collide, explicit: snap.explicit}

	got := forced.match(5, payload, code)
	if got.Name != "" {
		t.Errorf("Name = %q, want \"\" — two explicit candidates must abstain", got.Name)
	}
	if got.Reason != scopeReasonAmbiguous {
		t.Errorf("Reason = %q, want %q", got.Reason, scopeReasonAmbiguous)
	}
}

func TestScopeMatchTwoDerivedKeysStayAmbiguous(t *testing.T) {
	// The tier-3 case, deliberately NOT resolved in M2. It must abstain rather
	// than pick, and the reason must say ambiguous so the log can measure how
	// often this happens before tier 3 is built.
	payload := []byte{0x11, 0x22}
	cfg := &Config{AutoRegionKeys: &AutoRegionKeysConfig{Enabled: true}}
	set := newRegionKeySet(cfg)
	set.refreshDerived([]string{"aa", "bb"})

	snap := set.snapshot()
	collide := map[string][]byte{"#aa": snap.all["#aa"], "#bb": snap.all["#aa"]}
	forced := &regionKeySnapshot{all: collide, explicit: snap.explicit}
	code := codeFor("#aa", 5, payload)

	got := forced.match(5, payload, code)
	if got.Name != "" || got.Reason != scopeReasonAmbiguous {
		t.Errorf("got %+v, want an empty name with reason %q", got, scopeReasonAmbiguous)
	}
}
```

Add `"crypto/hmac"`, `"crypto/sha256"`, and `"encoding/hex"` to the test file's imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/ingestor && go test ./... -run TestScopeMatch -v`
Expected: FAIL to compile — `snap.match undefined`, `undefined: scopeReasonUnique`

- [ ] **Step 3: Implement**

Append to `cmd/ingestor/region_keys.go`:

```go
// scopeReason records how a scope match was decided, so the outcome is
// auditable in logs without a schema change. It is deliberately not stored:
// transmissions.scope_name keeps its existing three-state encoding.
type scopeReason string

const (
	scopeReasonNone                scopeReason = "none"                  // no key matched
	scopeReasonUnique              scopeReason = "unique"                // exactly one key matched
	scopeReasonExplicitOverDerived scopeReason = "explicit-over-derived" // several matched, one was operator config
	scopeReasonAmbiguous           scopeReason = "ambiguous"             // several matched, no principled winner
)

// scopeMatch is the result of naming one packet's region scope.
type scopeMatch struct {
	Name       string      // "" when unresolved — the caller stores that as the unmatched state
	Reason     scopeReason
	Candidates []string    // every matching name, populated only when more than one matched
}

// match names the region scope of a transport-scoped packet, resolving a
// multi-key collision by evidence rather than by map order.
//
// Tiers, in order:
//
//  1. Exactly one key matched — name it.
//  2. Several matched but exactly one came from hashRegions — name that one.
//     The operator's own configuration outranks a name picked up off the air,
//     and this covers the bulk of the ambiguity auto-derivation introduces.
//  3. Otherwise abstain, returning "". Two equally-sourced candidates offer no
//     principled winner, and naming a packet wrongly is worse than leaving it
//     unnamed — the rule #1609 established, unchanged.
//
// (The spec's tier-3 path-evidence tie-break sits between 2 and 3 and is
// deliberately not built here; see docs/specs/2026-09-07-auto-region-keys-design.md.
// The scopeReasonAmbiguous counter is what measures whether it is worth building.)
func (s *regionKeySnapshot) match(payloadType byte, payloadRaw []byte, code1 string) scopeMatch {
	matched := matchingRegions(s.all, payloadType, payloadRaw, code1)
	switch len(matched) {
	case 0:
		return scopeMatch{Reason: scopeReasonNone}
	case 1:
		return scopeMatch{Name: matched[0], Reason: scopeReasonUnique}
	}

	var explicitMatches []string
	for _, name := range matched {
		if s.explicit[name] {
			explicitMatches = append(explicitMatches, name)
		}
	}
	if len(explicitMatches) == 1 {
		return scopeMatch{Name: explicitMatches[0], Reason: scopeReasonExplicitOverDerived, Candidates: matched}
	}
	return scopeMatch{Reason: scopeReasonAmbiguous, Candidates: matched}
}
```

- [ ] **Step 4: Delete the superseded `matchScope`**

In `cmd/ingestor/main.go`, delete the `matchScope` function and its doc comment entirely (the block ending `return ""` just above `matchingRegions`). Keep `matchingRegions` unchanged — `match` calls it.

While deleting, note that the old comment's suggestion to "consider a pre-indexed lookup table" beyond 50 regions goes with it. That is not achievable: `code1` is an HMAC over the packet payload, so there is no payload-independent key to index on. The cost is inherently one HMAC per configured region per transport-scoped packet, which is precisely why `maxDerived` exists.

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd cmd/ingestor && go test ./... -run TestScopeMatch -v`
Expected: PASS (5 tests). The build will still fail elsewhere until Task 5 updates the call sites — that is expected; run with `-run` scoped as shown, and do not "fix" the callers yet.

- [ ] **Step 6: Commit**

```bash
git add cmd/ingestor/region_keys.go cmd/ingestor/region_keys_test.go cmd/ingestor/main.go
git commit -m "feat(ingestor): tiered scope matching with an explicit-over-derived tie-break"
```

---

### Task 5: Thread `*regionKeySet` through the call sites

Mechanical but wide. All thirteen sites, in one commit, so the tree is never half-converted.

**Files:**
- Modify: `cmd/ingestor/main.go` (lines ~111, 191, 686, 732, 973, 1008)
- Modify: `cmd/ingestor/client_reception.go` (lines ~26, 96, 425, 450)
- Modify: `cmd/ingestor/db.go` (lines ~1610, 2133, 2186)

- [ ] **Step 1: Change the signatures**

Replace the parameter type `regionKeys map[string][]byte` with `regionSet *regionKeySet` in:

| File | Function |
|---|---|
| `client_reception.go` | `handleClientPacket` |
| `client_reception.go` | `buildClientRxObservation` |
| `db.go` | `BackfillDefaultScopeAsync` |
| `db.go` | `BuildPacketData` |
| `main.go` | `handleMessage` |

- [ ] **Step 2: Add the counter**

Append to `cmd/ingestor/region_keys.go` (add `"log"` to imports):

```go
// scopeMatchCounters tallies how each transport-scoped packet's region was
// decided. It exists to answer one question before more machinery is built:
// how often does an ambiguous collision actually happen? The spec gates the
// path-evidence tie-break (tier 3) on this number.
var scopeMatchCounters struct {
	unique               atomic.Int64
	explicitOverDerived  atomic.Int64
	ambiguous            atomic.Int64
	none                 atomic.Int64
}

// recordScopeMatch tallies one decision and logs the interesting ones. Unique
// and none are the overwhelming majority and are counted silently; the other
// two are rare by construction and worth a line each.
func recordScopeMatch(m scopeMatch) {
	switch m.Reason {
	case scopeReasonUnique:
		scopeMatchCounters.unique.Add(1)
	case scopeReasonNone:
		scopeMatchCounters.none.Add(1)
	case scopeReasonExplicitOverDerived:
		scopeMatchCounters.explicitOverDerived.Add(1)
		log.Printf("[regions] collision resolved to explicit %s over derived candidates %v", m.Name, m.Candidates)
	case scopeReasonAmbiguous:
		scopeMatchCounters.ambiguous.Add(1)
		log.Printf("[regions] ambiguous collision between %v; storing unmatched", m.Candidates)
	}
}

// logScopeMatchCounters prints the running tally. Called from the refresh
// ticker so the numbers arrive on the same cadence as the key-set changes that
// move them.
func logScopeMatchCounters() {
	log.Printf("[regions] scope matches: unique=%d explicit-over-derived=%d ambiguous=%d none=%d",
		scopeMatchCounters.unique.Load(), scopeMatchCounters.explicitOverDerived.Load(),
		scopeMatchCounters.ambiguous.Load(), scopeMatchCounters.none.Load())
}
```

- [ ] **Step 3: Change the two match call sites**

In `client_reception.go`, replace:

```go
		if decoded.TransportCodes.Code1 != "0000" {
			sn := matchScope(regionKeys, byte(decoded.Header.PayloadType), decoded.payloadRaw, decoded.TransportCodes.Code1)
```

with:

```go
		if decoded.TransportCodes.Code1 != "0000" {
			m := regionSet.snapshot().match(byte(decoded.Header.PayloadType), decoded.payloadRaw, decoded.TransportCodes.Code1)
			recordScopeMatch(m)
			sn := m.Name
```

In `db.go`, inside `BuildPacketData`, replace:

```go
			pd.ScopeName = matchScope(regionKeys, byte(decoded.Header.PayloadType), decoded.payloadRaw, decoded.TransportCodes.Code1)
```

with:

```go
			m := regionSet.snapshot().match(byte(decoded.Header.PayloadType), decoded.payloadRaw, decoded.TransportCodes.Code1)
			recordScopeMatch(m)
			pd.ScopeName = m.Name
```

- [ ] **Step 4: Update the callers**

In `main.go`, change line ~111:

```go
	regionSet := newRegionKeySet(cfg)
	store.BackfillDefaultScopeAsync(regionSet)
```

and pass `regionSet` instead of `regionKeys` at every call to `handleMessage`, `handleClientPacket`, and `BuildPacketData`.

In `db.go`'s `BackfillDefaultScopeAsync`, replace the `len(regionKeys) == 0` early return with:

```go
	if len(regionSet.snapshot().all) == 0 {
```

In `client_reception.go`, pass `regionSet` through to `buildClientRxObservation`.

- [ ] **Step 5: Build and run the full suite**

Run: `cd cmd/ingestor && go build ./... && go test ./...`
Expected: build succeeds, all tests PASS. `scope_repair.go` still compiles because it calls `matchingRegions` directly, not `matchScope` — Task 7 changes its key source.

- [ ] **Step 6: Commit**

```bash
git add cmd/ingestor/
git commit -m "refactor(ingestor): thread *regionKeySet through the ingest path"
```

---

### Task 6: Read declared region names from the database

**Files:**
- Modify: `cmd/ingestor/db.go`
- Test: `cmd/ingestor/region_keys_test.go`

- [ ] **Step 1: Write the failing test**

Append to `cmd/ingestor/region_keys_test.go`:

```go
func TestDeclaredRegionStatsAggregatesLatestAnswerPerTarget(t *testing.T) {
	store := newTestStore(t)
	// Two answers from the same target: only the newer one counts, exactly as
	// CurrentDeclaredRegions orders (by observed_at, never ingested_at — a
	// drive buffered offline can arrive days late).
	insertDeclaredRegionsRow(t, store, "aa"+strings.Repeat("11", 31), "2026-09-01T00:00:00Z", "be,old")
	insertDeclaredRegionsRow(t, store, "aa"+strings.Repeat("11", 31), "2026-09-07T00:00:00Z", "be,new")
	insertDeclaredRegionsRow(t, store, "bb"+strings.Repeat("22", 31), "2026-09-05T00:00:00Z", "be")

	stats, err := store.DeclaredRegionStats()
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]declaredRegionStat{}
	for _, s := range stats {
		byName[s.Name] = s
	}
	if got := byName["be"].Declarers; got != 2 {
		t.Errorf("be declarers = %d, want 2", got)
	}
	if got := byName["be"].LastSeen; got != "2026-09-07T00:00:00Z" {
		t.Errorf("be lastSeen = %q, want the greatest observed_at", got)
	}
	if _, ok := byName["old"]; ok {
		t.Error("want the superseded answer's region gone — only the latest answer per target counts")
	}
	if got := byName["new"].Declarers; got != 1 {
		t.Errorf("new declarers = %d, want 1", got)
	}
}

// insertDeclaredRegionsRow seeds one node_declared_regions answer.
func insertDeclaredRegionsRow(t *testing.T, s *Store, target, observedAt, regionsCSV string) {
	t.Helper()
	_, err := s.db.Exec(
		`INSERT INTO node_declared_regions (target, rx_pubkey, observed_at, ingested_at, regions_csv, truncated)
		 VALUES (?, 'rx', ?, ?, ?, 0)`,
		target, observedAt, observedAt, regionsCSV)
	if err != nil {
		t.Fatal(err)
	}
}
```

`newTestStore` is defined in `cmd/ingestor/main_test.go:121` and opens a real store via `OpenStore`, so the full schema — `node_declared_regions` included — already exists. Do not add a second helper.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/ingestor && go test ./... -run TestDeclaredRegionStats -v`
Expected: FAIL to compile — `store.DeclaredRegionStats undefined`

- [ ] **Step 3: Implement**

Add to `cmd/ingestor/db.go`, beside `CurrentDeclaredRegions`:

```go
// DeclaredRegionStats returns every region name currently declared anywhere on
// the network, with the two facts the derived-tier cap ranks on: how many
// distinct repeaters declare it, and the most recent observed_at among them.
//
// Only the LATEST answer per target counts — the same rule
// CurrentDeclaredRegions follows, by observed_at and never ingested_at, so a
// drive buffered offline cannot resurrect a region a repeater has since
// dropped. The window function is covered by idx_ndr_target(target,
// observed_at).
//
// CSV splitting and aggregation happen in Go rather than SQL: regions_csv is
// written with strings.Join, and unpicking it in SQLite would need a recursive
// CTE for no gain at this row count (~200 targets).
func (s *Store) DeclaredRegionStats() ([]declaredRegionStat, error) {
	rows, err := s.db.Query(`
		WITH ranked AS (
			SELECT target, observed_at, regions_csv,
				ROW_NUMBER() OVER (PARTITION BY target ORDER BY observed_at DESC) AS rn
			FROM node_declared_regions
		)
		SELECT target, observed_at, regions_csv FROM ranked WHERE rn = 1
	`)
	if err != nil {
		return nil, fmt.Errorf("declared region stats: %w", err)
	}
	defer rows.Close()

	agg := map[string]*declaredRegionStat{}
	for rows.Next() {
		var target, observedAt, csv string
		if err := rows.Scan(&target, &observedAt, &csv); err != nil {
			return nil, fmt.Errorf("declared region stats scan: %w", err)
		}
		seenHere := map[string]bool{} // one target counts once per name
		for _, name := range splitDeclaredRegionsCSV(csv) {
			if name == "*" || seenHere[name] {
				continue // '*' is the wildcard, not a region name
			}
			seenHere[name] = true
			st, ok := agg[name]
			if !ok {
				st = &declaredRegionStat{Name: name}
				agg[name] = st
			}
			st.Declarers++
			if observedAt > st.LastSeen {
				st.LastSeen = observedAt
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("declared region stats rows: %w", err)
	}

	out := make([]declaredRegionStat, 0, len(agg))
	for _, st := range agg {
		out = append(out, *st)
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/ingestor && go test ./... -run TestDeclaredRegionStats -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/ingestor/db.go cmd/ingestor/region_keys_test.go
git commit -m "feat(ingestor): DeclaredRegionStats — declared names with declarer counts"
```

---

### Task 7: Wire the refresh into startup and a ticker

**Files:**
- Modify: `cmd/ingestor/region_keys.go` (a `refreshFromStore` helper so both startup and the ticker share one path)
- Modify: `cmd/ingestor/main.go`

- [ ] **Step 1: Add the shared refresh helper**

Append to `cmd/ingestor/region_keys.go`:

```go
// refreshFromStore reads the declared region names, ranks and caps them, and
// swaps in a new snapshot. Shared by startup and the ticker so both apply
// identical rules. A DB error is logged and the current snapshot is kept — a
// failed refresh must never empty the key set and silently unname all traffic.
func (s *regionKeySet) refreshFromStore(store *Store) {
	if !s.enabled {
		return
	}
	stats, err := store.DeclaredRegionStats()
	if err != nil {
		log.Printf("[regions] derived-key refresh failed, keeping %d existing key(s): %v", len(s.snapshot().all), err)
		return
	}
	ranked := rankDeclaredRegions(stats, s.max)
	added := s.refreshDerived(ranked)
	snap := s.snapshot()
	log.Printf("[regions] derived-key refresh: %d name(s) declared, %d kept after filter+cap(%d), %d total key(s) in force",
		len(stats), len(ranked), s.max, len(snap.all))
	if len(added) > 0 {
		log.Printf("[regions] derived keys now active: %v", added)
	}
	if len(stats) > s.max {
		log.Printf("[regions] NOTE: %d declared name(s) exceeded maxDerived=%d and were dropped, least-declared first", len(stats)-s.max, s.max)
	}
}
```

- [ ] **Step 2: Call it at startup**

In `cmd/ingestor/main.go`, replace the line added in Task 5:

```go
	regionSet := newRegionKeySet(cfg)
```

with:

```go
	regionSet := newRegionKeySet(cfg)
	if cfg.AutoRegionKeysEnabled() {
		regionSet.refreshFromStore(store)
	} else {
		log.Printf("[regions] autoRegionKeys disabled — only the %d configured hashRegions key(s) are in force", len(regionSet.snapshot().all))
	}
```

- [ ] **Step 3: Add the ticker**

In `cmd/ingestor/main.go`, after the existing client-RX retention ticker block, add:

```go
	// Derived region keys are refreshed on their own ticker rather than the
	// daily retention one: declared-region answers arrive continuously (a
	// companion app driving past a repeater), and waiting up to 24h to name a
	// newly-discovered region would defeat the point of deriving them at all.
	if cfg.AutoRegionKeysEnabled() {
		interval := time.Duration(cfg.AutoRegionKeysRefreshMinutes()) * time.Minute
		regionRefreshTicker := time.NewTicker(interval)
		go func() {
			for range regionRefreshTicker.C {
				regionSet.refreshFromStore(store)
				logScopeMatchCounters()
			}
		}()
		log.Printf("[regions] auto-derived region keys enabled: refreshing every %v, cap %d", interval, cfg.AutoRegionKeysMaxDerived())
	}
```

- [ ] **Step 4: Verify the disabled path changes nothing**

Append to `cmd/ingestor/region_keys_test.go`:

```go
func TestRefreshFromStoreIsNoOpWhenDisabled(t *testing.T) {
	store := newTestStore(t)
	insertDeclaredRegionsRow(t, store, "aa"+strings.Repeat("11", 31), "2026-09-07T00:00:00Z", "behss")

	cfg := &Config{HashRegions: []string{"#be"}} // autoRegionKeys absent
	set := newRegionKeySet(cfg)
	before := len(set.snapshot().all)
	set.refreshFromStore(store)

	if got := len(set.snapshot().all); got != before {
		t.Errorf("key count %d -> %d with autoRegionKeys off, want unchanged", before, got)
	}
	if _, ok := set.snapshot().all["#behss"]; ok {
		t.Error("a declared name became a key with the feature disabled — this is the safety property the default-off promise rests on")
	}
}
```

- [ ] **Step 5: Run the full suite**

Run: `cd cmd/ingestor && go build ./... && go test ./... && go test ./... -race`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add cmd/ingestor/region_keys.go cmd/ingestor/region_keys_test.go cmd/ingestor/main.go
git commit -m "feat(ingestor): refresh derived region keys at startup and on a ticker"
```

---

### Task 8: `scope-repair` must use the same key set

Without this, a repair run re-derives every automatically-named row against the explicit tier alone, finds no match, and writes `""` back over it. That is data loss, not a cosmetic gap.

**Files:**
- Modify: `cmd/ingestor/scope_repair.go` (`rederiveScope` ~line 67, `runScopeRepair` ~line 274)
- Test: `cmd/ingestor/scope_repair_test.go`

- [ ] **Step 1: Write the failing test**

Append to `cmd/ingestor/scope_repair_test.go`:

```go
// TestScopeRepairKeepsDerivedNames: a row named from a derived key must survive
// a repair run. If rederiveScope sees only the explicit tier it reports
// MatchCount 0, which lands in the "named -> unmatched" branch and wipes the
// name. This test is the guard against that.
func TestScopeRepairKeepsDerivedNames(t *testing.T) {
	payload := []byte{0x42, 0x43, 0x44}
	// A transport-flood packet: header 0x14 (route 0, payload type 5),
	// code1/code2, path byte 0x41 (hash_size 2, one hop), hop, then payload.
	code1 := codeFor("#behss", 5, payload)
	rawHex := "14" + code1 + "0000" + "41" + "E3D3" + strings.ToUpper(hex.EncodeToString(payload))

	cfg := &Config{AutoRegionKeys: &AutoRegionKeysConfig{Enabled: true}}
	set := newRegionKeySet(cfg)
	set.refreshDerived([]string{"behss"})

	got, err := rederiveScope(rawHex, set.snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Name != "#behss" {
		t.Errorf("State.Name = %q, want %q — a derived key must name the row during repair", got.State.Name, "#behss")
	}
	if got.MatchCount != 1 {
		t.Errorf("MatchCount = %d, want 1", got.MatchCount)
	}
}
```

Note: `codeFor` builds `code1` from the payload, and the raw hex embeds it, so the two sides cannot drift. Add `"encoding/hex"` and `"strings"` to the test file's imports if absent.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/ingestor && go test ./... -run TestScopeRepairKeepsDerivedNames -v`
Expected: FAIL to compile — `cannot use set.snapshot() (*regionKeySnapshot) as map[string][]byte`

- [ ] **Step 3: Change `rederiveScope` to take the snapshot**

In `cmd/ingestor/scope_repair.go`, change the signature:

```go
func rederiveScope(rawHex string, snap *regionKeySnapshot) (scopeDerivation, error) {
```

and the `matchingRegions` call inside it:

```go
	matched := matchingRegions(snap.all, byte(decoded.Header.PayloadType), decoded.payloadRaw, decoded.TransportCodes.Code1)
```

- [ ] **Step 4: Build the derived tier in `runScopeRepair`**

In `runScopeRepair`, replace:

```go
	regionKeys := loadRegionKeys(cfg)

	store, err := OpenStore(dbPath)
	if err != nil {
		log.Fatalf("scope-repair: db: %v", err)
	}
	defer store.Close()

	report, err := repairScopeNames(store.db, regionKeys, *apply)
```

with:

```go
	store, err := OpenStore(dbPath)
	if err != nil {
		log.Fatalf("scope-repair: db: %v", err)
	}
	defer store.Close()

	// The derived tier must be rebuilt before scanning. Repairing against the
	// explicit tier alone would find no key for any automatically-named row,
	// classify it as "named -> unmatched", and erase the name — turning a
	// maintenance tool into data loss.
	regionSet := newRegionKeySet(cfg)
	regionSet.refreshFromStore(store)
	snap := regionSet.snapshot()
	log.Printf("scope-repair: %d region key(s) in force", len(snap.all))

	report, err := repairScopeNames(store.db, snap, *apply)
```

- [ ] **Step 5: Update `repairScopeNames`**

Change its signature and the one call it makes:

```go
func repairScopeNames(db *sql.DB, snap *regionKeySnapshot, apply bool) (*scopeRepairReport, error) {
```

```go
		d, err := rederiveScope(rawHex, snap)
```

Update the other `rederiveScope` / `repairScopeNames` call sites in `scope_repair_test.go` to pass a snapshot built with `newRegionKeySet(&Config{HashRegions: ...}).snapshot()`.

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd cmd/ingestor && go test ./... -run TestScopeRepair -v` then `go test ./...`
Expected: PASS, including the pre-existing scope-repair tests.

- [ ] **Step 7: Commit**

```bash
git add cmd/ingestor/scope_repair.go cmd/ingestor/scope_repair_test.go
git commit -m "fix(scope-repair): repair against the full key set, not just hashRegions"
```

---

### Task 9: Benchmark the match path

AGENTS.md rule 0: perf claims need proof, and this grows the key set by up to `maxDerived`.

**Files:**
- Modify: `cmd/ingestor/region_keys_test.go`

- [ ] **Step 1: Write the benchmark**

```go
// BenchmarkScopeMatch sweeps key-set size because the cost is linear in it and
// cannot be reduced: code1 is an HMAC over the packet payload, so there is no
// payload-independent lookup key to index on. The sweep is the evidence for
// choosing maxDerived, not a single before/after number — the explicit tier
// size is operator config and varies per deployment.
func BenchmarkScopeMatch(b *testing.B) {
	payload := make([]byte, 51) // a typical GRP_TXT payload
	for i := range payload {
		payload[i] = byte(i)
	}
	for _, n := range []int{16, 58, 180, 314} {
		b.Run(fmt.Sprintf("keys=%d", n), func(b *testing.B) {
			all := make(map[string][]byte, n)
			explicit := make(map[string]bool, n)
			for i := 0; i < n; i++ {
				name := fmt.Sprintf("#r%04d", i)
				sum := sha256.Sum256([]byte(name))
				all[name] = sum[:16]
				explicit[name] = true
			}
			snap := &regionKeySnapshot{all: all, explicit: explicit}
			code := codeFor("#r0000", 5, payload)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = snap.match(5, payload, code)
			}
		})
	}
}
```

Add `"fmt"` to the test file's imports if absent.

- [ ] **Step 2: Run it and record the numbers**

Run: `cd cmd/ingestor && go test -bench BenchmarkScopeMatch -benchtime=200x -run '^$' ./...`
Expected: four lines, roughly linear in key count. Paste the actual output into the commit message — an unrecorded benchmark is not proof.

- [ ] **Step 3: Sanity-check against real load**

Divide the `keys=314` ns/op by the observed transport-scoped packet rate. On the reference deployment that rate is ~0.04/s (22126 packets over 7 days), so even a 300µs match is ~0.001% of one core. If your measured figure implies more than 5% of a core at your own packet rate, stop and reconsider `maxDerived` before shipping.

- [ ] **Step 4: Commit**

```bash
git add cmd/ingestor/region_keys_test.go
git commit -m "test(ingestor): benchmark scope matching across key-set sizes"
```

---

### Task 10: Document it

**Files:**
- Modify: `config.example.json`
- Modify: `docs/client-regions.md`

- [ ] **Step 1: Add the config block**

In `config.example.json`, immediately after the `_comment_hashRegions` line, add:

```json
  "autoRegionKeys": { "enabled": false, "maxDerived": 256, "refreshMinutes": 15 },
  "_comment_autoRegionKeys": "Opt-in: derive region keys from the region names repeaters declare over RF (node_declared_regions), on top of the explicit hashRegions list above. Default OFF. Solves the case where a repeater forwards a region this instance holds no key for: its traffic is stored unmatched and the Scope Audit reports the region as 'not observed', which reads as a finding about the repeater rather than a gap in this config. TOP-LEVEL FLAG, a sibling of hashRegions — config loading is plain json.Unmarshal with no DisallowUnknownFields, so nesting it elsewhere is silently ignored. maxDerived caps the derived tier (default 256): each key costs one HMAC per transport-scoped packet and raises the random 2-byte collision rate by 1/65536, and the match cannot be indexed because the code is an HMAC over the payload. Over the cap, names are kept by how many distinct repeaters declare them. Requires clientRegions (or an ESP32 observer on the neighbour-report firmware) to be populating node_declared_regions, or the derived tier stays empty."
```

- [ ] **Step 2: Document the second consumer**

In `docs/client-regions.md`, insert this section between `## Storage — node_declared_regions (ingestor-owned)` (line 98) and `## Configurable values (future customizer)` (line 122):

```markdown
## Second consumer — derived region keys

`node_declared_regions` originally had one reader: the declared side of the
Scope Audit. With `autoRegionKeys.enabled` set (default off, see
`config.example.json`), the ingestor reads it a second time, deriving a region
key `SHA256("#name")[:16]` for each declared name so that traffic in those
regions can be *named* rather than stored unmatched.

Two consequences operators should know about:

- **Retention now bounds nameability.** `retention.clientRegionsDays` already
  bounded how long a declared answer stayed visible in the audit. With
  derivation on, it also bounds how long a region stays *derivable*: once the
  last answer naming a region is pruned, its key leaves the set on the next
  refresh and its traffic reverts to unmatched. Regions you want named
  permanently belong in `hashRegions`, which nothing prunes.
- **The set is capped.** `autoRegionKeys.maxDerived` (default 256) limits the
  derived tier; over the cap, names are kept by how many distinct repeaters
  declare them. A region declared by a single repeater is the first to be
  dropped. The ingestor logs how many names were dropped on each refresh.
```

- [ ] **Step 3: Verify the example config still parses**

Run: `python -c "import json; json.load(open('config.example.json')); print('valid')"`
Expected: `valid`

- [ ] **Step 4: Commit**

```bash
git add config.example.json docs/client-regions.md
git commit -m "docs(config): document the opt-in autoRegionKeys block"
```

---

### Task 11: Verify end to end

- [ ] **Step 1: Full suites, both binaries**

Run: `cd cmd/ingestor && go test ./... -race` then `cd ../server && go test ./...`
Expected: PASS in both.

- [ ] **Step 2: Frontend suite**

Run: `node test-packet-filter.js && node test-aging.js && node test-frontend-helpers.js`
Expected: PASS. Nothing in this plan touches the frontend; run it to prove that.

- [ ] **Step 3: Prove the default-off promise on real data**

Run the ingestor against a copy of a real database with no `autoRegionKeys` block. Confirm the startup log reads `autoRegionKeys disabled — only the N configured hashRegions key(s) are in force` and that no `[regions] derived` line appears.

- [ ] **Step 4: Prove the feature on real data**

Enable the block, restart, and confirm the startup log reports the declared/kept/total counts. Then check that a packet which was previously stored unmatched is now named: pick one from `SELECT id, raw_hex FROM transmissions WHERE scope_name = '' LIMIT 5`, run `scope-repair` as a dry run, and confirm the report's "newly named" section lists the expected regions.

- [ ] **Step 5: Record the ambiguity rate**

After the ingestor has run for at least a day with the feature on, read the `[regions] scope matches:` line. The `ambiguous` count against the total is the measurement that decides whether M3 (path-evidence tie-break) is worth building. Write the number into `docs/specs/2026-09-07-auto-region-keys-design.md` under M3, replacing the estimate with the observation.

---

## Notes for the implementer

- **`matchingRegions` stays exactly as it is.** It is the shared primitive; only its caller changes. Its `#1609` ambiguity semantics are load-bearing for both `match` and `scope-repair`.
- **Do not lowercase region names anywhere.** The key is `SHA256("#name")[:16]` over raw bytes, so `#BEHSS` and `#behss` are different regions. `loadRegionKeys` does not fold case and neither may the derived path.
- **Do not let a failed refresh empty the key set.** `refreshFromStore` keeps the current snapshot on error. An empty key set would silently unname all traffic, which looks exactly like the bug this feature fixes.
- **The `null` region name will be derived.** That is intended: see `regionNameAcceptable`'s comment. It costs one slot.
