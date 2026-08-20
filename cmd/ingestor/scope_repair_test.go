package main

import (
	"database/sql"
	"encoding/hex"
	"testing"
)

// scopeRepairTestKeys returns the same two region keys TestMatchScopeAmbiguous
// (main_test.go) uses: "#test" and "#collide" are constructed so that, for
// payloadType=5 payload="hello", both derive code1 2AB5 — a genuine,
// deterministic multi-key collision, not a hoped-for chance event.
func scopeRepairTestKeys(t *testing.T) map[string][]byte {
	t.Helper()
	testKey, err := hex.DecodeString("9cd8fcf22a47333b591d96a2b848b73f")
	if err != nil {
		t.Fatal(err)
	}
	collideKey, err := hex.DecodeString("66e51699772a5a52628e8b7686c7d8fd")
	if err != nil {
		t.Fatal(err)
	}
	return map[string][]byte{"#test": testKey, "#collide": collideKey}
}

// insertScopeRepairFixtureRow writes one transmissions row via the real
// InsertTransmission path, with an explicitly chosen (possibly
// pre-#1609-buggy) scope_name — bypassing matchScope entirely so the test
// controls exactly what "was stored historically" independent of what the
// current, fixed matchScope would compute.
func insertScopeRepairFixtureRow(t *testing.T, store *Store, rawHex, storedScope string, isTransportScoped bool) {
	t.Helper()
	pd := &PacketData{
		RawHex:            rawHex,
		Timestamp:         "2024-01-01T00:00:00Z",
		Hash:              ComputeContentHash(rawHex),
		RouteType:         0,
		PayloadType:       5,
		PayloadVersion:    0,
		DecodedJSON:       "{}",
		ScopeName:         storedScope,
		IsTransportScoped: isTransportScoped,
	}
	if _, err := store.InsertTransmission(pd); err != nil {
		t.Fatalf("insert fixture row (raw_hex=%s): %v", rawHex, err)
	}
}

// newScopeRepairFixture builds a fresh SQLite-backed Store with five rows
// covering the cases scope-repair must tell apart:
//
//	A: transport-scoped, uniquely matched, stored name already correct.
//	B: transport-scoped, two region keys collide on the same code1 — the
//	   #1609 bug's signature. Stored name simulates the old first-match
//	   result; the fix re-derives "" (ambiguous/unmatched).
//	C: raw_hex too short to decode — must be counted and skipped, never
//	   touched, regardless of what garbage is stored in scope_name.
//	D: not transport-scoped (FLOOD route, no transport codes) — scope_name
//	   is SQL NULL and must stay NULL, never become "".
//	E: transport-scoped, previously named, but re-derives to ZERO matching
//	   region keys (not two). This is config drift, not the #1609 bug, and
//	   must be reported as unexpected rather than silently corrected to "".
func newScopeRepairFixture(t *testing.T) *Store {
	t.Helper()
	store := newTestStore(t)

	insertScopeRepairFixtureRow(t, store, "14D874000000776F726C64", "#test", true)
	insertScopeRepairFixtureRow(t, store, "142AB500000068656C6C6F", "#test", true)
	insertScopeRepairFixtureRow(t, store, "14", "#belgium", true)
	insertScopeRepairFixtureRow(t, store, "15006869", "", false)
	insertScopeRepairFixtureRow(t, store, "1499990000006162636465", "#ghost", true)

	return store
}

// scopeNameByRawHex reads the stored scope_name for a fixture row, keeping
// SQL NULL (ok=false) distinct from the empty string (ok=true, value="").
func scopeNameByRawHex(t *testing.T, store *Store, rawHex string) (value string, ok bool) {
	t.Helper()
	var ns sql.NullString
	err := store.db.QueryRow(`SELECT scope_name FROM transmissions WHERE raw_hex = ?`, rawHex).Scan(&ns)
	if err != nil {
		t.Fatalf("query scope_name for raw_hex=%s: %v", rawHex, err)
	}
	return ns.String, ns.Valid
}

const (
	fixtureRawA = "14D874000000776F726C64"
	fixtureRawB = "142AB500000068656C6C6F"
	fixtureRawC = "14"
	fixtureRawD = "15006869"
	fixtureRawE = "1499990000006162636465"
)

func assertScopeName(t *testing.T, store *Store, rawHex, wantValue string, wantOK bool) {
	t.Helper()
	value, ok := scopeNameByRawHex(t, store, rawHex)
	if ok != wantOK || value != wantValue {
		t.Errorf("scope_name for raw_hex=%s = (%q, valid=%v), want (%q, valid=%v)", rawHex, value, ok, wantValue, wantOK)
	}
}

// TestScopeRepairDryRun covers the "dry-run by default, touches nothing"
// requirement: it reports exactly the expected classification for all five
// fixture rows and leaves every one of them exactly as stored.
func TestScopeRepairDryRun(t *testing.T) {
	store := newScopeRepairFixture(t)
	regionKeys := scopeRepairTestKeys(t)

	report, err := repairScopeNames(store.db, regionKeys, false)
	if err != nil {
		t.Fatalf("repairScopeNames: %v", err)
	}

	if report.Applied {
		t.Error("Applied = true, want false for a dry run")
	}
	if report.ScannedNotNull != 4 {
		t.Errorf("ScannedNotNull = %d, want 4 (rows A, B, C, E — D is NULL and out of scope)", report.ScannedNotNull)
	}
	if report.DecodeFailed != 1 {
		t.Errorf("DecodeFailed = %d, want 1 (row C)", report.DecodeFailed)
	}
	if report.Unchanged != 1 {
		t.Errorf("Unchanged = %d, want 1 (row A)", report.Unchanged)
	}
	if report.CorrectedTotal != 1 {
		t.Errorf("CorrectedTotal = %d, want 1 (row B)", report.CorrectedTotal)
	}
	if got := report.CorrectedByOldName["#test"]; got != 1 {
		t.Errorf(`CorrectedByOldName["#test"] = %d, want 1`, got)
	}
	if len(report.Unexpected) != 1 {
		t.Fatalf("len(Unexpected) = %d, want 1 (row E)", len(report.Unexpected))
	}
	if u := report.Unexpected[0]; u.Old.Name != "#ghost" || u.New != (scopeState{Valid: true, Name: ""}) {
		t.Errorf("Unexpected[0] = %+v, want Old=#ghost New=(valid,\"\")", u)
	}

	// Dry run must touch nothing.
	assertScopeName(t, store, fixtureRawA, "#test", true)
	assertScopeName(t, store, fixtureRawB, "#test", true)
	assertScopeName(t, store, fixtureRawC, "#belgium", true)
	assertScopeName(t, store, fixtureRawD, "", false)
	assertScopeName(t, store, fixtureRawE, "#ghost", true)
}

// TestScopeRepairApply covers --apply: it corrects exactly row B (the
// genuine ambiguous match), leaves every other row untouched, and a second
// --apply run is a complete no-op — the idempotence requirement.
func TestScopeRepairApply(t *testing.T) {
	store := newScopeRepairFixture(t)
	regionKeys := scopeRepairTestKeys(t)

	report1, err := repairScopeNames(store.db, regionKeys, true)
	if err != nil {
		t.Fatalf("repairScopeNames (apply): %v", err)
	}
	if !report1.Applied {
		t.Error("Applied = false, want true")
	}
	if report1.CorrectedTotal != 1 {
		t.Fatalf("first apply: CorrectedTotal = %d, want 1", report1.CorrectedTotal)
	}

	assertScopeName(t, store, fixtureRawA, "#test", true)    // untouched: already correct
	assertScopeName(t, store, fixtureRawB, "", true)         // corrected: ambiguous -> unmatched
	assertScopeName(t, store, fixtureRawC, "#belgium", true) // untouched: undecodable
	assertScopeName(t, store, fixtureRawD, "", false)        // untouched: not transport-scoped, stays NULL
	assertScopeName(t, store, fixtureRawE, "#ghost", true)   // untouched: unexpected, not applied

	report2, err := repairScopeNames(store.db, regionKeys, true)
	if err != nil {
		t.Fatalf("repairScopeNames (second apply): %v", err)
	}
	if report2.CorrectedTotal != 0 {
		t.Errorf("second apply: CorrectedTotal = %d, want 0 (idempotent)", report2.CorrectedTotal)
	}
	if report2.Unchanged != 2 {
		t.Errorf("second apply: Unchanged = %d, want 2 (rows A and now-corrected B)", report2.Unchanged)
	}
	if report2.DecodeFailed != 1 {
		t.Errorf("second apply: DecodeFailed = %d, want 1 (row C)", report2.DecodeFailed)
	}
	if len(report2.Unexpected) != 1 {
		t.Errorf("second apply: len(Unexpected) = %d, want 1 (row E)", len(report2.Unexpected))
	}

	// State after the second apply must be identical to after the first.
	assertScopeName(t, store, fixtureRawA, "#test", true)
	assertScopeName(t, store, fixtureRawB, "", true)
	assertScopeName(t, store, fixtureRawC, "#belgium", true)
	assertScopeName(t, store, fixtureRawD, "", false)
	assertScopeName(t, store, fixtureRawE, "#ghost", true)
}
