# Companion Linking F1 (users store) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give `internal/users` everything sub-project F needs from `users.db`: the schema v6 migration, device sessions (kind, label, scopes, 90-day sliding expiry), single-use link challenges, and the companion-link table with transfer detection, plus the janitor pruning of expired challenges.

**Architecture:** One forward-only migration appended to `migrations` in `schema.go`. Device tokens are rows in the existing `sessions` table (`kind = 'device'`), created through the same private insert path as web sessions, so lookup, sliding extension, listing and revocation are the existing methods with three more columns. Challenges and links live in two new files. Challenge consumption is a single `DELETE … RETURNING` statement, so a challenge is burned in every outcome and two concurrent consumers cannot both win. The store keeps `SetMaxOpenConns(1)`, so the link upsert's read-then-write transaction is serialized; code inside a transaction must use `tx`, never `s.db` (one connection: `s.db` inside a tx deadlocks).

**Tech Stack:** Go (`internal/users` is its own module, `github.com/meshcore-analyzer/users`; `cmd/server` consumes it through a `replace`), SQLite via `modernc.org/sqlite`, stdlib `testing`, `crypto/rand`, `crypto/sha256`.

**Spec:** `docs/specs/2026-10-08-companion-linking-design.md`, sections *Data model*, *Device token* (store parts), *Companions* steps 1 and 3, *Testing → internal/users*.

---

## Exported API (F2 relies on these names)

| Name | Kind | Meaning |
|---|---|---|
| `SessionKindWeb`, `SessionKindDevice` | const string | `"web"`, `"device"`; the values of `sessions.kind`. |
| `DeviceSessionTTL` | const `time.Duration` | 90 days. F2 passes it to `ExtendSession` on every bearer use (sliding). |
| `LinkChallengeTTL` | const `time.Duration` | 5 minutes. |
| `Session.Kind`, `Session.Label`, `Session.Scopes` | fields | New on the existing `Session`. `Scopes` is `nil` for web sessions (= full access). |
| `(*Session).HasScope(scope string) bool` | method | Web: always true. Device: `scope` is in `Scopes`. |
| `CleanLabel(s string) string` | func | Strips invalid UTF-8 and control characters, trims, caps at 64 runes. Used for device labels and companion names. |
| `NormalizePubkey(s string) (string, error)` | func | Trim + lowercase; must be 64 hex characters, else `ErrBadPubkey`. |
| `(*Store).CreateDeviceSession(userID int64, label string, scopes []string, userAgent string) (raw string, *Session, error)` | method | Inserts a `kind='device'` session expiring at now+`DeviceSessionTTL`. Empty scopes → `ErrNoScopes`; a scope outside `[a-z0-9:._-]+` → error. |
| `(*Store).LookupSession`, `ExtendSession`, `ListSessions`, `DeleteSession`, `DeleteSessionByToken`, `DeleteUserSessions` | existing methods | Unchanged signatures; now carry and return `Kind`/`Label`/`Scopes`, and work for device rows. Revoking a device token = `DeleteSession(userID, id)` or `DeleteSessionByToken(raw)`. |
| `(*Store).CreateLinkChallenge(userID int64, pubkey string) (challenge string, expiresAt time.Time, error)` | method | 32 random bytes; returns them as 64 lowercase hex; stores only SHA-256 of the 32 raw bytes. |
| `(*Store).ConsumeLinkChallenge(userID int64, pubkey, challenge string) error` | method | Always deletes. `nil`, or `ErrChallengeMissing` / `ErrChallengeExpired` / `ErrChallengeMismatch`. |
| `(*Store).PruneLinkChallenges() (int64, error)` | method | Deletes expired challenges; called by the janitor. |
| `CompanionLink{Pubkey, UserID, Name, LinkedAt}` | type | One row of `companion_links`. |
| `(*Store).UpsertCompanionLink(userID int64, pubkey, name string) (*CompanionLink, prevOwner int64, error)` | method | `prevOwner` is the other user's id when the pubkey was transferred, else 0. Re-linking one's own companion updates the name and keeps `LinkedAt`. |
| `(*Store).ListCompanionLinks(userID int64) ([]CompanionLink, error)` | method | Newest link first. |
| `(*Store).GetCompanionLink(pubkey string) (*CompanionLink, error)` | method | `ErrNotFound` when unlinked. |
| `(*Store).DeleteCompanionLink(userID int64, pubkey string) error` | method | User-scoped; `ErrNotFound` when not linked to that user. |
| `(*Store).LinkedPubkeys() ([]string, error)` | method | Every linked pubkey, sorted (server-side use, e.g. admin and tests). |
| `ErrNoScopes`, `ErrBadPubkey`, `ErrChallengeMissing`, `ErrChallengeExpired`, `ErrChallengeMismatch` | vars | Sentinel errors. |

## Design constraints the implementer must not relax

- **The ingestor never imports `internal/users`** (AGENTS.md: it opens `users.db` `mode=ro` with raw SQL). `LinkedPubkeys` is for the server; the later ingestor work reads `SELECT pubkey FROM companion_links` directly. Do not make the ingestor depend on this package.
- **Migrations are forward-only.** Never edit v1–v5; append v6 only. The v6 SQL is the spec's *Data model* block verbatim, one statement per string.
- **A device token must never pass as a cookie session.** `LookupSession` returns the row whatever its kind; F2's `withUser` must reject `Kind == SessionKindDevice` on the cookie path. Nothing in F1 creates device sessions, so F1 alone exposes nothing.
- **Tokens and challenges are never logged**, and only their SHA-256 is stored.

## File Structure

| File | Change |
|---|---|
| `internal/users/schema.go` | Append migration v6. |
| `internal/users/schema_v6_test.go` | New: v5 → v6 upgrade test. |
| `internal/users/store.go` | Add `ErrNoScopes`, `ErrBadPubkey`, `ErrChallenge*`. |
| `internal/users/sessions.go` | `Session` gains `Kind`/`Label`/`Scopes`; private `createSession`; `CreateDeviceSession`; `HasScope`; `CleanLabel`; constants. |
| `internal/users/device_sessions_test.go` | New: device-session tests. |
| `internal/users/companions.go` | New: `NormalizePubkey` (Task 3), `CompanionLink` and its methods (Task 5). |
| `internal/users/challenges.go` | New: link challenges and their prune. |
| `internal/users/challenges_test.go` | New. |
| `internal/users/companions_test.go` | New. |
| `cmd/server/auth_service.go` | `prune()` calls `PruneLinkChallenges`. |
| `cmd/server/auth_janitor_challenges_test.go` | New: janitor test. |

All `internal/users` commands run from `C:\dev\corescope\CoreScope\internal\users`; `cmd/server` commands from `C:\dev\corescope\CoreScope\cmd\server`; git commands from the repo root.

---

### Task 1: Schema v6 migration

**Files:**
- Create: `internal/users/schema_v6_test.go`
- Modify: `internal/users/schema.go` (append to `migrations`)

- [ ] **Step 1: Write the failing test**

Create `internal/users/schema_v6_test.go`:

```go
package users

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// A users.db written by a v5 binary gains the companion-linking schema and
// keeps its sessions, which become web sessions with no label or scopes.
func TestMigrateV5DatabaseToV6(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{`CREATE TABLE schema_version (version INTEGER NOT NULL)`, `INSERT INTO schema_version (version) VALUES (5)`}
	for _, m := range migrations[:5] {
		stmts = append(stmts, m...)
	}
	stmts = append(stmts,
		`INSERT INTO users (email, display_name, password_hash, created_at) VALUES ('old@example.org', 'Old', 'x', 1)`,
		`INSERT INTO sessions (token_hash, user_id, csrf_token, created_at, expires_at, last_seen_at) VALUES ('h', 1, 'c', 1, 4102444800, 1)`)
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("build v5 db: %v", err)
		}
	}
	db.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open v5 db: %v", err)
	}
	defer st.Close()
	if len(migrations) < 6 {
		t.Fatal("migration v6 missing")
	}
	if v, err := st.SchemaVersion(); err != nil || v != len(migrations) {
		t.Fatalf("SchemaVersion = %d, %v; want %d", v, err, len(migrations))
	}

	var kind, label, scopes string
	if err := st.db.QueryRow(`SELECT kind, label, scopes FROM sessions WHERE token_hash = 'h'`).Scan(&kind, &label, &scopes); err != nil {
		t.Fatalf("v5 session lost: %v", err)
	}
	if kind != "web" || label != "" || scopes != "" {
		t.Fatalf("migrated session kind=%q label=%q scopes=%q", kind, label, scopes)
	}
	if _, err := st.db.Exec(`UPDATE sessions SET kind = 'api_key' WHERE token_hash = 'h'`); err == nil || !strings.Contains(err.Error(), "CHECK") {
		t.Fatalf("kind outside web/device accepted: %v", err)
	}
	if _, err := st.db.Exec(`UPDATE sessions SET kind = 'device' WHERE token_hash = 'h'`); err != nil {
		t.Fatalf("kind device rejected: %v", err)
	}

	pk := strings.Repeat("ab", 32)
	if _, err := st.db.Exec(`INSERT INTO companion_links (pubkey, user_id, linked_at) VALUES (?, 1, 1)`, pk); err != nil {
		t.Fatalf("companion_links: %v", err)
	}
	var name string
	if err := st.db.QueryRow(`SELECT name FROM companion_links WHERE pubkey = ?`, pk).Scan(&name); err != nil || name != "" {
		t.Fatalf("companion_links default name = %q, %v", name, err)
	}
	if _, err := st.db.Exec(`INSERT INTO companion_links (pubkey, user_id, linked_at) VALUES (?, 99, 1)`, strings.Repeat("cd", 32)); err == nil {
		t.Fatal("companion_links accepted an unknown user (foreign key off?)")
	}
	if _, err := st.db.Exec(`INSERT INTO link_challenges (challenge_hash, user_id, pubkey, expires_at) VALUES ('x', 1, ?, 1)`, pk); err != nil {
		t.Fatalf("link_challenges: %v", err)
	}
	var idx int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'companion_links_user'`).Scan(&idx); err != nil || idx != 1 {
		t.Fatalf("index companion_links_user = %d, %v", idx, err)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd internal/users && go test -run TestMigrateV5DatabaseToV6 .`
Expected: FAIL with `migration v6 missing`.

- [ ] **Step 3: Append migration v6**

In `internal/users/schema.go`, after the v5 entry (the one ending with the `notification_state` table) and before the closing `}` of `migrations`, add:

```go
	{ // v6: sub-project F, companion linking (device sessions, linked companions)
		`ALTER TABLE sessions ADD COLUMN kind TEXT NOT NULL DEFAULT 'web'
			CHECK (kind IN ('web','device'))`,
		`ALTER TABLE sessions ADD COLUMN label TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE sessions ADD COLUMN scopes TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE companion_links (
			pubkey TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			name TEXT NOT NULL DEFAULT '',
			linked_at INTEGER NOT NULL
		)`,
		`CREATE INDEX companion_links_user ON companion_links(user_id)`,
		`CREATE TABLE link_challenges (
			challenge_hash TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			pubkey TEXT NOT NULL,
			expires_at INTEGER NOT NULL
		)`,
	},
```

- [ ] **Step 4: Run the package tests**

Run: `cd internal/users && go test .`
Expected: PASS (including `TestMigrateV5DatabaseToV6`, `TestMigrateV4DatabaseToV5` and `TestOpenCreatesSchemaAndIsIdempotent`).

- [ ] **Step 5: Commit**

```bash
git add internal/users/schema.go internal/users/schema_v6_test.go
git commit -F - <<'EOF'
feat(users): schema v6 for companion linking

Sessions gain kind ('web'|'device'), label and scopes; new tables
companion_links (indexed by user) and link_challenges, both cascading
on user delete. Spec: docs/specs/2026-10-08-companion-linking-design.md.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 2: Device sessions

**Files:**
- Create: `internal/users/device_sessions_test.go`
- Modify: `internal/users/sessions.go`, `internal/users/store.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/users/device_sessions_test.go`:

```go
package users

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestDeviceSessionKindLabelScopes(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "dev@example.org", "Dev")
	_, web, err := st.CreateSession(u.ID, time.Hour, "browser")
	if err != nil {
		t.Fatal(err)
	}
	if web.Kind != SessionKindWeb || web.Label != "" || web.Scopes != nil || !web.HasScope("anything") {
		t.Fatalf("web session = %+v", web)
	}

	raw, dev, err := st.CreateDeviceSession(u.ID, "  Pixel\x07 8  ", []string{"rx", "account"}, "CoreDriveRX/1.0")
	if err != nil {
		t.Fatal(err)
	}
	if raw == "" || dev.Kind != SessionKindDevice || dev.Label != "Pixel 8" || dev.CSRFToken == "" ||
		!reflect.DeepEqual(dev.Scopes, []string{"rx", "account"}) || !dev.ExpiresAt.Equal(clk.Now().Add(DeviceSessionTTL)) {
		t.Fatalf("CreateDeviceSession = %+v", dev)
	}

	got, err := st.LookupSession(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != dev.ID || got.Kind != SessionKindDevice || got.Label != "Pixel 8" || got.UserAgent != "CoreDriveRX/1.0" ||
		!reflect.DeepEqual(got.Scopes, []string{"rx", "account"}) {
		t.Fatalf("LookupSession = %+v", got)
	}
	if !got.HasScope("rx") || !got.HasScope("account") || got.HasScope("admin") || got.HasScope("") {
		t.Fatalf("HasScope wrong for %v", got.Scopes)
	}

	list, err := st.ListSessions(u.ID)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListSessions = %+v, %v", list, err)
	}
	kinds := map[string]string{}
	for _, s := range list {
		kinds[s.Kind] = s.Label
	}
	if l, ok := kinds[SessionKindDevice]; !ok || l != "Pixel 8" {
		t.Fatalf("device row missing from list: %+v", list)
	}
	if l, ok := kinds[SessionKindWeb]; !ok || l != "" {
		t.Fatalf("web row missing from list: %+v", list)
	}
}

func TestDeviceSessionSlidingExpiry(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "slide@example.org", "Slide")
	raw, dev, err := st.CreateDeviceSession(u.ID, "Phone", []string{"rx"}, "")
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(DeviceSessionTTL - time.Hour)
	if err := st.ExtendSession(dev.ID, DeviceSessionTTL); err != nil {
		t.Fatal(err)
	}
	clk.Advance(DeviceSessionTTL - time.Hour) // well past the original expiry
	if _, err := st.LookupSession(raw); err != nil {
		t.Fatalf("extended device session expired early: %v", err)
	}
	clk.Advance(2 * time.Hour)
	if _, err := st.LookupSession(raw); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired device session err = %v", err)
	}
}

func TestDeviceSessionRevoke(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "rev@example.org", "Rev")
	other := mustCreate(t, st, "oth@example.org", "Oth")
	raw1, d1, _ := st.CreateDeviceSession(u.ID, "One", []string{"rx"}, "")
	raw2, _, _ := st.CreateDeviceSession(u.ID, "Two", []string{"rx"}, "")

	if err := st.DeleteSession(other.ID, d1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoking someone else's device = %v", err)
	}
	if err := st.DeleteSession(u.ID, d1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LookupSession(raw1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked device token still resolves: %v", err)
	}
	if err := st.DeleteSessionByToken(raw2); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LookupSession(raw2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("logged-out device token still resolves: %v", err)
	}
}

func TestDeviceSessionRejectsBadScopes(t *testing.T) {
	st, _ := newTestStore(t)
	u := mustCreate(t, st, "scope@example.org", "Scope")
	if _, _, err := st.CreateDeviceSession(u.ID, "x", nil, ""); !errors.Is(err, ErrNoScopes) {
		t.Fatalf("nil scopes err = %v", err)
	}
	for _, bad := range []string{"", "a,b", "Rx", "rx scope"} {
		if _, _, err := st.CreateDeviceSession(u.ID, "x", []string{bad}, ""); err == nil {
			t.Fatalf("scope %q accepted", bad)
		}
	}
	if list, _ := st.ListSessions(u.ID); len(list) != 0 {
		t.Fatalf("rejected device sessions were stored: %+v", list)
	}
}

func TestCleanLabel(t *testing.T) {
	cases := map[string]string{
		"  Car  ":      "Car",
		"a\tb\nc\x00d": "abcd",
		"bad\xffutf8":  "badutf8",
		"":             "",
	}
	for in, want := range cases {
		if got := CleanLabel(in); got != want {
			t.Errorf("CleanLabel(%q) = %q, want %q", in, got, want)
		}
	}
	long := CleanLabel(strings.Repeat("é", 70))
	if utf8.RuneCountInString(long) != 64 {
		t.Fatalf("CleanLabel cap = %d runes", utf8.RuneCountInString(long))
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd internal/users && go test -run 'TestDeviceSession|TestCleanLabel' .`
Expected: FAIL, build error: `undefined: SessionKindWeb`, `undefined: DeviceSessionTTL`, `st.CreateDeviceSession undefined`, `web.Kind undefined`, `undefined: ErrNoScopes`, `undefined: CleanLabel`.

- [ ] **Step 3: Add the error**

In `internal/users/store.go`, inside the `var ( … )` block after `ErrAccountChanged`, add:

```go
	// ErrNoScopes: a device session must name what it may do; an empty
	// scope list would mean full (web) access.
	ErrNoScopes = errors.New("users: a device session needs at least one scope")
```

- [ ] **Step 4: Implement in `sessions.go`**

Replace the `Session` type, `sessionCols`, `scanSession` and `CreateSession` in `internal/users/sessions.go` with the following, and add `"unicode"` to the imports (the file already imports `database/sql`, `errors`, `fmt`, `strings`, `time`):

```go
// Session kinds (sessions.kind).
const (
	SessionKindWeb    = "web"
	SessionKindDevice = "device"
)

// DeviceSessionTTL is a device token's lifetime, sliding on use: callers
// pass it to ExtendSession whenever the token is used.
const DeviceSessionTTL = 90 * 24 * time.Hour

// Session is one logged-in browser or device. The raw token exists only in
// the cookie (web) or on the device (bearer).
type Session struct {
	ID         int64
	UserID     int64
	CSRFToken  string // filled but unused for device sessions
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
	UserAgent  string
	Kind       string   // SessionKindWeb or SessionKindDevice
	Label      string   // device name; "" for web sessions
	Scopes     []string // nil = full access (web sessions)
}

// HasScope reports whether the session may use scope. Web sessions have
// full access; device sessions only their listed scopes.
func (s *Session) HasScope(scope string) bool {
	if s.Kind != SessionKindDevice {
		return true
	}
	for _, sc := range s.Scopes {
		if sc == scope {
			return true
		}
	}
	return false
}

const maxUserAgent = 200

const maxLabel = 64

// CleanLabel makes a user-supplied device or companion name safe to store:
// invalid UTF-8 and control characters removed, trimmed, at most 64 runes.
func CleanLabel(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxLabel {
		s = strings.TrimSpace(string(r[:maxLabel]))
	}
	return s
}

func validScope(sc string) bool {
	if sc == "" {
		return false
	}
	for _, r := range sc {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == ':' || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func splitScopes(v string) []string {
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
}

const sessionCols = `id, user_id, csrf_token, created_at, expires_at, last_seen_at, user_agent, kind, label, scopes`

func scanSession(row rowScanner) (*Session, error) {
	var s Session
	var created, expires, seen int64
	var scopes string
	if err := row.Scan(&s.ID, &s.UserID, &s.CSRFToken, &created, &expires, &seen, &s.UserAgent, &s.Kind, &s.Label, &scopes); err != nil {
		return nil, err
	}
	s.CreatedAt, s.ExpiresAt, s.LastSeenAt = fromUnix(created), fromUnix(expires), fromUnix(seen)
	s.Scopes = splitScopes(scopes)
	return &s, nil
}

// CreateSession starts a web session and returns the raw cookie token.
func (s *Store) CreateSession(userID int64, ttl time.Duration, userAgent string) (string, *Session, error) {
	return s.createSession(userID, ttl, userAgent, SessionKindWeb, "", nil)
}

// CreateDeviceSession issues a device (bearer) token for userID, limited to
// scopes and valid for DeviceSessionTTL. label is cleaned with CleanLabel.
func (s *Store) CreateDeviceSession(userID int64, label string, scopes []string, userAgent string) (string, *Session, error) {
	if len(scopes) == 0 {
		return "", nil, ErrNoScopes
	}
	for _, sc := range scopes {
		if !validScope(sc) {
			return "", nil, fmt.Errorf("users: invalid scope %q", sc)
		}
	}
	return s.createSession(userID, DeviceSessionTTL, userAgent, SessionKindDevice, CleanLabel(label), scopes)
}

func (s *Store) createSession(userID int64, ttl time.Duration, userAgent, kind, label string, scopes []string) (string, *Session, error) {
	raw, hash, err := NewToken()
	if err != nil {
		return "", nil, err
	}
	csrf, _, err := NewToken()
	if err != nil {
		return "", nil, err
	}
	if len(userAgent) > maxUserAgent {
		userAgent = strings.ToValidUTF8(userAgent[:maxUserAgent], "")
	}
	now := s.now()
	res, err := s.db.Exec(`INSERT INTO sessions (token_hash, user_id, csrf_token, created_at, expires_at, last_seen_at, user_agent, kind, label, scopes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, hash, userID, csrf, unix(now), unix(now.Add(ttl)), unix(now), userAgent,
		kind, label, strings.Join(scopes, ","))
	if err != nil {
		return "", nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return "", nil, err
	}
	return raw, &Session{ID: id, UserID: userID, CSRFToken: csrf, CreatedAt: fromUnix(unix(now)),
		ExpiresAt: fromUnix(unix(now.Add(ttl))), LastSeenAt: fromUnix(unix(now)), UserAgent: userAgent,
		Kind: kind, Label: label, Scopes: splitScopes(strings.Join(scopes, ","))}, nil
}
```

`LookupSession`, `ExtendSession`, `DeleteSessionByToken`, `DeleteSession`, `DeleteUserSessions`, `ListSessions` and `PruneExpiredSessions` stay as they are; they pick up the new columns through `sessionCols`/`scanSession`.

- [ ] **Step 5: Run the package tests**

Run: `cd internal/users && go test .`
Expected: PASS (new tests plus the existing `TestSessionLifecycle`, `TestDeleteSessions`, `TestPruneExpiredSessions`).

- [ ] **Step 6: Check the consumer still builds**

`Session` now holds a slice, so it is no longer comparable with `==`.
Run: `cd cmd/server && go build ./... && go vet ./...`
Expected: no output. If a `*a == *b` comparison of sessions fails to compile, compare `ID`s instead in that spot.

- [ ] **Step 7: Commit**

```bash
git add internal/users/sessions.go internal/users/store.go internal/users/device_sessions_test.go
git commit -F - <<'EOF'
feat(users): device sessions with kind, label and scopes

CreateDeviceSession issues a scoped bearer token in the sessions table
with a 90-day sliding expiry (DeviceSessionTTL). Lookup, listing and
revocation return kind, label and scopes; web sessions keep full access.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 3: Link challenges

**Files:**
- Create: `internal/users/companions.go`, `internal/users/challenges.go`, `internal/users/challenges_test.go`
- Modify: `internal/users/store.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/users/challenges_test.go`:

```go
package users

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestNormalizePubkey(t *testing.T) {
	pk := strings.Repeat("ab", 32)
	if got, err := NormalizePubkey("  " + strings.ToUpper(pk) + " "); err != nil || got != pk {
		t.Fatalf("NormalizePubkey = %q, %v", got, err)
	}
	for _, bad := range []string{"", pk[:62], pk + "00", strings.Repeat("zz", 32)} {
		if _, err := NormalizePubkey(bad); !errors.Is(err, ErrBadPubkey) {
			t.Fatalf("NormalizePubkey(%q) err = %v", bad, err)
		}
	}
}

func TestLinkChallengeLifecycle(t *testing.T) {
	st, clk := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "A")
	pk := strings.Repeat("ab", 32)
	ch, exp, err := st.CreateLinkChallenge(a.ID, strings.ToUpper(pk))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(ch)
	if err != nil || len(raw) != 32 || ch != strings.ToLower(ch) || !exp.Equal(clk.Now().Add(LinkChallengeTTL)) {
		t.Fatalf("CreateLinkChallenge = %q, %v, %v", ch, exp, err)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM link_challenges WHERE challenge_hash = ? AND pubkey = ? AND user_id = ?`,
		challengeHash(raw), pk, a.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("stored challenge rows = %d, %v (want SHA-256 of the raw bytes)", n, err)
	}
	if _, _, err := st.CreateLinkChallenge(a.ID, "nothex"); !errors.Is(err, ErrBadPubkey) {
		t.Fatalf("bad pubkey err = %v", err)
	}

	if err := st.ConsumeLinkChallenge(a.ID, pk, ch); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if err := st.ConsumeLinkChallenge(a.ID, pk, ch); !errors.Is(err, ErrChallengeMissing) {
		t.Fatalf("second consume err = %v, want single use", err)
	}
	if err := st.ConsumeLinkChallenge(a.ID, pk, "zz"); !errors.Is(err, ErrChallengeMissing) {
		t.Fatalf("malformed challenge err = %v", err)
	}
}

func TestLinkChallengeBindingAndExpiry(t *testing.T) {
	st, clk := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "A")
	b := mustCreate(t, st, "b@example.org", "B")
	pk := strings.Repeat("ab", 32)
	otherPk := strings.Repeat("cd", 32)

	ch, _, _ := st.CreateLinkChallenge(a.ID, pk)
	if err := st.ConsumeLinkChallenge(b.ID, pk, ch); !errors.Is(err, ErrChallengeMismatch) {
		t.Fatalf("other user err = %v", err)
	}
	if err := st.ConsumeLinkChallenge(a.ID, pk, ch); !errors.Is(err, ErrChallengeMissing) {
		t.Fatalf("mismatch did not consume: %v", err)
	}

	ch, _, _ = st.CreateLinkChallenge(a.ID, pk)
	if err := st.ConsumeLinkChallenge(a.ID, otherPk, ch); !errors.Is(err, ErrChallengeMismatch) {
		t.Fatalf("other pubkey err = %v", err)
	}

	ch, _, _ = st.CreateLinkChallenge(a.ID, pk)
	clk.Advance(LinkChallengeTTL)
	if err := st.ConsumeLinkChallenge(a.ID, pk, ch); !errors.Is(err, ErrChallengeExpired) {
		t.Fatalf("expired err = %v", err)
	}
	if err := st.ConsumeLinkChallenge(a.ID, pk, ch); !errors.Is(err, ErrChallengeMissing) {
		t.Fatalf("expired challenge not consumed: %v", err)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd internal/users && go test -run 'TestNormalizePubkey|TestLinkChallenge' .`
Expected: FAIL, build error: `undefined: NormalizePubkey`, `undefined: ErrBadPubkey`, `st.CreateLinkChallenge undefined`, `undefined: LinkChallengeTTL`, `undefined: challengeHash`, `undefined: ErrChallengeMissing`.

- [ ] **Step 3: Add the errors**

In `internal/users/store.go`, inside the `var ( … )` block after `ErrNoScopes`, add:

```go
	ErrBadPubkey = errors.New("users: pubkey must be 64 hex characters")
	// Link challenges (companion linking). A challenge is consumed by every
	// lookup, whichever of these it returns.
	ErrChallengeMissing  = errors.New("users: link challenge unknown or already used")
	ErrChallengeExpired  = errors.New("users: link challenge expired")
	ErrChallengeMismatch = errors.New("users: link challenge bound to another user or pubkey")
```

- [ ] **Step 4: Create `internal/users/companions.go`**

```go
package users

import (
	"encoding/hex"
	"strings"
)

// NormalizePubkey returns a companion's public key as 64 lowercase hex
// characters, or ErrBadPubkey.
func NormalizePubkey(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != 64 {
		return "", ErrBadPubkey
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", ErrBadPubkey
	}
	return s, nil
}
```

- [ ] **Step 5: Create `internal/users/challenges.go`**

```go
package users

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// LinkChallengeTTL is how long a companion-link challenge stays valid.
const LinkChallengeTTL = 5 * time.Minute

// challengeHash is the stored form of a challenge: SHA-256 of its 32 raw bytes.
func challengeHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// CreateLinkChallenge issues a single-use challenge bound to userID and
// pubkey. It returns the 32 random bytes as lowercase hex; only their hash
// is stored.
func (s *Store) CreateLinkChallenge(userID int64, pubkey string) (string, time.Time, error) {
	pk, err := NormalizePubkey(pubkey)
	if err != nil {
		return "", time.Time{}, err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("users: challenge: %w", err)
	}
	exp := unix(s.now().Add(LinkChallengeTTL))
	if _, err := s.db.Exec(`INSERT INTO link_challenges (challenge_hash, user_id, pubkey, expires_at) VALUES (?, ?, ?, ?)`,
		challengeHash(raw), userID, pk, exp); err != nil {
		return "", time.Time{}, fmt.Errorf("users: create link challenge: %w", err)
	}
	return hex.EncodeToString(raw), fromUnix(exp), nil
}

// ConsumeLinkChallenge checks a challenge returned by the client and deletes
// it in every case. It returns ErrChallengeMissing (unknown, malformed or
// already used), ErrChallengeExpired, or ErrChallengeMismatch (bound to
// another user or pubkey). One DELETE … RETURNING statement, so two
// concurrent consumers cannot both succeed.
func (s *Store) ConsumeLinkChallenge(userID int64, pubkey, challenge string) error {
	raw, err := hex.DecodeString(challenge)
	if err != nil || len(raw) != 32 {
		return ErrChallengeMissing
	}
	var owner, exp int64
	var pk string
	err = s.db.QueryRow(`DELETE FROM link_challenges WHERE challenge_hash = ? RETURNING user_id, pubkey, expires_at`,
		challengeHash(raw)).Scan(&owner, &pk, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrChallengeMissing
	}
	if err != nil {
		return fmt.Errorf("users: consume link challenge: %w", err)
	}
	if !s.now().Before(fromUnix(exp)) {
		return ErrChallengeExpired
	}
	want, perr := NormalizePubkey(pubkey)
	if owner != userID || perr != nil || pk != want {
		return ErrChallengeMismatch
	}
	return nil
}
```

- [ ] **Step 6: Run the package tests**

Run: `cd internal/users && go test .`
Expected: PASS. (If the second consume in `TestLinkChallengeLifecycle` reports nil, the driver did not complete the `RETURNING` delete on `QueryRow`; replace it with a `tx` that does `SELECT … FROM link_challenges WHERE challenge_hash = ?` then `DELETE … WHERE challenge_hash = ?` and commits, using `tx` only.)

- [ ] **Step 7: Commit**

```bash
git add internal/users/store.go internal/users/companions.go internal/users/challenges.go internal/users/challenges_test.go
git commit -F - <<'EOF'
feat(users): single-use companion link challenges

CreateLinkChallenge stores SHA-256 of 32 random bytes bound to a user
and pubkey for 5 minutes. ConsumeLinkChallenge always deletes and tells
missing, expired and mismatched challenges apart.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 4: Prune expired challenges in the janitor

**Files:**
- Modify: `internal/users/challenges.go`, `internal/users/challenges_test.go`, `cmd/server/auth_service.go`
- Create: `cmd/server/auth_janitor_challenges_test.go`

- [ ] **Step 1: Write the failing store test**

Append to `internal/users/challenges_test.go`:

```go
func TestPruneLinkChallenges(t *testing.T) {
	st, clk := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "A")
	pk := strings.Repeat("ab", 32)
	old, _, _ := st.CreateLinkChallenge(a.ID, pk)
	clk.Advance(3 * time.Minute)
	fresh, _, _ := st.CreateLinkChallenge(a.ID, pk)
	clk.Advance(3 * time.Minute) // old expired, fresh has 2 minutes left

	n, err := st.PruneLinkChallenges()
	if err != nil || n != 1 {
		t.Fatalf("PruneLinkChallenges = %d, %v", n, err)
	}
	if err := st.ConsumeLinkChallenge(a.ID, pk, old); !errors.Is(err, ErrChallengeMissing) {
		t.Fatalf("pruned challenge err = %v", err)
	}
	if err := st.ConsumeLinkChallenge(a.ID, pk, fresh); err != nil {
		t.Fatalf("live challenge pruned: %v", err)
	}
}
```

and add `"time"` to that file's imports.

- [ ] **Step 2: Write the failing janitor test**

Create `cmd/server/auth_janitor_challenges_test.go`:

```go
package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/users"
)

func TestJanitorPrunesExpiredLinkChallenges(t *testing.T) {
	f := newAuthFixture(t)
	dave := f.registerAndActivate(t, "dave@example.org", "Dave", pw)
	pk := strings.Repeat("cd", 32)
	f.st.SetClock(func() time.Time { return time.Now().Add(-10 * time.Minute) })
	ch, _, err := f.st.CreateLinkChallenge(dave.me.ID, pk)
	if err != nil {
		t.Fatal(err)
	}
	f.st.SetClock(time.Now)
	f.srv.auth.prune()
	// Pruned → missing. Without the janitor it would still be there, expired.
	if err := f.st.ConsumeLinkChallenge(dave.me.ID, pk, ch); !errors.Is(err, users.ErrChallengeMissing) {
		t.Fatalf("expired challenge survived the janitor: %v", err)
	}
}
```

- [ ] **Step 3: Run them and watch them fail**

Run: `cd internal/users && go test -run TestPruneLinkChallenges .`
Expected: FAIL, build error `st.PruneLinkChallenges undefined`.

Run: `cd cmd/server && go test -run TestJanitorPrunesExpiredLinkChallenges .`
Expected: FAIL with `expired challenge survived the janitor: users: link challenge expired`.

- [ ] **Step 4: Implement**

Append to `internal/users/challenges.go`:

```go
// PruneLinkChallenges deletes challenges that have expired.
func (s *Store) PruneLinkChallenges() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM link_challenges WHERE expires_at <= ?`, unix(s.now()))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
```

In `cmd/server/auth_service.go`, in `func (a *authService) prune()`, directly after the `PruneExpiredSessions` block, add:

```go
	if _, err := a.st.PruneLinkChallenges(); err != nil {
		log.Printf("[users] prune link challenges: %v", err)
	}
```

- [ ] **Step 5: Run both**

Run: `cd internal/users && go test .`
Expected: PASS.
Run: `cd cmd/server && go test -run 'TestJanitor' .`
Expected: PASS (`TestJanitorPrunesExpiredLinkChallenges` and `TestJanitorPrunesOldLoginAudit`).

- [ ] **Step 6: Commit**

```bash
git add internal/users/challenges.go internal/users/challenges_test.go cmd/server/auth_service.go cmd/server/auth_janitor_challenges_test.go
git commit -F - <<'EOF'
feat(users): janitor prunes expired link challenges

PruneLinkChallenges runs with the other expired-row pruning in the
auth janitor.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 5: Companion links

**Files:**
- Modify: `internal/users/companions.go`
- Create: `internal/users/companions_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/users/companions_test.go`:

```go
package users

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCompanionLinkUpsertAndTransfer(t *testing.T) {
	st, clk := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "A")
	b := mustCreate(t, st, "b@example.org", "B")
	pk := strings.Repeat("ab", 32)

	l, prev, err := st.UpsertCompanionLink(a.ID, strings.ToUpper(pk), "  Car\n ")
	if err != nil || prev != 0 {
		t.Fatalf("first link: prev=%d err=%v", prev, err)
	}
	first := clk.Now()
	if l.Pubkey != pk || l.UserID != a.ID || l.Name != "Car" || !l.LinkedAt.Equal(first) {
		t.Fatalf("first link = %+v", l)
	}

	clk.Advance(time.Hour)
	l, prev, err = st.UpsertCompanionLink(a.ID, pk, "Car 2")
	if err != nil || prev != 0 || l.Name != "Car 2" || !l.LinkedAt.Equal(first) {
		t.Fatalf("re-link by owner = %+v, prev=%d, err=%v", l, prev, err)
	}
	if got, _ := st.GetCompanionLink(pk); got == nil || got.Name != "Car 2" || !got.LinkedAt.Equal(first) {
		t.Fatalf("after re-link Get = %+v", got)
	}

	clk.Advance(time.Hour)
	l, prev, err = st.UpsertCompanionLink(b.ID, pk, "Bike")
	if err != nil || prev != a.ID {
		t.Fatalf("transfer: prev=%d (want %d) err=%v", prev, a.ID, err)
	}
	if l.UserID != b.ID || !l.LinkedAt.Equal(clk.Now()) {
		t.Fatalf("transferred link = %+v", l)
	}
	got, err := st.GetCompanionLink(pk)
	if err != nil || got.UserID != b.ID || got.Name != "Bike" {
		t.Fatalf("Get after transfer = %+v, %v", got, err)
	}
	if list, _ := st.ListCompanionLinks(a.ID); len(list) != 0 {
		t.Fatalf("previous owner still lists it: %+v", list)
	}
	if list, _ := st.ListCompanionLinks(b.ID); len(list) != 1 || list[0].Pubkey != pk {
		t.Fatalf("new owner list = %+v", list)
	}
	if _, _, err := st.UpsertCompanionLink(a.ID, "short", "x"); !errors.Is(err, ErrBadPubkey) {
		t.Fatalf("bad pubkey err = %v", err)
	}
}

func TestCompanionLinkListDeleteAndAll(t *testing.T) {
	st, clk := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "A")
	b := mustCreate(t, st, "b@example.org", "B")
	pk1, pk2, pk3 := strings.Repeat("03", 32), strings.Repeat("01", 32), strings.Repeat("02", 32)
	st.UpsertCompanionLink(a.ID, pk1, "one")
	clk.Advance(time.Minute)
	st.UpsertCompanionLink(a.ID, pk2, "two")
	st.UpsertCompanionLink(b.ID, pk3, "three")

	list, err := st.ListCompanionLinks(a.ID)
	if err != nil || len(list) != 2 || list[0].Pubkey != pk2 || list[1].Pubkey != pk1 {
		t.Fatalf("ListCompanionLinks (newest first) = %+v, %v", list, err)
	}
	all, err := st.LinkedPubkeys()
	if err != nil || !reflect.DeepEqual(all, []string{pk2, pk3, pk1}) {
		t.Fatalf("LinkedPubkeys = %v, %v", all, err)
	}

	if err := st.DeleteCompanionLink(b.ID, pk1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting someone else's link = %v", err)
	}
	if err := st.DeleteCompanionLink(a.ID, strings.ToUpper(pk1)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetCompanionLink(pk1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete err = %v", err)
	}
	if err := st.DeleteCompanionLink(a.ID, pk1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err = %v", err)
	}
	if _, err := st.GetCompanionLink("nothex"); !errors.Is(err, ErrBadPubkey) {
		t.Fatalf("Get bad pubkey err = %v", err)
	}
}

// Deleting a user removes their links, challenges and device tokens.
func TestCompanionDataCascadesOnUserDelete(t *testing.T) {
	st, _ := newTestStore(t)
	a := mustCreate(t, st, "a@example.org", "A")
	b := mustCreate(t, st, "b@example.org", "B")
	pkA, pkB := strings.Repeat("aa", 32), strings.Repeat("bb", 32)
	st.UpsertCompanionLink(a.ID, pkA, "")
	st.UpsertCompanionLink(b.ID, pkB, "")
	if _, _, err := st.CreateLinkChallenge(a.ID, pkA); err != nil {
		t.Fatal(err)
	}
	raw, _, err := st.CreateDeviceSession(a.ID, "Phone", []string{"rx"}, "")
	if err != nil {
		t.Fatal(err)
	}

	if err := st.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetCompanionLink(pkA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("link survived user delete: %v", err)
	}
	if all, _ := st.LinkedPubkeys(); !reflect.DeepEqual(all, []string{pkB}) {
		t.Fatalf("LinkedPubkeys after delete = %v", all)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM link_challenges WHERE user_id = ?`, a.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("challenges after user delete = %d, %v", n, err)
	}
	if _, err := st.LookupSession(raw); !errors.Is(err, ErrNotFound) {
		t.Fatalf("device token survived user delete: %v", err)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd internal/users && go test -run 'TestCompanion' .`
Expected: FAIL, build error: `st.UpsertCompanionLink undefined`, `st.GetCompanionLink undefined`, `st.ListCompanionLinks undefined`, `st.LinkedPubkeys undefined`, `st.DeleteCompanionLink undefined`.

- [ ] **Step 3: Implement**

Replace `internal/users/companions.go` with:

```go
package users

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// NormalizePubkey returns a companion's public key as 64 lowercase hex
// characters, or ErrBadPubkey.
func NormalizePubkey(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != 64 {
		return "", ErrBadPubkey
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", ErrBadPubkey
	}
	return s, nil
}

// CompanionLink ties a companion's pubkey to the user who proved they hold
// its key. A pubkey has at most one owner.
type CompanionLink struct {
	Pubkey   string
	UserID   int64
	Name     string
	LinkedAt time.Time
}

const companionCols = `pubkey, user_id, name, linked_at`

func scanCompanion(row rowScanner) (*CompanionLink, error) {
	var l CompanionLink
	var at int64
	if err := row.Scan(&l.Pubkey, &l.UserID, &l.Name, &at); err != nil {
		return nil, err
	}
	l.LinkedAt = fromUnix(at)
	return &l, nil
}

// UpsertCompanionLink links pubkey to userID; the newest proof wins. When
// the pubkey belonged to another user it moves to userID with a fresh
// LinkedAt, and prevOwner is that user's id. Otherwise prevOwner is 0; a
// re-link by the same owner only updates the name. name is cleaned with
// CleanLabel. The caller must have consumed a valid challenge and verified
// the signature first.
func (s *Store) UpsertCompanionLink(userID int64, pubkey, name string) (*CompanionLink, int64, error) {
	pk, err := NormalizePubkey(pubkey)
	if err != nil {
		return nil, 0, err
	}
	name = CleanLabel(name)
	tx, err := s.db.Begin()
	if err != nil {
		return nil, 0, err
	}
	var prev, linkedAt int64
	err = tx.QueryRow(`SELECT user_id, linked_at FROM companion_links WHERE pubkey = ?`, pk).Scan(&prev, &linkedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		prev, linkedAt = 0, unix(s.now())
	case err != nil:
		tx.Rollback()
		return nil, 0, fmt.Errorf("users: read companion link: %w", err)
	case prev != userID:
		linkedAt = unix(s.now())
	}
	if _, err := tx.Exec(`INSERT INTO companion_links (pubkey, user_id, name, linked_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(pubkey) DO UPDATE SET user_id = excluded.user_id, name = excluded.name, linked_at = excluded.linked_at`,
		pk, userID, name, linkedAt); err != nil {
		tx.Rollback()
		return nil, 0, fmt.Errorf("users: upsert companion link: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, fmt.Errorf("users: upsert companion link: %w", err)
	}
	if prev == userID {
		prev = 0
	}
	return &CompanionLink{Pubkey: pk, UserID: userID, Name: name, LinkedAt: fromUnix(linkedAt)}, prev, nil
}

// ListCompanionLinks returns a user's linked companions, newest first.
func (s *Store) ListCompanionLinks(userID int64) ([]CompanionLink, error) {
	rows, err := s.db.Query(`SELECT `+companionCols+` FROM companion_links WHERE user_id = ? ORDER BY linked_at DESC, pubkey`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CompanionLink
	for rows.Next() {
		l, err := scanCompanion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

// GetCompanionLink returns the link for pubkey, or ErrNotFound.
func (s *Store) GetCompanionLink(pubkey string) (*CompanionLink, error) {
	pk, err := NormalizePubkey(pubkey)
	if err != nil {
		return nil, err
	}
	l, err := scanCompanion(s.db.QueryRow(`SELECT `+companionCols+` FROM companion_links WHERE pubkey = ?`, pk))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("users: get companion link: %w", err)
	}
	return l, nil
}

// DeleteCompanionLink unlinks pubkey, only if it is linked to userID.
func (s *Store) DeleteCompanionLink(userID int64, pubkey string) error {
	pk, err := NormalizePubkey(pubkey)
	if err != nil {
		return err
	}
	return expectOne(s.db.Exec(`DELETE FROM companion_links WHERE pubkey = ? AND user_id = ?`, pk, userID))
}

// LinkedPubkeys returns every linked pubkey, sorted. (The ingestor does not
// call this: it reads users.db read-only with raw SQL.)
func (s *Store) LinkedPubkeys() ([]string, error) {
	rows, err := s.db.Query(`SELECT pubkey FROM companion_links ORDER BY pubkey`)
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
```

- [ ] **Step 4: Run the package tests, format and vet**

Run: `cd internal/users && go test . && gofmt -l . && go vet .`
Expected: `ok`, then no output from `gofmt -l` or `go vet`.
Run: `cd cmd/server && go build ./... && go vet ./... && go test -run 'TestJanitor|TestSession|TestLogin' .`
Expected: no build/vet output; tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/users/companions.go internal/users/companions_test.go
git commit -F - <<'EOF'
feat(users): companion links with transfer detection

UpsertCompanionLink reports the previous owner when a pubkey changes
hands; list, get, user-scoped delete and LinkedPubkeys round it out.
Links, challenges and device tokens cascade on user delete.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF
```

---

## Self-review against the spec

- *Data model*: three `ALTER TABLE sessions` columns with the `kind` CHECK and defaults, `companion_links` with `ON DELETE CASCADE` and index `companion_links_user`, `link_challenges` with `ON DELETE CASCADE`: Task 1, verbatim.
- Device tokens use the session token format (`NewToken`, SHA-256 stored), `csrf_token` filled: Task 2 (`createSession` is the one insert path).
- 90-day expiry with its own constant, sliding: `DeviceSessionTTL` + `ExtendSession`, Task 2.
- `GET /api/account/sessions` needs `kind` and `label`: `ListSessions` returns them, Task 2.
- `deviceName` trimmed, control characters stripped, capped at 64: `CleanLabel`, Task 2.
- Challenge 32 random bytes as hex, 5 minutes, single use, bound to user and pubkey, consumed in every case, missing/expired/mismatch distinct (F2 maps all three to 410): Task 3.
- Janitor prunes expired challenges with the other expired rows: Task 4.
- Upsert, transfer with previous owner (for F2's audit rows and mail), list, get, delete: Task 5.
- *Testing → internal/users*: v6 migration (Task 1), kind/label/scopes and sliding expiry (Task 2), challenge lifecycle (Tasks 3–4), upsert and transfer, cascade on user delete (Task 5).
- Not in F1 (F2 and later): HTTP routes, bearer auth in `withUser` (including rejecting device sessions on the cookie path), signature verification, `meshcore-my-nodes` merge, audit rows, mail, CORS, `rx-coverage?mine=1`, the ingestor's linked-only filter.
