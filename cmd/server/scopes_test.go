package main

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

// setupScopeConformanceDB builds an in-memory transmissions/observations pair
// carrying exactly the columns ScopeConformance reads: code1/scope_name/
// first_seen/route_type on transmissions, path_json on observations. Mirrors
// the live ingestor schema (cmd/ingestor/db.go) rather than a convenient
// fiction, so the join behaves the way it does against a real database.
func setupScopeConformanceDB(t *testing.T) *DB {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	schema := `
		CREATE TABLE transmissions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			raw_hex TEXT NOT NULL,
			hash TEXT NOT NULL UNIQUE,
			first_seen TEXT NOT NULL,
			route_type INTEGER,
			payload_type INTEGER,
			code1 TEXT,
			code2 TEXT,
			scope_name TEXT
		);
		CREATE TABLE observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			transmission_id INTEGER NOT NULL REFERENCES transmissions(id),
			path_json TEXT,
			timestamp INTEGER NOT NULL
		);
	`
	if _, err := conn.Exec(schema); err != nil {
		t.Fatal(err)
	}
	return &DB{conn: conn}
}

// newScopeTestStore wires a *PacketStore to a freshly seeded scope-conformance
// schema. ScopeConformance is a pure SQL read against s.db, so no other
// PacketStore field needs to be populated.
func newScopeTestStore(t *testing.T) *PacketStore {
	t.Helper()
	db := setupScopeConformanceDB(t)
	return newTestStoreWithDB(t, db, &Config{})
}

// scopeSeed carries the code1/scope_name pair for one of the three states a
// seeded transmission can be in. scopeName mirrors scopeNameForDB's own
// encoding: nil means the packet carried no scope at all, a non-nil pointer
// to an empty string means transport-scoped but unmatched, and a non-nil
// pointer to a name means matched.
type scopeSeed struct {
	code1     string
	scopeName *string
}

func scopeMatched(name string) scopeSeed {
	n := name
	return scopeSeed{code1: "1234", scopeName: &n}
}

func scopeUnmatched() scopeSeed {
	empty := ""
	return scopeSeed{code1: "1234", scopeName: &empty}
}

func scopeUnscoped() scopeSeed {
	return scopeSeed{code1: "0000", scopeName: nil}
}

var scopeSeedCounter int

// seedTransmissionRoute inserts one transmission plus a single observation
// attributing it to forwarder, built the way the ingestor would build it: the
// path_json hop is uppercase (packetpath.DecodePathFromRawHex does
// strings.ToUpper on every hop), so the seed exercises the same case the
// live join has to cope with rather than a lowercase convenience fiction.
func seedTransmissionRoute(t *testing.T, s *PacketStore, forwarder string, seed scopeSeed, routeType int) {
	t.Helper()
	scopeSeedCounter++
	hash := fmt.Sprintf("scopehash%d", scopeSeedCounter)
	firstSeen := "2026-01-15T12:00:00Z"

	res, err := s.db.conn.Exec(
		`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, code1, code2, scope_name)
		 VALUES ('AA', ?, ?, ?, 1, ?, '00', ?)`,
		hash, firstSeen, routeType, seed.code1, seed.scopeName,
	)
	if err != nil {
		t.Fatalf("seed transmission: %v", err)
	}
	txID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("seed transmission id: %v", err)
	}

	pathJSON := fmt.Sprintf(`["%s"]`, strings.ToUpper(forwarder))
	if _, err := s.db.conn.Exec(
		`INSERT INTO observations (transmission_id, path_json, timestamp) VALUES (?, ?, ?)`,
		txID, pathJSON, time.Now().Unix(),
	); err != nil {
		t.Fatalf("seed observation: %v", err)
	}
}

// seedTransmission seeds a FLOOD packet (route_type=1) — path[last] is the
// actual transmitter, so forwarder is attributable.
func seedTransmission(t *testing.T, s *PacketStore, forwarder string, seed scopeSeed) {
	t.Helper()
	seedTransmissionRoute(t, s, forwarder, seed, RouteFlood)
}

// seedDirectTransmission seeds a DIRECT packet (route_type=2) — path[last] is
// the route's far end, never the transmitter, so forwarder must NOT be
// attributed even though it appears in path_json.
func seedDirectTransmission(t *testing.T, s *PacketStore, forwarder string, seed scopeSeed) {
	t.Helper()
	seedTransmissionRoute(t, s, forwarder, seed, RouteDirect)
}

func TestScopeConformanceKeepsThreeStatesDistinct(t *testing.T) {
	s := newScopeTestStore(t)
	// Same forwarder on all three, so only the scope state differs.
	seedTransmission(t, s, "1a2b", scopeMatched("be-van")) // scope_name = matched name
	seedTransmission(t, s, "1a2b", scopeUnmatched())       // code1 != 0000, no key -> scope_name = empty string
	seedTransmission(t, s, "1a2b", scopeUnscoped())        // code1 == 0000 -> scope_name = NULL

	got, err := s.ScopeConformance("1a2b", "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Observed) != 1 || got.Observed[0].Scope != "be-van" {
		t.Errorf("observed = %+v, want exactly the matched scope", got.Observed)
	}
	if got.Unmatched != 1 {
		t.Errorf("unmatched = %d, want 1 — a scoped packet we hold no key for is information, not absence", got.Unmatched)
	}
	if got.Unscoped != 1 {
		t.Errorf("unscoped = %d, want 1", got.Unscoped)
	}
}

func TestScopeConformanceNormalisesPubkeyCase(t *testing.T) {
	s := newScopeTestStore(t)
	seedTransmission(t, s, "1a2b", scopeMatched("be"))
	lower, err := s.ScopeConformance("1a2b", "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	upper, err := s.ScopeConformance("1A2B", "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(upper.Observed) != len(lower.Observed) {
		t.Errorf("uppercase pubkey returned %d rows, lowercase %d — case must not silently empty the result",
			len(upper.Observed), len(lower.Observed))
	}
	if len(upper.Observed) != 1 {
		t.Fatalf("want 1 observed scope, got %d", len(upper.Observed))
	}
}

func TestScopeConformanceEmptyIsNotAnError(t *testing.T) {
	s := newScopeTestStore(t)
	got, err := s.ScopeConformance("deadbeef", "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("a repeater we never heard is a valid question: %v", err)
	}
	if got == nil || len(got.Observed) != 0 || got.Unmatched != 0 || got.Unscoped != 0 {
		t.Errorf("want an empty result, got %+v", got)
	}
}

func TestScopeConformanceIgnoresDirectRoutes(t *testing.T) {
	s := newScopeTestStore(t)
	// A DIRECT packet's path[last] is the route's far end, never the transmitter.
	// Attributing it would credit a scope to a node that never forwarded it.
	seedDirectTransmission(t, s, "1a2b", scopeMatched("be-van"))
	got, err := s.ScopeConformance("1a2b", "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Observed) != 0 {
		t.Errorf("a DIRECT route must not attribute a forwarder, got %+v", got.Observed)
	}
}

// TestScopeConformanceRouteMix verifies the route-type mix counts only the
// route types this node was actually observed forwarding (FLOOD types),
// using the named RouteTransportFlood/RouteFlood constants.
func TestScopeConformanceRouteMix(t *testing.T) {
	s := newScopeTestStore(t)
	seedTransmissionRoute(t, s, "1a2b", scopeMatched("be"), RouteTransportFlood)
	seedTransmissionRoute(t, s, "1a2b", scopeMatched("be"), RouteFlood)

	got, err := s.ScopeConformance("1a2b", "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if got.Routes.TransportFlood != 1 {
		t.Errorf("Routes.TransportFlood = %d, want 1", got.Routes.TransportFlood)
	}
	if got.Routes.Flood != 1 {
		t.Errorf("Routes.Flood = %d, want 1", got.Routes.Flood)
	}
	if got.Routes.Direct != 0 || got.Routes.TransportDirect != 0 {
		t.Errorf("Routes = %+v, want Direct/TransportDirect both 0 (this node forwarded no such packets)", got.Routes)
	}
}

// TestScopeConformanceRouteMixIgnoresDirectRoutes is the route-mix half of
// the FLOOD-only attribution rule: a DIRECT packet whose path[last] happens
// to equal this pubkey must not inflate Routes.Direct, because path[last] on
// a DIRECT route is the far end, not proof this node forwarded anything.
func TestScopeConformanceRouteMixIgnoresDirectRoutes(t *testing.T) {
	s := newScopeTestStore(t)
	seedDirectTransmission(t, s, "1a2b", scopeMatched("be-van"))
	seedTransmissionRoute(t, s, "1a2b", scopeMatched("be-van"), RouteTransportDirect)

	got, err := s.ScopeConformance("1a2b", "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if got.Routes.Direct != 0 {
		t.Errorf("Routes.Direct = %d, want 0 — DIRECT path[last] is the route's far end, not this forwarder", got.Routes.Direct)
	}
	if got.Routes.TransportDirect != 0 {
		t.Errorf("Routes.TransportDirect = %d, want 0 — same rule for TRANSPORT_DIRECT", got.Routes.TransportDirect)
	}
	if len(got.Observed) != 0 {
		t.Errorf("Observed = %+v, want empty — no flood packet was seeded", got.Observed)
	}
}

// TestScopeConformanceRespectsSinceWindow confirms packets before the
// requested window are excluded, keeping the query an index range rather
// than a full scan.
func TestScopeConformanceRespectsSinceWindow(t *testing.T) {
	s := newScopeTestStore(t)
	seedTransmission(t, s, "1a2b", scopeMatched("be"))

	got, err := s.ScopeConformance("1a2b", "2099-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Observed) != 0 {
		t.Errorf("observed = %+v, want empty — the only seeded packet is before the since window", got.Observed)
	}
}

// testFullPubkeyA/testFullPubkeyB are 64-hex-char (32-byte) pubkeys standing
// in for real node identities. Task 2's endpoint hands ScopeConformance a
// full pubkey like this, not a short hash — path_json hops are truncated
// hashes (1-4 bytes), so the join must match the hop as a PREFIX of the full
// pubkey, not by exact equality.
var (
	testFullPubkeyA = "1a2b" + strings.Repeat("11", 30) // 64 hex chars
	testFullPubkeyB = "bbbb" + strings.Repeat("22", 30) // 64 hex chars
)

// TestScopeConformanceMatchesTruncatedHopAgainstFullPubkey is the case that
// fails without prefix matching: a real repeater query passes the full
// 64-char pubkey, but path_json only ever stores a 1-4 byte hash prefix. An
// exact-equality join would silently return nothing for every real node.
func TestScopeConformanceMatchesTruncatedHopAgainstFullPubkey(t *testing.T) {
	s := newScopeTestStore(t)
	hop := testFullPubkeyA[:4] // 2-byte hash, a genuine prefix of the full pubkey
	seedTransmissionRoute(t, s, hop, scopeMatched("be-van"), RouteFlood)

	got, err := s.ScopeConformance(testFullPubkeyA, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Observed) != 1 || got.Observed[0].Scope != "be-van" {
		t.Errorf("observed = %+v, want the scope attributed via the truncated hop matching the full pubkey's prefix", got.Observed)
	}
}

// TestScopeConformanceExcludesOneByteHop mirrors deriveHeardKey's rule
// (cmd/ingestor/client_reception.go: "exclude 1-byte (collision-prone),
// matching Reach"): a 2-hex-char hop is too short to trust as an
// attribution, even though it is a genuine prefix of the pubkey.
func TestScopeConformanceExcludesOneByteHop(t *testing.T) {
	s := newScopeTestStore(t)
	hop := testFullPubkeyA[:2] // 1-byte hash — collision-prone, must not attribute
	seedTransmissionRoute(t, s, hop, scopeMatched("be-van"), RouteFlood)

	got, err := s.ScopeConformance(testFullPubkeyA, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Observed) != 0 {
		t.Errorf("observed = %+v, want empty — a 1-byte hop must not be attributed even though it is a genuine prefix", got.Observed)
	}
}

// TestScopeConformanceDoesNotCrossAttributeDifferentPubkey guards against a
// prefix match that is too loose: a hop that is a genuine prefix of pubkey B
// must not be attributed to pubkey A just because both are being queried
// with the same truncated-hash join.
func TestScopeConformanceDoesNotCrossAttributeDifferentPubkey(t *testing.T) {
	s := newScopeTestStore(t)
	hopForB := testFullPubkeyB[:4]
	seedTransmissionRoute(t, s, hopForB, scopeMatched("be-van"), RouteFlood)

	got, err := s.ScopeConformance(testFullPubkeyA, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Observed) != 0 {
		t.Errorf("observed = %+v, want empty — hop matches pubkey B's prefix, not pubkey A's", got.Observed)
	}
}
