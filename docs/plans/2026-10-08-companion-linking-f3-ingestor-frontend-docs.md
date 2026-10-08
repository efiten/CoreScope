# Companion Linking F3 (ingestor, frontend, docs) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Finish sub-project F on top of F1 (store) and F2 (server): the ingestor's opt-in linked-only filter (`clientRxCoverage.requireLinkedCompanion`), the account page's device rows and Companions section, the coverage page's "My coverage" toggle, an e2e test, and the operator/user docs. The Dutch release note is drafted in Task 9 but only added and published once the version is live.

**Architecture:** The ingestor reads `companion_links` from `users.db` read-only with raw SQL, exactly like the approved hashtag channels (`approved_channels.go`): a lazily opened `mode=ro` handle, an atomically swapped snapshot, and a failed read that keeps the last good set. The new `linkedCompanionSet` adds a miss path: a pubkey not in the snapshot re-reads `users.db` at most once per 5 s before it is dropped, so a companion linked a moment ago is accepted within seconds while a flood of unknown pubkeys costs one read per 5 s. The set hangs off `Store` (`store.linkedCompanions`, nil = off), so `handleMessage`'s signature and its 92 test call sites do not change; the check sits right after the client-topic blacklist check, before the `packets`/`rf`/`regions` switch. Drops are an atomic counter in `DBStats`, published in the stats file as `client_unlinked_dropped`. The frontend changes follow the existing patterns: `account.js` string-built HTML through `escapeHtml`, `CSAuth.request`, `msgBox`/`CSAuth.say`, and a `load*` function per list; `rx-coverage.js` gains three small pure helpers exported on `window.CSRxCoverage._test`.

**Tech Stack:** Go (`cmd/ingestor` module, `github.com/mattn/go-sqlite3`, stdlib `testing`), vanilla JS (`public/account.js`, `public/rx-coverage.js`), Node `vm`-sandbox unit tests (`tests/unit/*.js`, run by `test-all.sh`), Playwright (`tests/e2e/test-user-management-e2e.js`), Markdown docs.

**Spec:** `docs/specs/2026-10-08-companion-linking-design.md`, sections *Linked-only ingest* (ingestor part), *Account page*, *Coverage attribution* (toggle), *CORS* (docs), *Testing → cmd/ingestor* and *e2e*, *Rollout*. Server API: the "API for F3" table at the top of `docs/plans/2026-10-08-companion-linking-f2-server.md`. Store API: `docs/plans/2026-10-08-companion-linking-f1-store.md`.

---

## What F3 relies on (from F1/F2, do not change here)

| Item | Source |
|---|---|
| `companion_links (pubkey TEXT PRIMARY KEY, user_id, name, linked_at)` in `users.db`, pubkeys 64 lowercase hex | F1 Task 1 (schema v6) |
| `GET /api/account/sessions` items carry `kind` (`"web"`/`"device"`) and `label` | F2 Task 2 |
| `GET /api/account/companions` → `[{pubkey, name, linkedAt, lastSeenAt}]`, `lastSeenAt` may be `null` | F2 Task 7 |
| `DELETE /api/account/companions/{pubkey}` → `204`, `404` when not linked to the caller | F2 Task 7 |
| `POST /api/auth/device-token`, `POST /api/account/companions/challenge`, `POST /api/account/companions` (bearer), signed message `"corescope-link:" + <host of publicBaseUrl> + ":" + challenge` | F2 Tasks 3, 4, 7 |
| `GET /api/rx-coverage?mine=1` (cookie session; `401` without one) | F2 Task 10 |
| `config.example.json` comment for `clientRxCoverage.requireLinkedCompanion` | F2 Task 5 |

## Design constraints the implementer must not relax

- **The ingestor never imports `internal/users` and never writes `users.db`** (AGENTS.md). It opens `users.db` with `usersDBReadOnlyDSN` (`mode=ro`) and runs one raw query, `SELECT pubkey FROM companion_links`. `TestLinkedCompanionSetIsReadOnly` pins this.
- **The filter applies before every client handler** (`packets`, `rf`, `regions`), after the existing client-topic blacklist check, whatever `clientRxCoverage.enabled` says.
- **Drops are counted, never logged per message.** The only log lines are startup, a missing table (once), and a failing/recovered read (once per streak).
- **Until the first successful read of `companion_links`, all client data passes.** A missing table (older `users.db`) or an unreadable `users.db` warns once and never crashes. After the first success, a failed read keeps the last good set.
- **The setting is ignored with a startup warning when `userManagement.enabled` is off.** The filter is then not installed at all.
- **It is a filter, not a security boundary.** Every doc that mentions it says so, and says to upgrade the RX clients first.
- The account page and coverage page never show another user's companions; they only call the caller's own `/api/account/*` routes and `?mine=1`.

## File Structure

| File | Change |
|---|---|
| `cmd/ingestor/config.go` | `ClientRxCoverageConfig.RequireLinkedCompanion`; `RequireLinkedCompanionSet`, `RequireLinkedCompanion`. |
| `cmd/ingestor/linked_companions_config_test.go` | New: config parsing test. |
| `cmd/ingestor/linked_companions.go` | New: `linkedCompanionSet` (read-only reader, miss re-read cap, missing-table handling), `allowClientPubkey`, `newLinkedCompanionFilter`, `refreshLoop`. |
| `cmd/ingestor/linked_companions_test.go` | New: set tests (linked/unlinked, miss refresh, 5 s cap under flood, unlink after refresh, missing table, missing file, read-only). |
| `cmd/ingestor/linked_companions_ingest_test.go` | New: `handleMessage` with the filter off/on, all three sub-topics, stats file. |
| `cmd/ingestor/linked_companions_startup_test.go` | New: startup (off, ignored with warning, on). |
| `cmd/ingestor/db.go` | `DBStats.ClientUnlinkedDropped`; `Store.linkedCompanions`. |
| `cmd/ingestor/stats_file.go` | `IngestorStatsSnapshot.ClientUnlinkedDropped` (`client_unlinked_dropped`). |
| `cmd/ingestor/main.go` | Filter check in `handleMessage`; startup wiring. |
| `AGENTS.md` | The users.db read-only rule names `companion_links` too. |
| `public/account.js` | Device rows ("CoreDrive RX – label" + marker); Companions section (`companionsHtml`, `loadCompanions`, Unlink). |
| `public/account.css` | `.um-chip-device`, `.account-empty`. |
| `tests/unit/test-user-management-ui.js` | Device-row and Companions tests. |
| `public/rx-coverage.js` | "My coverage" toggle; `coverageUrl`, `mineBtnHtml`, `authUser`; `window.CSRxCoverage._test`. |
| `tests/unit/test-rx-coverage-mine.js` | New. |
| `test-all.sh` | Runs the new unit test. |
| `tests/e2e/test-user-management-e2e.js` | One step: seeded device token under Devices, linked companion under Companions, unlink. |
| `docs/client-rx-coverage.md` | Section "Linked companions only (optional)". |
| `docs/user-guide/accounts.md` | Operator section "Companion linking (CoreDrive RX)" (device tokens, CORS, linked-only); user bullet "Companions" and "My coverage". |
| `config.example.json` | `_comment_corsAllowedOrigins` names the bearer-route exception. |

`cmd/ingestor` commands run from `C:\dev\corescope\CoreScope\cmd\ingestor`; Node commands and git commands from the repo root `C:\dev\corescope\CoreScope`. After every Go code step run `gofmt -w` on the touched Go files (struct fields and literals realign when a longer field is added).

---

### Task 1: Ingestor config `clientRxCoverage.requireLinkedCompanion`

**Files:**
- Create: `cmd/ingestor/linked_companions_config_test.go`
- Modify: `cmd/ingestor/config.go`

- [ ] **Step 1: Write the failing test**

Create `cmd/ingestor/linked_companions_config_test.go`:

```go
package main

import (
	"encoding/json"
	"testing"
)

func TestRequireLinkedCompanionConfig(t *testing.T) {
	cases := []struct {
		js      string
		set, on bool
	}{
		{`{}`, false, false},
		{`{"clientRxCoverage": {"enabled": true}}`, false, false},
		{`{"clientRxCoverage": {"requireLinkedCompanion": false}, "userManagement": {"enabled": true}}`, false, false},
		{`{"clientRxCoverage": {"requireLinkedCompanion": true}}`, true, false},
		{`{"clientRxCoverage": {"requireLinkedCompanion": true}, "userManagement": {"enabled": false}}`, true, false},
		{`{"clientRxCoverage": {"requireLinkedCompanion": true}, "userManagement": {"enabled": true}}`, true, true},
	}
	for _, c := range cases {
		var cfg Config
		if err := json.Unmarshal([]byte(c.js), &cfg); err != nil {
			t.Fatalf("%s: %v", c.js, err)
		}
		if got := cfg.RequireLinkedCompanionSet(); got != c.set {
			t.Errorf("%s: RequireLinkedCompanionSet = %v, want %v", c.js, got, c.set)
		}
		if got := cfg.RequireLinkedCompanion(); got != c.on {
			t.Errorf("%s: RequireLinkedCompanion = %v, want %v", c.js, got, c.on)
		}
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd cmd/ingestor && go test -run TestRequireLinkedCompanionConfig .`
Expected: FAIL, build error `cfg.RequireLinkedCompanionSet undefined (type Config has no field or method RequireLinkedCompanionSet)`.

- [ ] **Step 3: Implement**

In `cmd/ingestor/config.go`, replace

```go
// ClientRxCoverageConfig controls the opt-in mobile client-RX coverage feature.
type ClientRxCoverageConfig struct {
	Enabled bool `json:"enabled"`
}
```

with

```go
// ClientRxCoverageConfig controls the opt-in mobile client-RX coverage feature.
type ClientRxCoverageConfig struct {
	Enabled bool `json:"enabled"`
	// RequireLinkedCompanion drops every meshcore/client/<pubkey>/...
	// message whose companion no user linked (linked_companions.go). In
	// effect only with userManagement.enabled.
	RequireLinkedCompanion bool `json:"requireLinkedCompanion,omitempty"`
}

// RequireLinkedCompanionSet reports whether
// clientRxCoverage.requireLinkedCompanion is set, whatever userManagement
// says (startup uses it to warn when the setting is ignored).
func (c *Config) RequireLinkedCompanionSet() bool {
	return c.ClientRxCoverage != nil && c.ClientRxCoverage.RequireLinkedCompanion
}

// RequireLinkedCompanion reports whether the linked-only filter is in
// effect: the setting and userManagement.enabled both on.
func (c *Config) RequireLinkedCompanion() bool {
	return c.RequireLinkedCompanionSet() && c.UserManagement != nil && c.UserManagement.Enabled
}
```

Run `gofmt -w config.go`.

- [ ] **Step 4: Run the test**

Run: `cd cmd/ingestor && go test -run 'TestRequireLinkedCompanionConfig|TestApprovedChannelsConfig|TestClientRxCoverageEnabledDefault' .`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/ingestor/config.go cmd/ingestor/linked_companions_config_test.go
git commit -F - <<'EOF'
feat(ingestor): read clientRxCoverage.requireLinkedCompanion

The ingestor's view of the setting, in effect only together with
userManagement.enabled, like on the server.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 2: The linked-companion set (read-only, refresh, miss re-read cap)

**Files:**
- Create: `cmd/ingestor/linked_companions.go`, `cmd/ingestor/linked_companions_test.go`

- [ ] **Step 1: Write the failing tests**

Create `cmd/ingestor/linked_companions_test.go`:

```go
package main

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The columns of companion_links in cmd/server's users.db (internal/users
// schema v6); the ingestor reads pubkey only.
const testCompanionLinksDDL = `CREATE TABLE IF NOT EXISTS companion_links (
	pubkey TEXT PRIMARY KEY,
	user_id INTEGER NOT NULL,
	name TEXT NOT NULL DEFAULT '',
	linked_at INTEGER NOT NULL
)`

// testUnlinkedPK is never written to companion_links.
const testUnlinkedPK = "b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3"

// linkClock is a settable clock for the 5 s miss gap.
type linkClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *linkClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *linkClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// setLinks replaces the companion_links rows of the users.db at path with pks
// (creating the file and table when needed), as the server would.
func setLinks(t *testing.T, path string, pks ...string) {
	t.Helper()
	stmts := []string{testCompanionLinksDDL, `DELETE FROM companion_links`}
	for i, pk := range pks {
		stmts = append(stmts, fmt.Sprintf(`INSERT INTO companion_links (pubkey, user_id, linked_at) VALUES ('%s', 1, %d)`, pk, i))
	}
	execUsersDB(t, path, stmts...)
}

// newTestLinkedSet returns a set over a fresh users.db holding pks, on a
// frozen clock. It is not read yet.
func newTestLinkedSet(t *testing.T, pks ...string) (*linkedCompanionSet, string, *linkClock) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "users.db")
	setLinks(t, path, pks...)
	clk := &linkClock{t: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
	s := newLinkedCompanionSet(path)
	s.now = clk.now
	t.Cleanup(s.Close)
	return s, path, clk
}

func TestLinkedCompanionsTableIsInUsersSchema(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "internal", "users", "schema.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "CREATE TABLE companion_links (") {
		t.Fatalf("internal/users/schema.go no longer creates companion_links; %q reads it", linkedCompanionsQuery)
	}
}

func TestLinkedCompanionSetAllowsLinkedOnly(t *testing.T) {
	s, _, _ := newTestLinkedSet(t, testCompanionPK)
	s.refresh()
	if !s.Allow(testCompanionPK) || !s.Allow(strings.ToUpper(testCompanionPK)) {
		t.Fatal("linked pubkey refused")
	}
	if s.Allow(testUnlinkedPK) {
		t.Fatal("unlinked pubkey allowed")
	}
}

func TestLinkedCompanionSetMissRefreshAcceptsNewLink(t *testing.T) {
	s, path, clk := newTestLinkedSet(t)
	s.refresh()
	setLinks(t, path, testCompanionPK) // linked after the last refresh
	clk.add(linkedCompanionsMissGap - time.Second)
	if s.Allow(testCompanionPK) {
		t.Fatal("re-read inside the 5 s gap")
	}
	clk.add(time.Second)
	if !s.Allow(testCompanionPK) {
		t.Fatal("a miss after the gap did not pick up the new link")
	}
}

func TestLinkedCompanionSetMissRefreshCapHoldsUnderFlood(t *testing.T) {
	s, _, clk := newTestLinkedSet(t, testCompanionPK)
	s.refresh()
	base := s.readCount()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 250; i++ {
				s.Allow(fmt.Sprintf("%02x%062x", g, i))
			}
		}(g)
	}
	wg.Wait()
	if n := s.readCount() - base; n != 0 {
		t.Fatalf("%d re-reads inside the gap; want 0", n)
	}
	clk.add(linkedCompanionsMissGap)
	for i := 0; i < 1000; i++ {
		s.Allow(fmt.Sprintf("ff%062x", i))
	}
	if n := s.readCount() - base; n != 1 {
		t.Fatalf("%d re-reads for a flood after the gap; want 1", n)
	}
	if !s.Allow(testCompanionPK) {
		t.Fatal("the flood dropped a linked pubkey")
	}
}

func TestLinkedCompanionSetUnlinkHonouredAfterPeriodicRefresh(t *testing.T) {
	s, path, clk := newTestLinkedSet(t, testCompanionPK)
	s.refresh()
	setLinks(t, path) // unlinked on the server
	clk.add(30 * time.Second)
	if !s.Allow(testCompanionPK) {
		t.Fatal("a hit must not re-read; the unlink waits for the periodic refresh")
	}
	s.refresh()
	if s.Allow(testCompanionPK) {
		t.Fatal("unlink not honoured after the periodic refresh")
	}
}

func TestLinkedCompanionSetMissingTableIsEmptyAndWarnsOnce(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	path := filepath.Join(t.TempDir(), "users.db")
	writeUsersDB(t, path) // an older users.db: proposals, no companion_links
	s := newLinkedCompanionSet(path)
	defer s.Close()
	s.refresh()
	s.refresh()
	if !s.Allow(testCompanionPK) {
		t.Fatal("dropped before the list was ever read; want pass-through")
	}
	if n := strings.Count(buf.String(), "no companion_links table"); n != 1 {
		t.Fatalf("warned %d times; want once:\n%s", n, buf.String())
	}
	setLinks(t, path, testCompanionPK) // the server was upgraded
	s.refresh()
	if !s.Allow(testCompanionPK) {
		t.Fatal("the table appearing later is not picked up")
	}
	if s.Allow(testUnlinkedPK) {
		t.Fatal("an unlinked pubkey passed once the list was read")
	}
}

func TestLinkedCompanionSetMissingFileLogsOnceAndRecovers(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	path := filepath.Join(t.TempDir(), "users.db")
	s := newLinkedCompanionSet(path)
	defer s.Close()
	s.refresh()
	s.refresh()
	if !s.Allow(testCompanionPK) {
		t.Fatal("dropped before users.db was ever read; want pass-through")
	}
	if n := strings.Count(buf.String(), "linked companions unavailable"); n != 1 {
		t.Fatalf("logged %d times; want once:\n%s", n, buf.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the ingestor created users.db")
	}
	setLinks(t, path, testCompanionPK)
	s.refresh()
	if !s.Allow(testCompanionPK) {
		t.Fatal("users.db appearing later is not picked up")
	}
	if !strings.Contains(buf.String(), "linked companions readable again") {
		t.Fatalf("no recovery line:\n%s", buf.String())
	}
}

// Pass-through holds only until the first successful read: after that a
// failed read keeps the last good set, so it fails closed.
func TestLinkedCompanionSetFailsClosedOnlyAfterFirstRead(t *testing.T) {
	s, path, clock := newTestLinkedSet(t, testCompanionPK)
	s.refresh()
	if s.Allow(testUnlinkedPK) {
		t.Fatal("an unlinked pubkey passed after a successful read")
	}
	s.Close()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	clock.add(linkedCompanionsMissGap)
	s.refresh() // fails: users.db is gone
	if !s.Allow(testCompanionPK) {
		t.Fatal("the last good set was dropped on a failed read")
	}
	clock.add(linkedCompanionsMissGap)
	if s.Allow(testUnlinkedPK) {
		t.Fatal("a failed read after the first success let an unlinked pubkey through")
	}
}

func TestLinkedCompanionSetIsReadOnly(t *testing.T) {
	s, path, _ := newTestLinkedSet(t, testCompanionPK)
	s.refresh()
	_, err := s.db.Exec(`INSERT INTO companion_links (pubkey, user_id, linked_at) VALUES ('x', 1, 1)`)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "readonly") {
		t.Fatalf("write through the ingestor's users.db handle: err = %v; want a read-only refusal", err)
	}
	if !strings.Contains(usersDBReadOnlyDSN(path), "mode=ro") {
		t.Fatal("DSN without mode=ro")
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd cmd/ingestor && go test -run 'TestLinkedCompanion' .`
Expected: FAIL, build error `undefined: linkedCompanionSet`, `undefined: newLinkedCompanionSet`, `undefined: linkedCompanionsQuery`, `undefined: linkedCompanionsMissGap`.

- [ ] **Step 3: Implement**

Create `cmd/ingestor/linked_companions.go`:

```go
package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Linked-only ingest (docs/specs/2026-10-08-companion-linking-design.md,
// "Linked-only ingest"). With clientRxCoverage.requireLinkedCompanion and
// userManagement.enabled, every meshcore/client/<pubkey>/... message whose
// pubkey no user linked is dropped before any client handler runs. The set
// comes from users.db, read-only, with raw SQL (AGENTS.md: the ingestor never
// imports internal/users and never writes users.db). It is a filter for
// honest clients, not a security boundary: all RX clients share one broker
// account.

const (
	// linkedCompanionsRefresh is the periodic re-read; it picks up unlinks.
	linkedCompanionsRefresh = time.Minute
	// linkedCompanionsMissGap caps the re-reads a miss may trigger.
	linkedCompanionsMissGap = 5 * time.Second
)

// linkedCompanionsQuery reads internal/users schema v6 (companion_links);
// TestLinkedCompanionsTableIsInUsersSchema checks the table still exists there.
const linkedCompanionsQuery = `SELECT pubkey FROM companion_links`

// linkedCompanionSet hands each client message the current set of linked
// pubkeys. Snapshots are never mutated after they are stored, so a hit is
// lock-free. Reads (refresh, or a miss after the gap) are serialized by mu.
type linkedCompanionSet struct {
	path string
	now  func() time.Time
	cur  atomic.Pointer[map[string]struct{}]

	mu       sync.Mutex // guards everything below
	db       *sql.DB    // opened lazily, read-only
	lastRead time.Time  // last read attempt, for the miss gap
	reads    int        // read attempts (tests)
	failing  bool       // the last read failed; logged once per failure streak
	loaded   atomic.Bool // a read of companion_links has succeeded at least once
	noTable  bool       // warned that companion_links is missing
}

// newLinkedCompanionSet starts unloaded: until the first successful read of
// companion_links, every pubkey passes. A users.db that is missing, unreadable
// or older than companion linking must not cost the data of linked companions.
func newLinkedCompanionSet(usersDBPath string) *linkedCompanionSet {
	s := &linkedCompanionSet{path: usersDBPath, now: time.Now}
	empty := map[string]struct{}{}
	s.cur.Store(&empty)
	return s
}

// Allow reports whether pubkey (the topic segment, any case) is linked. A
// miss re-reads users.db when the last read is at least
// linkedCompanionsMissGap old, so a companion linked a moment ago is accepted
// within seconds while a flood of unknown pubkeys costs one read per gap.
func (s *linkedCompanionSet) Allow(pubkey string) bool {
	pk := strings.ToLower(strings.TrimSpace(pubkey))
	if _, ok := (*s.cur.Load())[pk]; ok {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := (*s.cur.Load())[pk]; ok { // another miss re-read meanwhile
		return true
	}
	if s.now().Sub(s.lastRead) < linkedCompanionsMissGap {
		return !s.loaded.Load()
	}
	s.readLocked()
	if !s.loaded.Load() {
		return true
	}
	_, ok := (*s.cur.Load())[pk]
	return ok
}

// refresh re-reads the set: at startup and from refreshLoop.
func (s *linkedCompanionSet) refresh() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readLocked()
}

func (s *linkedCompanionSet) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// readLocked swaps in a fresh snapshot and marks the set loaded. A missing
// companion_links table (a server older than companion linking) is warned once
// and leaves the set unloaded, so data still passes. Any other failure keeps
// the current snapshot (or pass-through, before the first success) and is
// logged once per streak.
func (s *linkedCompanionSet) readLocked() {
	s.lastRead = s.now()
	s.reads++
	keys, err := s.query()
	if err != nil && strings.Contains(err.Error(), "no such table") {
		if !s.noTable {
			log.Printf("[companions] %s has no companion_links table (a server older than companion linking); passing client data until it appears", s.path)
			s.noTable = true
		}
		return
	}
	if err != nil {
		if !s.failing {
			log.Printf("[companions] linked companions unavailable (%v); keeping the %d in force", err, len(*s.cur.Load()))
			s.failing = true
		}
		return
	}
	if s.failing {
		log.Printf("[companions] linked companions readable again from %s", s.path)
		s.failing = false
	}
	next := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		next[strings.ToLower(strings.TrimSpace(k))] = struct{}{}
	}
	s.cur.Store(&next)
	s.loaded.Store(true)
}

func (s *linkedCompanionSet) query() ([]string, error) {
	if s.db == nil {
		if _, err := os.Stat(s.path); err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("%w at %s", errUsersDBMissing, s.path)
			}
			return nil, err
		}
		db, err := sql.Open("sqlite3", usersDBReadOnlyDSN(s.path))
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(1)
		s.db = db
	}
	rows, err := s.db.Query(linkedCompanionsQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var pk string
		if err := rows.Scan(&pk); err != nil {
			return nil, err
		}
		out = append(out, pk)
	}
	return out, rows.Err()
}

// Close closes the read-only handle, if any.
func (s *linkedCompanionSet) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		s.db.Close()
		s.db = nil
	}
}
```

Run `gofmt -w linked_companions.go`.

- [ ] **Step 4: Run the tests**

Run: `cd cmd/ingestor && go test -race -run 'TestLinkedCompanion|TestChannelKeySet' .`
Expected: PASS (no race reports).

- [ ] **Step 5: Commit**

```bash
git add cmd/ingestor/linked_companions.go cmd/ingestor/linked_companions_test.go
git commit -F - <<'EOF'
feat(ingestor): read linked companions from users.db

A read-only set of companion_links pubkeys, refreshed on demand. A miss
re-reads at most once per 5 s, so a fresh link is accepted within
seconds and a flood of unknown pubkeys costs one read per 5 s. Until the
first successful read every pubkey passes (a missing table warns once);
after it, a failed read keeps the last good set.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 3: Drop unlinked client messages before every handler, and count them

**Files:**
- Create: `cmd/ingestor/linked_companions_ingest_test.go`
- Modify: `cmd/ingestor/linked_companions.go`, `cmd/ingestor/db.go`, `cmd/ingestor/stats_file.go`, `cmd/ingestor/main.go`

- [ ] **Step 1: Write the failing tests**

Create `cmd/ingestor/linked_companions_ingest_test.go`:

```go
package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// allClientFeatures turns on every client sub-topic and the linked-only
// setting; whether the filter runs depends only on store.linkedCompanions.
func allClientFeatures() *Config {
	return &Config{
		ClientRxCoverage: &ClientRxCoverageConfig{Enabled: true, RequireLinkedCompanion: true},
		ClientRfSamples:  &ClientRfSamplesConfig{Enabled: true},
		ClientRegions:    &ClientRegionsConfig{Enabled: true},
		UserManagement:   &UserManagementConfig{Enabled: true},
	}
}

// linkedFilterStore is a test store with the filter installed over pks.
func linkedFilterStore(t *testing.T, pks ...string) *Store {
	t.Helper()
	store := newTestStore(t)
	s, _, _ := newTestLinkedSet(t, pks...)
	s.refresh()
	store.linkedCompanions = s
	return store
}

func clientTopicMsg(pk, sub, payload string) *mockMessage {
	return &mockMessage{topic: "meshcore/client/" + pk + "/" + sub, payload: []byte(payload)}
}

func rfSampleCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM client_rf_samples`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestLinkedOnlyFilterOffChangesNothing(t *testing.T) {
	store := newTestStore(t) // no filter installed
	handleMessage(store, "test", MQTTSource{Name: "test"}, clientCoverageMsg(), nil, nil, allClientFeatures())
	if n := clientReceptionCount(t, store); n != 1 {
		t.Fatalf("filter off: %d client_receptions rows, want 1", n)
	}
	if d := store.Stats.ClientUnlinkedDropped.Load(); d != 0 {
		t.Fatalf("filter off: %d drops counted", d)
	}
}

func TestLinkedOnlyFilterPassesLinked(t *testing.T) {
	store := linkedFilterStore(t, testCompanionPK)
	handleMessage(store, "test", MQTTSource{Name: "test"}, clientCoverageMsg(), nil, nil, allClientFeatures())
	if n := clientReceptionCount(t, store); n != 1 {
		t.Fatalf("linked: %d client_receptions rows, want 1", n)
	}
	if d := store.Stats.ClientUnlinkedDropped.Load(); d != 0 {
		t.Fatalf("linked: %d drops counted", d)
	}
}

func TestLinkedOnlyFilterDropsUnlinkedBeforeEveryHandler(t *testing.T) {
	store := linkedFilterStore(t) // nothing linked
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	cfg := allClientFeatures()
	src := MQTTSource{Name: "test"}
	rf := `{"type":"RF_SAMPLE","timestamp":"` + rfFixtureTime(0) + `","gps":{"lat":51.2,"lon":4.4,"acc_m":8},"stationary":false,"uptime_secs":84213,"noise_floor":-119,"rx_air_secs":20877}`

	handleMessage(store, "test", src, clientCoverageMsg(), nil, nil, cfg)
	handleMessage(store, "test", src, clientTopicMsg(testCompanionPK, "rf", rf), nil, nil, cfg)
	handleMessage(store, "test", src, clientTopicMsg(testCompanionPK, "regions", `{}`), nil, nil, cfg)

	if n := clientReceptionCount(t, store); n != 0 {
		t.Fatalf("unlinked packets wrote %d client_receptions rows", n)
	}
	if n := rfSampleCount(t, store); n != 0 {
		t.Fatalf("unlinked rf wrote %d client_rf_samples rows", n)
	}
	if d := store.Stats.ClientUnlinkedDropped.Load(); d != 3 {
		t.Fatalf("%d drops counted, want 3 (packets, rf, regions)", d)
	}
	if buf.Len() != 0 {
		t.Fatalf("a drop was logged per message:\n%s", buf.String())
	}
}

func TestStatsFileCarriesClientUnlinkedDropped(t *testing.T) {
	statsPath := filepath.Join(t.TempDir(), "ingestor-stats.json")
	t.Setenv("CORESCOPE_INGESTOR_STATS", statsPath)
	store := newTestStore(t)
	store.Stats.ClientUnlinkedDropped.Add(3)
	stop := StartStatsFileWriter(store, 20*time.Millisecond)
	defer stop()
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, err := os.ReadFile(statsPath)
		if err == nil && strings.Contains(string(b), `"client_unlinked_dropped":3`) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("stats file never showed client_unlinked_dropped=3: %s (err %v)", b, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd cmd/ingestor && go test -run 'TestLinkedOnlyFilter|TestStatsFileCarriesClientUnlinkedDropped' .`
Expected: FAIL, build error `store.Stats.ClientUnlinkedDropped undefined` and `store.linkedCompanions undefined`.

- [ ] **Step 3: Implement**

a) In `cmd/ingestor/db.go`, in `DBStats`, directly after the line `	SignatureDrops         atomic.Int64`, add:

```go
	// ClientUnlinkedDropped counts meshcore/client/<pubkey>/... messages
	// dropped by the linked-only filter (clientRxCoverage.requireLinkedCompanion).
	ClientUnlinkedDropped atomic.Int64
```

b) In `cmd/ingestor/db.go`, in `type Store struct`, directly after the line `	Stats DBStats`, add:

```go

	// linkedCompanions is the linked-only ingest filter
	// (linked_companions.go); nil means off.
	linkedCompanions *linkedCompanionSet
```

c) In `cmd/ingestor/stats_file.go`, in `IngestorStatsSnapshot`, directly after the `SignatureDrops` field, add:

```go
	// ClientUnlinkedDropped counts client-topic messages dropped by the
	// linked-only filter (clientRxCoverage.requireLinkedCompanion).
	ClientUnlinkedDropped int64 `json:"client_unlinked_dropped"`
```

and in `StartStatsFileWriter`'s `snap := IngestorStatsSnapshot{` literal, directly after `SignatureDrops:       s.Stats.SignatureDrops.Load(),`, add:

```go
				ClientUnlinkedDropped: s.Stats.ClientUnlinkedDropped.Load(),
```

d) Append to `cmd/ingestor/linked_companions.go`:

```go

// allowClientPubkey applies the linked-only filter to a client topic's
// pubkey segment. A drop is counted, never logged: a flood would otherwise
// fill the log.
func (s *Store) allowClientPubkey(pubkey string) bool {
	if s.linkedCompanions == nil || s.linkedCompanions.Allow(pubkey) {
		return true
	}
	s.Stats.ClientUnlinkedDropped.Add(1)
	return false
}
```

e) In `cmd/ingestor/main.go`, in `handleMessage`, replace

```go
		if cfg.IsObserverBlacklisted(parts[2]) {
			log.Printf("MQTT [%s] client %.8s blacklisted, dropping", tag, parts[2])
			return
		}
		switch parts[3] {
```

with

```go
		if cfg.IsObserverBlacklisted(parts[2]) {
			log.Printf("MQTT [%s] client %.8s blacklisted, dropping", tag, parts[2])
			return
		}
		// Linked-only ingest (clientRxCoverage.requireLinkedCompanion): an
		// unlinked companion is dropped before any client handler runs.
		if !store.allowClientPubkey(parts[2]) {
			return
		}
		switch parts[3] {
```

Run `gofmt -w db.go stats_file.go linked_companions.go main.go`.

- [ ] **Step 4: Run the tests**

Run: `cd cmd/ingestor && go test -run 'TestLinkedOnlyFilter|TestStatsFile|TestClientRxCoverageGate|TestHandleClient|TestLinkedCompanion' .`
Expected: PASS. (`TestStatsFileWriter_PublishesProcIO` skips on hosts without `/proc/self/io`; the new stats test does not depend on it.)

- [ ] **Step 5: Commit**

```bash
git add cmd/ingestor/linked_companions.go cmd/ingestor/linked_companions_ingest_test.go cmd/ingestor/db.go cmd/ingestor/stats_file.go cmd/ingestor/main.go
git commit -F - <<'EOF'
feat(ingestor): drop client messages from unlinked companions

With the linked-only filter installed, a meshcore/client/<pubkey>/...
message whose pubkey is not linked is dropped before the packets, rf and
regions handlers, and counted as client_unlinked_dropped in the stats
file instead of being logged.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 4: Startup wiring and the ignored-setting warning

**Files:**
- Create: `cmd/ingestor/linked_companions_startup_test.go`
- Modify: `cmd/ingestor/linked_companions.go`, `cmd/ingestor/main.go`, `AGENTS.md`

- [ ] **Step 1: Write the failing test**

Create `cmd/ingestor/linked_companions_startup_test.go`:

```go
package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinkedCompanionFilterStartup(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	usersPath := filepath.Join(t.TempDir(), "users.db")
	setLinks(t, usersPath, testCompanionPK)
	um := func(on bool) *UserManagementConfig { return &UserManagementConfig{Enabled: on, DBPath: usersPath} }

	off := &Config{ClientRxCoverage: &ClientRxCoverageConfig{Enabled: true}, UserManagement: um(true)}
	if f := newLinkedCompanionFilter(off); f != nil {
		t.Fatal("filter installed with the setting off")
	}
	if buf.Len() != 0 {
		t.Fatalf("logged with the setting off:\n%s", buf.String())
	}

	ignored := &Config{ClientRxCoverage: &ClientRxCoverageConfig{RequireLinkedCompanion: true}, UserManagement: um(false)}
	if f := newLinkedCompanionFilter(ignored); f != nil {
		t.Fatal("filter installed with user management off")
	}
	if !strings.Contains(buf.String(), "requireLinkedCompanion is ignored") {
		t.Fatalf("no startup warning for the ignored setting:\n%s", buf.String())
	}

	on := &Config{ClientRxCoverage: &ClientRxCoverageConfig{RequireLinkedCompanion: true}, UserManagement: um(true)}
	f := newLinkedCompanionFilter(on)
	if f == nil {
		t.Fatal("no filter with the setting and user management on")
	}
	defer f.Close()
	if !f.Allow(testCompanionPK) {
		t.Fatal("startup did not read the linked set")
	}
	if !strings.Contains(buf.String(), "only linked companions") {
		t.Fatalf("no startup line naming the filter:\n%s", buf.String())
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd cmd/ingestor && go test -run TestLinkedCompanionFilterStartup .`
Expected: FAIL, build error `undefined: newLinkedCompanionFilter`.

- [ ] **Step 3: Implement**

a) In `cmd/ingestor/linked_companions.go`, add `"path/filepath"` to the import block, and append:

```go

// newLinkedCompanionFilter returns the linked-only filter, already read
// once, when clientRxCoverage.requireLinkedCompanion is in effect, else nil.
// The setting without userManagement.enabled is ignored with a startup
// warning, as on the server.
func newLinkedCompanionFilter(cfg *Config) *linkedCompanionSet {
	if !cfg.RequireLinkedCompanionSet() {
		return nil
	}
	if !cfg.RequireLinkedCompanion() {
		log.Printf("[companions] WARNING: clientRxCoverage.requireLinkedCompanion is ignored: it needs userManagement.enabled")
		return nil
	}
	shown := cfg.UsersDBPath()
	if abs, err := filepath.Abs(shown); err == nil {
		shown = abs
	}
	log.Printf("[companions] only linked companions may publish client data; reading companion_links from %s", shown)
	s := newLinkedCompanionSet(cfg.UsersDBPath())
	s.refresh()
	return s
}

// refreshLoop re-reads the set every linkedCompanionsRefresh for the life of
// the process, which picks up unlinks.
func (s *linkedCompanionSet) refreshLoop() {
	t := time.NewTicker(linkedCompanionsRefresh)
	defer t.Stop()
	for range t.C {
		s.refresh()
	}
}
```

b) In `cmd/ingestor/main.go`, replace

```go
	regionSet := newRegionKeySet(cfg)
```

with

```go
	// Linked-only ingest (clientRxCoverage.requireLinkedCompanion): nil when
	// off or ignored. Installed before any MQTT source connects.
	if lc := newLinkedCompanionFilter(cfg); lc != nil {
		store.linkedCompanions = lc
		go lc.refreshLoop()
	}

	regionSet := newRegionKeySet(cfg)
```

c) In `AGENTS.md`, replace

```
  for the approved hashtag channel names. `TestChannelKeySetIsReadOnly` pins
  the read-only open.
```

with

```
  for the approved hashtag channel names. With
  `clientRxCoverage.requireLinkedCompanion` it reads `companion_links` the same
  way (`SELECT pubkey`, every 60 s, plus at most one re-read per 5 s on a miss).
  `TestChannelKeySetIsReadOnly` and `TestLinkedCompanionSetIsReadOnly` pin the
  read-only open.
```

Run `gofmt -w linked_companions.go main.go`.

- [ ] **Step 4: Run the ingestor suite**

Run: `cd cmd/ingestor && go vet . && go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/ingestor/linked_companions.go cmd/ingestor/linked_companions_startup_test.go cmd/ingestor/main.go AGENTS.md
git commit -F - <<'EOF'
feat(ingestor): turn on the linked-only filter at startup

clientRxCoverage.requireLinkedCompanion with user management on installs
the filter and refreshes it every 60 s; without user management the
setting is ignored with a startup warning.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 5: Account page, Devices: CoreDrive RX device tokens

**Files:**
- Modify: `public/account.js`, `public/account.css`, `tests/unit/test-user-management-ui.js`

- [ ] **Step 1: Write the failing test**

In `tests/unit/test-user-management-ui.js`, directly after the test `'sessions list marks the current device without a logout button'` (its closing `});`), add:

```js
test('device tokens render as CoreDrive RX with a marker and keep their logout button', () => {
  const env = loadAccount('#/account', () => ({}));
  const h = env.t.sessionsHtml([
    { id: 1, kind: 'web', label: '', userAgent: 'Firefox', lastSeenAt: '2026-01-01T00:00:00Z', current: true },
    { id: 2, kind: 'device', label: 'Pixel <8>', userAgent: 'CoreDriveRX/1.17', lastSeenAt: '2026-01-01T00:00:00Z', current: false },
    { id: 3, kind: 'device', label: '', userAgent: '', lastSeenAt: '2026-01-01T00:00:00Z', current: false }]);
  assert(h.indexOf('<li><span>Firefox') !== -1, 'web session changed: ' + h);
  assert(h.indexOf('CoreDrive RX – Pixel &lt;8&gt;') !== -1, 'no labelled device row: ' + h);
  assert(h.indexOf('<span>CoreDrive RX <span class="um-chip um-chip-device">') !== -1, 'no unlabelled device row: ' + h);
  assert(h.indexOf('CoreDriveRX/1.17') === -1, 'a device row shows its user agent instead of its label');
  assert.strictEqual((h.match(/data-kind="device"/g) || []).length, 2);
  assert.strictEqual((h.match(/um-chip-device/g) || []).length, 2);
  assert(h.indexOf('data-sess="2"') !== -1 && h.indexOf('data-sess="3"') !== -1, 'device rows lost their logout button');
});
```

- [ ] **Step 2: Run it and watch it fail**

Run: `node tests/unit/test-user-management-ui.js`
Expected: `FAIL device tokens render as CoreDrive RX with a marker and keep their logout button: no labelled device row: …`, exit code 1.

- [ ] **Step 3: Implement**

a) In `public/account.js`, replace

```js
  function sessionsHtml(list) {
    var html = '';
    (list || []).forEach(function (s) {
      html += '<li><span>' + escapeHtml(s.userAgent || 'Unknown device') + '<br><small>last seen ' + escapeHtml(fmtDate(s.lastSeenAt)) + '</small></span>' +
```

with

```js
  // A CoreDrive RX device token (kind "device") shows its label and a marker;
  // a browser session its user agent. Both are revoked with the same button.
  function sessionsHtml(list) {
    var html = '';
    (list || []).forEach(function (s) {
      var device = s.kind === 'device';
      var name = device ? 'CoreDrive RX' + (s.label ? ' – ' + s.label : '') : (s.userAgent || 'Unknown device');
      html += '<li' + (device ? ' data-kind="device"' : '') + '><span>' + escapeHtml(name) +
        (device ? ' <span class="um-chip um-chip-device">app</span>' : '') +
        '<br><small>last seen ' + escapeHtml(fmtDate(s.lastSeenAt)) + '</small></span>' +
```

(The two lines after it, building the `this device` chip or the `data-sess` logout button, stay as they are.)

b) In `public/account.css`, directly after the `.account-sessions li { … }` line, add:

```css
.um-chip-device { margin-left: 4px; font-size: var(--fs-xs, 11px); color: var(--text-muted); }
```

- [ ] **Step 4: Run the tests**

Run: `node tests/unit/test-user-management-ui.js`
Expected: every line `ok`, last line `N passed, 0 failed`.

- [ ] **Step 5: Commit**

```bash
git add public/account.js public/account.css tests/unit/test-user-management-ui.js
git commit -F - <<'EOF'
feat(account): show CoreDrive RX device tokens under Devices

A device token lists as "CoreDrive RX – <label>" with an app marker
instead of a user agent, and is logged out with the same button.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 6: Account page, Companions section with Unlink

**Files:**
- Modify: `public/account.js`, `public/account.css`, `tests/unit/test-user-management-ui.js`

- [ ] **Step 1: Write the failing tests**

In `tests/unit/test-user-management-ui.js`, directly after the test added in Task 5, add:

```js
test('companions list escapes fields, shortens the pubkey and says never without a reception', () => {
  const env = loadAccount('#/account', () => ({}));
  const pk = 'ab'.repeat(32);
  const h = env.t.companionsHtml([
    { pubkey: pk, name: '<img src=x>', linkedAt: '2026-10-01T10:00:00Z', lastSeenAt: null },
    { pubkey: '"><b>', name: '', linkedAt: 'x', lastSeenAt: '2026-10-02T09:00:00Z' }]);
  assert(h.indexOf('<img') === -1 && h.indexOf('<b>') === -1, 'raw tag in companions HTML: ' + h);
  assert(h.indexOf('&lt;img src=x&gt;') !== -1, 'name missing: ' + h);
  assert(h.indexOf('<code>' + pk.slice(0, 12) + '…</code>') !== -1, 'no short pubkey: ' + h);
  assert(h.indexOf('data-unlink="' + pk + '"') !== -1, 'Unlink does not carry the full pubkey');
  assert(h.indexOf('last seen never') !== -1, 'no "never" for a null lastSeenAt');
  assert.strictEqual((h.match(/data-unlink=/g) || []).length, 2);
});

test('companions empty state explains linking from CoreDrive RX', () => {
  const env = loadAccount('#/account', () => ({}));
  [[], null].forEach((list) => {
    const h = env.t.companionsHtml(list);
    assert(h.indexOf('id="compEmpty"') !== -1 && h.indexOf('CoreDrive RX') !== -1 && h.indexOf('data-unlink') === -1, h);
  });
});

test('profile view has a Companions section below Devices', () => {
  const env = loadAccount('#/account', () => ({}));
  const html = env.t.profileHtml({ email: 'a@b.c', role: 'user', displayName: 'Ann' });
  const dev = html.indexOf('<h3>Devices</h3>'), comp = html.indexOf('<h3>Companions</h3>');
  assert(dev !== -1 && comp > dev, 'Companions heading missing or above Devices');
  assert(html.indexOf('id="compList"') !== -1 && html.indexOf('id="compMsg"') !== -1);
});

test('profile view lists companions; Unlink confirms, sends DELETE and reloads the list', async () => {
  const pk = 'cd'.repeat(32);
  let linked = [{ pubkey: pk, name: 'Car', linkedAt: '2026-10-01T10:00:00Z', lastSeenAt: null }];
  const env = loadAccount('#/account', (p) => {
    if (p === '/api/account/companions') return { ok: true, status: 200, data: linked };
    if (p === '/api/account/companions/' + pk) { linked = []; return { ok: true, status: 204, data: {} }; }
    return { ok: true, status: 200, data: [] };
  }, { confirm: () => true });
  env.user.current = { id: 1, email: 'a@b.c', displayName: 'Ann', role: 'user' };
  env.t.views.profile({ set innerHTML(v) {} });
  await new Promise((r) => setTimeout(r, 5));
  assert(env.els.compList.innerHTML.indexOf('data-unlink="' + pk + '"') !== -1, env.els.compList.innerHTML);
  await env.els.compList.handlers.click({ target: { getAttribute: (a) => (a === 'data-unlink' ? pk : null) } });
  await new Promise((r) => setTimeout(r, 5));
  assert(env.calls.some((c) => c.method === 'DELETE' && c.p === '/api/account/companions/' + pk), 'no DELETE');
  assert.strictEqual(env.els.compMsg.textContent, 'Companion unlinked.');
  assert(env.els.compList.innerHTML.indexOf('id="compEmpty"') !== -1, 'list not reloaded');
});

test('a cancelled Unlink sends nothing', async () => {
  const pk = 'cd'.repeat(32);
  const env = loadAccount('#/account', (p) => (p === '/api/account/companions'
    ? { ok: true, status: 200, data: [{ pubkey: pk, name: '', linkedAt: 'x', lastSeenAt: null }] }
    : { ok: true, status: 200, data: [] }), { confirm: () => false });
  env.user.current = { id: 1, email: 'a@b.c', displayName: 'Ann', role: 'user' };
  env.t.views.profile({ set innerHTML(v) {} });
  await new Promise((r) => setTimeout(r, 5));
  await env.els.compList.handlers.click({ target: { getAttribute: () => pk } });
  assert(!env.calls.some((c) => c.method === 'DELETE'), 'DELETE sent after cancel');
});

test('a rejected companions load shows the error in compMsg', async () => {
  const env = loadAccount('#/account', () => ({}));
  env.user.current = { id: 1, email: 'a@b.c', displayName: 'Ann', role: 'user' };
  env.setRequest(() => Promise.reject(new Error('net')));
  env.t.views.profile({ set innerHTML(v) {} });
  await new Promise((r) => setTimeout(r, 5));
  assert.strictEqual(env.els.compMsg.textContent, 'Network error, try again.');
});
```

- [ ] **Step 2: Run them and watch them fail**

Run: `node tests/unit/test-user-management-ui.js`
Expected: `FAIL companions list escapes fields…: env.t.companionsHtml is not a function` (and the other new tests fail), exit code 1.

- [ ] **Step 3: Implement**

a) In `public/account.js`, in `profileHtml`, replace

```js
      '<h3>Devices</h3><ul class="account-sessions" id="sessList"></ul>' + msgBox('sessMsg') +
```

with

```js
      '<h3>Devices</h3><ul class="account-sessions" id="sessList"></ul>' + msgBox('sessMsg') +
      '<h3>Companions</h3><ul class="account-sessions" id="compList"></ul>' + msgBox('compMsg') +
```

b) In `public/account.js`, directly after the `sessionsHtml` function (its closing `}`), add:

```js

  function shortKey(pk) { return String(pk || '').slice(0, 12) + '…'; }

  // Companions linked to this account (CoreDrive RX links them by signing a
  // challenge with the companion's key). Private to the owner.
  function companionsHtml(list) {
    if (!list || !list.length) {
      return '<li class="account-empty" id="compEmpty"><span>No companions linked yet. ' +
        'To link one, log in to this site from the CoreDrive RX app while it is connected to your companion; ' +
        'it then shows up here and in My nodes.</span></li>';
    }
    var html = '';
    list.forEach(function (c) {
      var name = c.name || shortKey(c.pubkey);
      html += '<li><span>' + escapeHtml(name) + ' <small><code>' + escapeHtml(shortKey(c.pubkey)) + '</code></small>' +
        '<br><small>linked ' + escapeHtml(fmtDate(c.linkedAt)) + ' · last seen ' +
        escapeHtml(c.lastSeenAt ? fmtDate(c.lastSeenAt) : 'never') + '</small></span>' +
        '<button type="button" class="account-btn account-btn-secondary" data-unlink="' + escapeHtml(c.pubkey) +
        '" aria-label="Unlink ' + escapeHtml(name) + '">Unlink</button></li>';
    });
    return html;
  }
```

c) In `public/account.js`, in `views.profile`, replace

```js
      loadSessions();
    }
  };
```

with

```js
      loadSessions();

      function loadCompanions() {
        return CSAuth.request('GET', '/api/account/companions').then(function (r) {
          var list = document.getElementById('compList');
          if (!list) return;
          if (!r.ok) { CSAuth.say('compMsg', CSAuth.errText(r), false); return; }
          list.innerHTML = companionsHtml(r.data);
        }).catch(function () { CSAuth.say('compMsg', 'Network error, try again.', false); });
      }
      document.getElementById('compList').addEventListener('click', function (e) {
        var pk = e.target && e.target.getAttribute && e.target.getAttribute('data-unlink');
        if (!pk) return;
        if (!window.confirm('Unlink this companion? It stays in My nodes, but its coverage no longer counts as yours. ' +
          'Link it again by logging in from CoreDrive RX.')) return;
        return CSAuth.request('DELETE', '/api/account/companions/' + encodeURIComponent(pk)).then(function (r) {
          CSAuth.say('compMsg', r.ok ? 'Companion unlinked.' : CSAuth.errText(r), r.ok);
          loadCompanions();
        }).catch(function () { CSAuth.say('compMsg', 'Network error, try again.', false); });
      });
      loadCompanions();
    }
  };
```

d) In `public/account.js`, replace the last line

```js
  window.CSAccount = { _test: { profileHtml: profileHtml, sessionsHtml: sessionsHtml, renewLinkHtml: renewLinkHtml, views: views, checkMailHtml: checkMailHtml } };
```

with

```js
  window.CSAccount = { _test: { profileHtml: profileHtml, sessionsHtml: sessionsHtml, companionsHtml: companionsHtml, renewLinkHtml: renewLinkHtml, views: views, checkMailHtml: checkMailHtml } };
```

e) In `public/account.css`, directly after the `.um-chip-device` line from Task 5, add:

```css
.account-sessions li.account-empty { color: var(--text-muted); }
```

- [ ] **Step 4: Run the tests**

Run: `node tests/unit/test-user-management-ui.js`
Expected: every line `ok`, last line `N passed, 0 failed`.

- [ ] **Step 5: Commit**

```bash
git add public/account.js public/account.css tests/unit/test-user-management-ui.js
git commit -F - <<'EOF'
feat(account): list linked companions with an Unlink button

A Companions section below Devices shows name, short pubkey, linked
since and last seen; Unlink asks first and calls DELETE. The empty state
explains that linking happens by logging in from CoreDrive RX.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 7: Coverage page "My coverage" toggle

**Files:**
- Create: `tests/unit/test-rx-coverage-mine.js`
- Modify: `public/rx-coverage.js`, `test-all.sh`

- [ ] **Step 1: Write the failing test**

Create `tests/unit/test-rx-coverage-mine.js`:

```js
'use strict';
const REPO_ROOT = require('path').resolve(__dirname, '..', '..');
// "My coverage" toggle (docs/specs/2026-10-08-companion-linking-design.md,
// Coverage attribution): shown to logged-in users only, it adds mine=1 to the
// signal-layer request. The real public/rx-coverage.js is loaded in a vm, with
// a window that has no addEventListener, like test-rx-coverage-config-race.js.
const assert = require('assert');
const fs = require('fs');
const path = require('path');
const vm = require('vm');

const code = fs.readFileSync(path.join(REPO_ROOT, 'public', 'rx-coverage.js'), 'utf8');
const sandbox = {
  window: {},
  document: { getElementById: function () { return null; } },
  registerPage: function () {},
  console: { warn: function () {} },
  Promise: Promise,
  setTimeout: function () {}, clearTimeout: function () {},
  fetch: function () { throw new Error('no fetch expected'); },
  L: {}, getComputedStyle: function () { return { getPropertyValue: function () { return ''; } }; },
};
vm.createContext(sandbox);
vm.runInContext(code, sandbox);
const t = sandbox.window.CSRxCoverage && sandbox.window.CSRxCoverage._test;
assert.ok(t, 'rx-coverage.js should expose window.CSRxCoverage._test');

// The request URL: mine=1 only with the toggle on, after the rx filter.
assert.strictEqual(t.coverageUrl('1,2,3,4', 9, 7, '', false), '/api/rx-coverage?bbox=1,2,3,4&z=9&days=7');
assert.strictEqual(t.coverageUrl('1,2,3,4', 9, 7, '', true), '/api/rx-coverage?bbox=1,2,3,4&z=9&days=7&mine=1');
assert.strictEqual(t.coverageUrl('1,2,3,4', 9, 30, 'ab cd', true), '/api/rx-coverage?bbox=1,2,3,4&z=9&days=30&rx=ab%20cd&mine=1');

// The toggle: nothing for a logged-out visitor, a pressed/unpressed button otherwise.
assert.strictEqual(t.mineBtnHtml(null, false), '', 'no toggle for a logged-out visitor');
const off = t.mineBtnHtml({ id: 1 }, false);
assert.ok(/data-mine="1"/.test(off) && /aria-pressed="false"/.test(off) && />My coverage</.test(off) && !/class="active"/.test(off), off);
const on = t.mineBtnHtml({ id: 1 }, true);
assert.ok(/aria-pressed="true"/.test(on) && /class="active"/.test(on), on);

// Only a logged-in user of an instance with accounts enabled counts.
const u = { id: 1 };
assert.strictEqual(t.authUser(undefined), null);
assert.strictEqual(t.authUser({ isEnabled: function () { return false; }, user: function () { return u; } }), null);
assert.strictEqual(t.authUser({ isEnabled: function () { return true; }, user: function () { return null; } }), null);
assert.strictEqual(t.authUser({ isEnabled: function () { return true; }, user: function () { return u; } }), u);

// Wiring: the signal layer builds its URL with the toggle, and the bar starts hidden.
assert.ok(code.indexOf('coverageUrl(bbox, map.getZoom(), days, selectedRx, mine && !!authUser(window.CSAuth))') !== -1,
  'drawSignalLayer must build its URL with coverageUrl and the toggle');
assert.ok(/id="rxMineBar"[^>]*hidden/.test(code), 'the toggle bar must start hidden');

console.log('rx-coverage My coverage OK');
```

- [ ] **Step 2: Run it and watch it fail**

Run: `node tests/unit/test-rx-coverage-mine.js`
Expected: `AssertionError [ERR_ASSERTION]: rx-coverage.js should expose window.CSRxCoverage._test`, exit code 1.

- [ ] **Step 3: Implement**

All edits in `public/rx-coverage.js`.

a) Replace

```js
  var layer = 'signal';
```

with

```js
  var layer = 'signal';
  // mine: "My coverage" — the signal layer only shows coverage collected by
  // companions linked to the logged-in user (/api/rx-coverage?mine=1).
  var mine = false;
```

b) Directly after the `layerBtn` function (its single line), add:

```js

  // mineBtnHtml renders the "My coverage" toggle, or nothing for a visitor
  // who is not logged in (?mine=1 needs a session).
  function mineBtnHtml(user, on) {
    if (!user) return '';
    return '<button data-mine="1"' + (on ? ' class="active"' : '') + ' aria-pressed="' + (on ? 'true' : 'false') + '"' +
      ' title="Only coverage collected by companions linked to your account">My coverage</button>';
  }

  // coverageUrl builds the signal-layer request.
  function coverageUrl(bbox, z, d, rx, mineOn) {
    return '/api/rx-coverage?bbox=' + bbox + '&z=' + z + '&days=' + d +
      (rx ? '&rx=' + encodeURIComponent(rx) : '') + (mineOn ? '&mine=1' : '');
  }

  // authUser is the logged-in user, or null (accounts off, logged out, or
  // auth.js not loaded).
  function authUser(auth) {
    return auth && auth.isEnabled && auth.isEnabled() && auth.user ? (auth.user() || null) : null;
  }
```

c) In `pageHtml`, replace

```js
      '<div class="analytics-time-range" id="rxDays" style="margin:8px 0">' + dayBtn(1) + dayBtn(7) + dayBtn(14) + dayBtn(30) + '</div>' +
```

with

```js
      '<div class="analytics-time-range" id="rxDays" style="margin:8px 0">' + dayBtn(1) + dayBtn(7) + dayBtn(14) + dayBtn(30) + '</div>' +
      '<div class="analytics-time-range" id="rxMineBar" style="margin:8px 0" hidden></div>' +
```

d) In `drawSignalLayer`, replace

```js
    var url = '/api/rx-coverage?bbox=' + bbox + '&z=' + map.getZoom() + '&days=' + days + (selectedRx ? '&rx=' + encodeURIComponent(selectedRx) : '');
```

with

```js
    // Until auth.js has answered, mine is never sent (it would answer 401).
    var url = coverageUrl(bbox, map.getZoom(), days, selectedRx, mine && !!authUser(window.CSAuth));
```

e) In `syncHash`, replace

```js
    var q = 'days=' + days + (selectedRx ? '&rx=' + encodeURIComponent(selectedRx) : '') + (layer !== 'signal' ? '&layer=' + layer : '');
```

with

```js
    var q = 'days=' + days + (selectedRx ? '&rx=' + encodeURIComponent(selectedRx) : '') + (layer !== 'signal' ? '&layer=' + layer : '') + (mine ? '&mine=1' : '');
```

f) In `start`, replace

```js
    selectedRx = ''; selectedName = ''; days = 7; boardCache = []; layer = 'signal';
```

with

```js
    selectedRx = ''; selectedName = ''; days = 7; boardCache = []; layer = 'signal'; mine = false;
```

and replace

```js
        if (window.MC_CLIENT_RF_SAMPLES && p.get('layer') === 'noise') layer = 'noise';
```

with

```js
        if (window.MC_CLIENT_RF_SAMPLES && p.get('layer') === 'noise') layer = 'noise';
        mine = p.get('mine') === '1';
```

g) In `start`, replace

```js
    if (layerBar) layerBar.addEventListener('click', function (e) { var b = e.target.closest('button[data-layer]'); if (b) setLayer(b.dataset.layer); });
```

with

```js
    if (layerBar) layerBar.addEventListener('click', function (e) { var b = e.target.closest('button[data-layer]'); if (b) setLayer(b.dataset.layer); });
    var mineBar = document.getElementById('rxMineBar');
    if (mineBar) mineBar.addEventListener('click', function (e) { var b = e.target.closest('button[data-mine]'); if (b) setMine(!mine); });
    // The toggle waits for auth.js's first /api/auth/me; a mine=1 link from a
    // logged-in user then redraws with the filter.
    Promise.resolve(window.CSAuth && window.CSAuth.ready ? window.CSAuth.ready() : null).then(function () {
      if (destroyed || current !== generation) return;
      renderMine();
      if (mine && map) drawCoverage();
    });
```

h) Replace

```js
  function destroy() {
```

with

```js
  // renderMine shows the toggle to a logged-in user only; without one the
  // toggle is off (a mine=1 link opened while logged out is ignored).
  function renderMine() {
    var user = authUser(window.CSAuth);
    if (!user) mine = false;
    var bar = document.getElementById('rxMineBar');
    if (!bar) return;
    bar.innerHTML = mineBtnHtml(user, mine);
    bar.hidden = !user;
  }

  function setMine(on) {
    if (on === mine) return;
    mine = on;
    renderMine();
    drawCoverage(); syncHash();
  }

  // Logging in or out while the page is open shows or hides the toggle; a
  // logout with the toggle on redraws everyone's coverage.
  function onAuthChanged() {
    if (destroyed || !map) return;
    var was = mine;
    renderMine();
    if (was !== mine) { drawCoverage(); syncHash(); }
  }

  function destroy() {
```

i) Replace the last lines

```js
  registerPage('rx-coverage', { init: init, destroy: destroy });
})();
```

with

```js
  if (window.addEventListener) window.addEventListener('cs-auth-changed', onAuthChanged);
  window.CSRxCoverage = { _test: { coverageUrl: coverageUrl, mineBtnHtml: mineBtnHtml, authUser: authUser } };
  registerPage('rx-coverage', { init: init, destroy: destroy });
})();
```

j) In `test-all.sh`, replace

```
node tests/unit/test-rx-coverage-config-race.js
```

with

```
node tests/unit/test-rx-coverage-config-race.js
node tests/unit/test-rx-coverage-mine.js
```

- [ ] **Step 4: Run the tests**

Run: `node tests/unit/test-rx-coverage-mine.js && node tests/unit/test-rx-coverage-config-race.js && node tests/unit/test-rx-coverage-escape.js && node tests/unit/test-rx-coverage-noise.js && node tests/unit/test-rx-coverage-viewport.js`
Expected: `rx-coverage My coverage OK`, `rx-coverage config race OK`, and the other three exit 0.

- [ ] **Step 5: Commit**

```bash
git add public/rx-coverage.js tests/unit/test-rx-coverage-mine.js test-all.sh
git commit -F - <<'EOF'
feat(rx-coverage): "My coverage" toggle for logged-in users

The toggle shows only to a logged-in user and limits the signal layer to
the coverage of their linked companions (/api/rx-coverage?mine=1). It is
kept in the hash and switches off on logout.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 8: e2e: device token, linked companion, unlink

**Files:**
- Modify: `tests/e2e/test-user-management-e2e.js`

The e2e server is the `-tags e2etest` build with user management on and `publicBaseUrl` `http://localhost:13582` (see the file's header), so the signed message's host is `new URL(BASE).host`. The device token and the link are seeded through the real API with a fresh Ed25519 key from Node's `crypto`; the page then shows them.

- [ ] **Step 1: Write the failing test**

a) In `tests/e2e/test-user-management-e2e.js`, replace

```js
const { chromium } = require('playwright');
```

with

```js
const crypto = require('crypto');
const { chromium } = require('playwright');
```

b) Directly before the line

```js
  await step('feature off: no userManagement block, so no notifications flag, and no toggle on the node page', async () => {
```

add:

```js
  // Companion linking (docs/specs/2026-10-08-companion-linking-design.md).
  const driver = await (await browser.newContext()).newPage();
  driver.setDefaultTimeout(8000);
  driver.on('pageerror', (e) => console.error('[pageerror driver]', e.message));
  await step('companions: a device token shows under Devices, a linked companion under Companions, and unlinks', async () => {
    await registerAndActivate(driver, 'driver@e2e.test', 'E2E Driver');
    const api = driver.request;
    let r = await api.post(BASE + '/api/auth/device-token', { data: { email: 'driver@e2e.test', password: PW, deviceName: 'E2E Pixel' } });
    assert(r.ok(), 'device-token HTTP ' + r.status());
    const auth = { Authorization: 'Bearer ' + (await r.json()).token };
    const { privateKey, publicKey } = crypto.generateKeyPairSync('ed25519');
    const pk = publicKey.export({ format: 'der', type: 'spki' }).subarray(-32).toString('hex');
    r = await api.post(BASE + '/api/account/companions/challenge', { headers: auth, data: { pubkey: pk } });
    assert(r.ok(), 'challenge HTTP ' + r.status());
    const { challenge } = await r.json();
    const signed = Buffer.from('corescope-link:' + new URL(BASE).host + ':' + challenge, 'utf8');
    const signature = crypto.sign(null, signed, privateKey).toString('hex');
    r = await api.post(BASE + '/api/account/companions', { headers: auth, data: { pubkey: pk, challenge, signature, name: 'E2E Car' } });
    assert(r.ok(), 'link HTTP ' + r.status() + ': ' + (await r.text()));

    await driver.goto(BASE + '/#/account');
    await driver.reload();
    await driver.waitForSelector('#sessList li[data-kind="device"]');
    assert((await driver.textContent('#sessList')).indexOf('CoreDrive RX – E2E Pixel') !== -1, 'device token not listed as CoreDrive RX');
    const unlink = '#compList [data-unlink="' + pk + '"]';
    await driver.waitForSelector(unlink);
    assert((await driver.textContent('#compList')).indexOf('E2E Car') !== -1, 'companion name missing');
    await axeClean(driver, '#compList');

    driver.once('dialog', (d) => d.accept());
    await driver.click(unlink);
    await driver.waitForSelector('#compEmpty');
    assert((await driver.textContent('#compMsg')).indexOf('Companion unlinked') !== -1, 'no unlink message');
    r = await api.get(BASE + '/api/account/companions', { headers: auth });
    assert(r.ok() && (await r.json()).length === 0, 'companion still linked after Unlink');
  });
  await driver.context().close();

```

- [ ] **Step 2: Run it and watch it fail against a server without F3's frontend**

Start the two servers as described in the file's header comment, but with `public/` from before Task 5 (`git stash` the Tasks 5–6 changes, or run this step before them).
Run: `BASE_URL=http://localhost:13582 BASE_URL_OFF=http://localhost:13581 node tests/e2e/test-user-management-e2e.js`
Expected: `✗ companions: … : page.waitForSelector: Timeout 8000ms exceeded` (no `li[data-kind="device"]`), exit code 1.

- [ ] **Step 3: Implementation**

None beyond Tasks 5–6; restore them (`git stash pop`) and restart the e2e server so it serves the current `public/`.

- [ ] **Step 4: Run it**

Run: `BASE_URL=http://localhost:13582 BASE_URL_OFF=http://localhost:13581 node tests/e2e/test-user-management-e2e.js`
Expected: `✓ companions: a device token shows under Devices, a linked companion under Companions, and unlinks`, last line `N/N tests passed`, exit code 0.

- [ ] **Step 5: Commit**

```bash
git add tests/e2e/test-user-management-e2e.js
git commit -F - <<'EOF'
test(e2e): device token, linked companion and unlink on the account page

Seeds a device token and a companion link through the real API (fresh
Ed25519 key, signed challenge), then checks Devices and Companions and
unlinks from the page.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 9: Docs and release note

**Files:**
- Modify: `docs/client-rx-coverage.md`, `docs/user-guide/accounts.md`, `config.example.json`

- [ ] **Step 1: Write the failing check**

Run: `grep -c "requireLinkedCompanion" docs/client-rx-coverage.md docs/user-guide/accounts.md`
Expected: `docs/client-rx-coverage.md:0` and `docs/user-guide/accounts.md:0` (exit code 1: the docs do not cover the feature yet).

- [ ] **Step 2: `docs/client-rx-coverage.md`**

Directly before the line `## Companion BLE source (verified against firmware)`, add:

````markdown
## Linked companions only (optional)

With [user management](user-guide/accounts.md) on, users link their companions from CoreDrive RX
(the companion signs a challenge with its own key). An operator can then accept client data from
linked companions only:

```json
"clientRxCoverage": { "enabled": true, "requireLinkedCompanion": true }
```

- The ingestor drops every `meshcore/client/{PUBLIC_KEY}/…` message (`packets`, `rf`, `regions`)
  whose pubkey is not linked to an account, before any handler runs. Drops are counted as
  `client_unlinked_dropped` in the ingestor stats file and never logged per message.
- It reads the linked pubkeys from `users.db` (read-only) every 60 s, which picks up unlinks. A
  pubkey it does not know triggers one extra read at most every 5 s, so a companion linked a
  moment ago is accepted within seconds.
- `/api/config/client` then carries `clientRxRequireLinkedCompanion: true`, and RX holds its queue
  until its companion is linked instead of publishing data that would be dropped.
- It needs `userManagement.enabled`. Without it the server and the ingestor log a startup warning
  and ignore the setting. A `users.db` from before companion linking (no `companion_links` table)
  counts as no linked companions, with one warning.
- Unlinking does not delete coverage that is already stored.

**It is a filter, not a security boundary.** All RX clients share one broker account, so anyone
with that password can still publish under a linked pubkey. The setting keeps honest, unlinked
clients out; real enforcement needs per-user publish credentials (a later sub-project). The broker
ACL in [Trust](#trust) stays the trust boundary.

> **Upgrade the RX clients first.** An RX build without account support cannot link, so under
> `requireLinkedCompanion` all of its data is dropped. Turn the setting on only once the RX
> clients in use support linking.

````

- [ ] **Step 3: `docs/user-guide/accounts.md`**

a) Directly before the line `### Backups`, add:

````markdown
### Companion linking (CoreDrive RX)

With user management on, a user logs in from the CoreDrive RX app with their email and password.
RX then holds a **device token** instead of a cookie: it shows under *My account, Devices* as
"CoreDrive RX – <device name>", lasts 90 days from its last use, and *Log out* there revokes it.
The token is limited to the account routes RX needs (`/api/auth/me`, `/api/auth/logout`,
`/api/account/settings`, `/api/account/companions*`); anything else answers 403.

RX then links the companion it is connected to: the companion signs a server challenge with its
own key, so only whoever holds the device can link it. The newest proof wins: a companion that
changes hands moves to the new owner, and the previous owner gets an audit entry and, with
notifications on, a mail. A linked companion is added to the user's *My nodes*; its coverage counts
as theirs (*My coverage* on the coverage page). Links are private to their owner and to admins
(*Admin, user detail* lists them).

- **RX on another origin** (for example a hosted RX build): add that origin to
  `corsAllowedOrigins`. For listed origins, and only on the device-token routes above, the server
  then also allows `POST, PUT, DELETE` and the `Authorization` and `Content-Type` headers.
  `Access-Control-Allow-Credentials` stays off; RX sends the token as a bearer header, not a
  cookie. Same-origin RX needs nothing.
- The signed message contains the host of `publicBaseUrl`, so RX must log in on that address.
- **Linked companions only:** `clientRxCoverage.requireLinkedCompanion` makes the ingestor drop
  client data from companions nobody linked. It is a filter, not a security boundary, and it drops
  everything from RX builds without account support: upgrade the RX clients first. See
  [Client RX Coverage, Linked companions only](../client-rx-coverage.md#linked-companions-only-optional).

````

b) Replace

```
  or off, chooses the events, and *Watch my nodes* copies your synced My nodes. You get
  at most one mail per check, and every mail has a link that turns the mails off.
```

with

```
  or off, chooses the events, and *Watch my nodes* copies your synced My nodes. You get
  at most one mail per check, and every mail has a link that turns the mails off.
- **Companions:** log in from the CoreDrive RX app while it is connected to your
  companion, and it is linked to your account: it shows under *My account, Companions*
  (name, short key, linked since, last seen) and is added to My nodes. The app shows
  under *Devices* as "CoreDrive RX – <device name>"; *Log out* there signs the app out.
  *Unlink* removes the link but leaves the companion in My nodes and its coverage on the
  map. On the coverage page, *My coverage* shows only what your linked companions heard.
```

- [ ] **Step 4: `config.example.json`**

In the `"_comment_corsAllowedOrigins"` value, replace the text

```
and Access-Control-Allow-Methods is limited to GET, HEAD, OPTIONS (the cross-domain surface is read-only — same-origin admin writes are unaffected).
```

with

```
and Access-Control-Allow-Methods is limited to GET, HEAD, OPTIONS (the cross-domain surface is read-only — same-origin admin writes are unaffected). One exception, with userManagement.enabled: the CoreDrive RX device-token routes (/api/auth/device-token, /api/auth/me, /api/auth/logout, /api/account/settings, /api/account/companions*) also allow POST, PUT, DELETE and the Authorization and Content-Type headers for listed origins, so an RX build on another origin can log in and link companions with a bearer token.
```

Run: `node -e "JSON.parse(require('fs').readFileSync('config.example.json','utf8'))"`
Expected: no output, exit code 0.

- [ ] **Step 5: Release note draft (do NOT create the file)**

The fork writes one short Dutch note per `-on8ar.N` tag in `docs/release-notes/nl/` (latest: `v3.13.1-on8ar.3.md`). **This note may only be added and published once the version runs on the LIVE environment** (user decision, 2026-10-08). So this plan does not create it: keep the draft below here, and when the release is live, save it as `docs/release-notes/nl/<live tag>.md` and publish it then.

```markdown
Je account en CoreDrive RX horen nu bij elkaar.

**Companions koppelen.** Log in vanuit de CoreDrive RX-app terwijl die met je companion verbonden is. De companion bewijst met zijn eigen sleutel dat hij van jou is, en staat daarna op je accountpagina onder *Companions* en in je My nodes. De app zelf zie je onder *Devices* als "CoreDrive RX – <toestelnaam>"; daar log je hem ook weer uit. *Unlink* maakt de koppeling ongedaan; de companion blijft in My nodes en zijn dekking blijft op de kaart.

**Mijn dekking.** Op de dekkingspagina toont *My coverage* alleen wat jouw gekoppelde companions gehoord hebben. De knop zie je alleen als je ingelogd bent.

**Voor beheerders.** Met `clientRxCoverage.requireLinkedCompanion` neemt de analyser alleen nog data aan van gekoppelde companions. Dat is een filter, geen beveiliging: alle RX-clients delen één broker-account. Zet het pas aan als iedereen een RX-versie met inloggen gebruikt, anders valt hun data weg.

Zie je iets vreemds? Zeg het hier.
```

- [ ] **Step 6: Check and commit**

Run: `grep -c "requireLinkedCompanion" docs/client-rx-coverage.md docs/user-guide/accounts.md`
Expected: both counts at least 1.

```bash
git add docs/client-rx-coverage.md docs/user-guide/accounts.md config.example.json
git commit -F - <<'EOF'
docs: companion linking, linked-only ingest and the CORS exception

Operator and user docs for device tokens, companions and My coverage;
requireLinkedCompanion with the filter-not-boundary note and the
upgrade-RX-first warning; the corsAllowedOrigins comment names the
bearer routes. The Dutch release note waits until the version is live.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 10: Full verification

**Files:** none (fix forward in the task that owns a failure).

- [ ] **Step 1: Go modules**

Run: `make test`
Expected: all modules PASS (`cmd/ingestor`, `cmd/server`, `internal/users`, `internal/sigvalidate` and the rest).

- [ ] **Step 2: Race check on the ingestor filter**

Run: `cd cmd/ingestor && go test -race -run 'TestLinkedCompanion|TestLinkedOnlyFilter' .`
Expected: PASS, no `DATA RACE`.

- [ ] **Step 3: Frontend unit tests**

Run: `sh test-all.sh`
Expected: exit code 0; the output includes `rx-coverage My coverage OK` and `0 failed` for `test-user-management-ui.js`.

- [ ] **Step 4: e2e**

Run the two servers from the header of `tests/e2e/test-user-management-e2e.js`, then:
`BASE_URL=http://localhost:13582 BASE_URL_OFF=http://localhost:13581 node tests/e2e/test-user-management-e2e.js`
Expected: `N/N tests passed`, exit code 0.

- [ ] **Step 5: OpenAPI completeness**

Run: `cd cmd/server && go test -run 'TestOpenAPI' .`
Expected: PASS (F3 adds no route; F2's entries stay complete).

---

## Self-review against the spec

- *Linked-only ingest, ingestor part*: setting read from `clientRxCoverage.requireLinkedCompanion` and in effect only with `userManagement.enabled`, ignored with a startup warning otherwise (Tasks 1, 4); in-memory set from `companion_links`, read-only raw SQL, refreshed every 60 s (Tasks 2, 4); a miss re-reads at most once per 5 s (Task 2); the check runs before the `packets`, `rf` and `regions` handlers (Task 3); drops counted as `client_unlinked_dropped` in the stats file, never logged per message (Task 3); pass-through until the first successful read, an old `users.db` without the table warns once (Task 2).
- *Testing → cmd/ingestor*: off changes nothing (`TestLinkedOnlyFilterOffChangesNothing`); linked passes (`…PassesLinked`, `TestLinkedCompanionSetAllowsLinkedOnly`); unlinked dropped and counted (`…DropsUnlinkedBeforeEveryHandler`, `TestStatsFileCarriesClientUnlinkedDropped`); link after the last refresh accepted through the miss refresh (`…MissRefreshAcceptsNewLink`); 5 s cap under a flood (`…MissRefreshCapHoldsUnderFlood`); unlink honoured after the periodic refresh (`…UnlinkHonouredAfterPeriodicRefresh`); user management off ignored with a warning (`TestLinkedCompanionFilterStartup`).
- *Account page*: device tokens with label and marker, nothing else changes (Task 5); Companions below Devices with name, short pubkey, linked since, last seen, Unlink, and the empty-state text (Task 6).
- *Coverage attribution*: "My coverage" toggle only for logged-in users, fetching `?mine=1` (Task 7).
- *CORS*: documented for operators and in the `corsAllowedOrigins` comment (Task 9); the behaviour is F2's.
- *Rollout*: opt-in; "upgrade RX clients first" in both docs and the release-note draft (Task 9). "Filter, not a security boundary" in both docs, the release note and the code comment (Tasks 2, 9).
- *e2e*: seeded device token under Devices, linked companion under Companions, unlink (Task 8).
- Deviations, resolved here: the spec says drops are counted "per reason"; the stats file has no per-reason map, so the one reason is a top-level field `client_unlinked_dropped` next to `sig_drops`. The filter lives on `Store` rather than as a `handleMessage` parameter, so the 92 existing test call sites stay as they are. The spec does not say what a failed `users.db` read means for the filter; everything passes until the first successful read (decided with the user, 2026-10-08), and after that a failed read keeps the last good set. "My coverage" applies to the signal layer only, since only `/api/rx-coverage` has `mine`. The Dutch release note is a draft in Task 9; it is added and published only once the version runs on LIVE.
