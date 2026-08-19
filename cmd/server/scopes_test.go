package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
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
	seedTransmissionRouteAt(t, s, forwarder, seed, routeType, "2026-01-15T12:00:00Z")
}

// seedTransmissionRouteAt is seedTransmissionRoute with an explicit
// first_seen, for tests (e.g. the handler tests below) that need a
// transmission to fall inside a real-wall-clock ?window= lookback rather
// than the fixed date the ScopeConformance unit tests above use.
func seedTransmissionRouteAt(t *testing.T, s *PacketStore, forwarder string, seed scopeSeed, routeType int, firstSeen string) {
	t.Helper()
	scopeSeedCounter++
	hash := fmt.Sprintf("scopehash%d", scopeSeedCounter)

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
	// Same forwarder on all three, so only the scope state differs. Unmatched
	// and unscoped are seeded with DIFFERENT counts (2 vs 3) deliberately: a
	// fixture with the same count for both (as this test used to have, 1/1)
	// cannot catch the two increments being swapped in scopes.go — the
	// asserted numbers would still match either way. See FIX 4.
	seedTransmission(t, s, "1a2b", scopeMatched("be-van")) // scope_name = matched name
	seedTransmission(t, s, "1a2b", scopeUnmatched())       // code1 != 0000, no key -> scope_name = empty string
	seedTransmission(t, s, "1a2b", scopeUnmatched())
	seedTransmission(t, s, "1a2b", scopeUnscoped()) // code1 == 0000 -> scope_name = NULL
	seedTransmission(t, s, "1a2b", scopeUnscoped())
	seedTransmission(t, s, "1a2b", scopeUnscoped())

	got, err := s.ScopeConformance("1a2b", "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Observed) != 1 || got.Observed[0].Scope != "be-van" {
		t.Errorf("observed = %+v, want exactly the matched scope", got.Observed)
	}
	if got.Unmatched != 2 {
		t.Errorf("unmatched = %d, want 2 — a scoped packet we hold no key for is information, not absence", got.Unmatched)
	}
	if got.Unscoped != 3 {
		t.Errorf("unscoped = %d, want 3", got.Unscoped)
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

// TestScopeConformanceSurvivesMalformedPathJSON guards against a SQLite
// quirk: json_each() in the EXISTS subquery is evaluated independently of
// the `o.path_json IS NOT NULL` predicate in the same WHERE clause, so a
// single row anywhere in the window whose path_json is an empty string or
// otherwise non-JSON text fails json_each() with "malformed JSON (1)" and
// errors the WHOLE query — not just that row — for every pubkey. A good row
// alongside the malformed one must still be returned, and the request must
// not error.
func TestScopeConformanceSurvivesMalformedPathJSON(t *testing.T) {
	s := newScopeTestStore(t)
	seedTransmission(t, s, "1a2b", scopeMatched("be-van")) // good row

	// A second transmission with a malformed path_json observation, forced
	// straight into the DB — seedTransmissionRoute always writes valid JSON,
	// so this bypasses it to reproduce a row that could exist in a live DB
	// (e.g. partial ingest, a truncated write) without a helper that could
	// itself drift from what real corruption looks like.
	res, err := s.db.conn.Exec(
		`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, code1, code2, scope_name)
		 VALUES ('AA', 'malformedhash', '2026-01-15T12:00:00Z', ?, 1, '1234', '00', 'be-van')`,
		RouteFlood,
	)
	if err != nil {
		t.Fatalf("seed malformed transmission: %v", err)
	}
	txID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("seed malformed transmission id: %v", err)
	}
	if _, err := s.db.conn.Exec(
		`INSERT INTO observations (transmission_id, path_json, timestamp) VALUES (?, '', ?)`,
		txID, time.Now().Unix(),
	); err != nil {
		t.Fatalf("seed malformed observation: %v", err)
	}

	got, err := s.ScopeConformance("1a2b", "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("a malformed path_json row elsewhere must not error the whole query: %v", err)
	}
	if len(got.Observed) != 1 || got.Observed[0].Scope != "be-van" {
		t.Errorf("observed = %+v, want the one good row still returned", got.Observed)
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

// --- GET /api/nodes/{pubkey}/scopes handler tests (Task 2) ---

// setupNodeScopesServer builds a *Server wired to a DB carrying both the
// ScopeConformance schema (transmissions/observations, via
// setupScopeConformanceDB) and node_declared_regions, so the handler can be
// exercised end-to-end through the router.
func setupNodeScopesServer(t *testing.T) (*Server, *mux.Router) {
	t.Helper()
	db := setupScopeConformanceDB(t)
	if _, err := db.conn.Exec(`
		CREATE TABLE node_declared_regions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			target TEXT NOT NULL,
			rx_pubkey TEXT NOT NULL,
			observed_at TEXT NOT NULL,
			ingested_at TEXT NOT NULL,
			regions_csv TEXT NOT NULL,
			truncated INTEGER NOT NULL DEFAULT 0,
			lat REAL, lon REAL, pos_acc_m REAL, repeater_clock INTEGER,
			UNIQUE(target, rx_pubkey, observed_at)
		)`); err != nil {
		t.Fatal(err)
	}
	db.detectSchema() // picks up hasDeclaredRegionsTable now that the table exists

	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	srv.store = newTestStoreWithDB(t, db, cfg)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)
	return srv, router
}

func TestHandleNodeScopesKnownRepeater(t *testing.T) {
	srv, router := setupNodeScopesServer(t)
	hop := testFullPubkeyA[:4]
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionRouteAt(t, srv.store, hop, scopeMatched("be-van"), RouteFlood, recent)
	seedTransmissionRouteAt(t, srv.store, hop, scopeUnmatched(), RouteFlood, recent)
	seedTransmissionRouteAt(t, srv.store, hop, scopeUnscoped(), RouteFlood, recent)

	req := httptest.NewRequest("GET", "/api/nodes/"+testFullPubkeyA+"/scopes", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var got NodeScopesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Observed) != 1 || got.Observed[0].Scope != "be-van" {
		t.Errorf("observed = %+v, want exactly the matched scope with a non-empty name", got.Observed)
	}
	if got.Unmatched != 1 {
		t.Errorf("unmatched = %d, want 1 (separate top-level count)", got.Unmatched)
	}
	if got.Unscoped != 1 {
		t.Errorf("unscoped = %d, want 1 (separate top-level count)", got.Unscoped)
	}
	if got.Declared != nil {
		t.Errorf("declared = %+v, want nil — no declared-region row was seeded for this pubkey", got.Declared)
	}
}

func TestHandleNodeScopesUnknownRepeaterReturns200Empty(t *testing.T) {
	_, router := setupNodeScopesServer(t)
	unknown := strings.Repeat("ab", 32)

	req := httptest.NewRequest("GET", "/api/nodes/"+unknown+"/scopes", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a repeater we never heard is a valid empty answer, not 404", w.Code)
	}
	var got NodeScopesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Observed) != 0 || got.Unmatched != 0 || got.Unscoped != 0 {
		t.Errorf("want an empty conformance answer, got %+v", got)
	}
	if got.Declared != nil {
		t.Errorf("declared = %+v, want nil", got.Declared)
	}
	if !strings.Contains(w.Body.String(), `"declared":null`) {
		t.Errorf("body = %s, want a literal \"declared\":null, not an omitted key or empty object", w.Body.String())
	}
}

func TestHandleNodeScopesInvalidPubkeyReturns400(t *testing.T) {
	_, router := setupNodeScopesServer(t)

	req := httptest.NewRequest("GET", "/api/nodes/not-hex/scopes", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// TestHandleNodeScopesWindowVocabulary confirms this endpoint matches the
// vocabulary already used by the sibling /api/scope-stats endpoint (1h, 24h,
// 7d; default 24h) rather than the broader ParseTimeWindow alias set
// (which also accepts 1d/3d/1w/30d) used elsewhere in the API.
func TestHandleNodeScopesWindowVocabulary(t *testing.T) {
	_, router := setupNodeScopesServer(t)
	pk := strings.Repeat("cd", 32)

	for _, window := range []string{"1h", "24h", "7d"} {
		req := httptest.NewRequest("GET", "/api/nodes/"+pk+"/scopes?window="+window, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("window=%s: status = %d, want 200", window, w.Code)
		}
	}

	req := httptest.NewRequest("GET", "/api/nodes/"+pk+"/scopes", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var got NodeScopesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Window != "24h" {
		t.Errorf("default window = %q, want 24h", got.Window)
	}

	req = httptest.NewRequest("GET", "/api/nodes/"+pk+"/scopes?window=30d", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("window=30d: status = %d, want 400 (not part of this endpoint's vocabulary)", w.Code)
	}
}

// TestHandleNodeScopesDeclaredRegions covers Step 3b: a repeater that has
// answered a declared-regions request returns a populated declared object,
// ordered by the greatest observed_at (never ingested_at).
func TestHandleNodeScopesDeclaredRegions(t *testing.T) {
	srv, router := setupNodeScopesServer(t)
	pk := testFullPubkeyA

	// Older-but-later-ingested row (simulates a drive buffered offline that
	// arrives late) must NOT win over the fresher observed_at below.
	if _, err := srv.db.conn.Exec(`
		INSERT INTO node_declared_regions (target, rx_pubkey, observed_at, ingested_at, regions_csv, truncated)
		VALUES (?, ?, ?, ?, ?, 0)`,
		pk, "aabbccdd", "2026-08-10T00:00:00.000Z", "2026-08-18T23:59:59.000Z", "be"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.db.conn.Exec(`
		INSERT INTO node_declared_regions (target, rx_pubkey, observed_at, ingested_at, regions_csv, truncated)
		VALUES (?, ?, ?, ?, ?, 0)`,
		pk, "rxpubkeyhex", "2026-08-18T12:34:56.789Z", "2026-08-18T12:35:01.000Z", "*,be,be-vlg"); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/nodes/"+pk+"/scopes", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var got NodeScopesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Declared == nil {
		t.Fatal("declared = nil, want a populated object")
	}
	wantRegions := []string{"*", "be", "be-vlg"}
	if len(got.Declared.Regions) != len(wantRegions) {
		t.Fatalf("regions = %v, want %v", got.Declared.Regions, wantRegions)
	}
	for i, want := range wantRegions {
		if got.Declared.Regions[i] != want {
			t.Errorf("regions[%d] = %q, want %q", i, got.Declared.Regions[i], want)
		}
	}
	if got.Declared.ObservedAt != "2026-08-18T12:34:56.789Z" {
		t.Errorf("observedAt = %q, want the greatest observed_at row — ordering must never fall back to ingested_at", got.Declared.ObservedAt)
	}
	if got.Declared.Truncated {
		t.Error("truncated = true, want false")
	}
}

// TestHandleNodeScopesDeclaredEmptyRegionsIsNotNull covers the other half of
// Step 3b: a repeater that answered with zero flood-allowed regions is a
// meaningful, distinct fact from never having been asked, and must serialise
// as declared.regions == [] rather than declared == null.
func TestHandleNodeScopesDeclaredEmptyRegionsIsNotNull(t *testing.T) {
	srv, router := setupNodeScopesServer(t)
	pk := testFullPubkeyB

	if _, err := srv.db.conn.Exec(`
		INSERT INTO node_declared_regions (target, rx_pubkey, observed_at, ingested_at, regions_csv, truncated)
		VALUES (?, ?, ?, ?, ?, 0)`,
		pk, "rxpubkeyhex", "2026-08-18T12:00:00.000Z", "2026-08-18T12:00:05.000Z", ""); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/nodes/"+pk+"/scopes", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	if !strings.Contains(w.Body.String(), `"regions":[]`) {
		t.Errorf("body = %s, want a literal \"regions\":[], not null or an omitted key", w.Body.String())
	}
	var got NodeScopesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Declared == nil {
		t.Fatal("declared = nil, want a populated object — the repeater answered, it just declared nothing flood-allowed")
	}
	if len(got.Declared.Regions) != 0 {
		t.Errorf("regions = %v, want empty", got.Declared.Regions)
	}
}

// TestHandleNodeScopesNoDeclaredRegionsTable covers the missing-table
// degrade path (mirrors TestHandleScopeStatsNoColumn's established pattern
// for optional schema): an older database predating node_declared_regions
// must not fail the whole request — it degrades to declared: null.
func TestHandleNodeScopesNoDeclaredRegionsTable(t *testing.T) {
	db := setupScopeConformanceDB(t) // no node_declared_regions table at all
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	srv.store = newTestStoreWithDB(t, db, cfg)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	pk := strings.Repeat("ef", 32)
	req := httptest.NewRequest("GET", "/api/nodes/"+pk+"/scopes", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a missing node_declared_regions table must degrade, not fail the request: %s", w.Code, w.Body.String())
	}
	var got NodeScopesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Declared != nil {
		t.Errorf("declared = %+v, want nil when the table doesn't exist", got.Declared)
	}
}

// TestHandleNodeScopesUppercasePubkeyMatchesLowercaseDeclaredRow is the real
// case-normalisation coverage: every other test above seeds with a pubkey
// that is already a lowercase literal, so it can pass whether or not the
// handler lowercases the URL pubkey first — it isn't coverage of that call at
// all. Here the declared row's target is the lowercase string the ingestor
// actually writes, but the URL pubkey is uppercase. This must still resolve:
// isHexPubkey accepts only a-f (an unlowercased uppercase pubkey would 400
// before ever reaching the query), and the declared lookup is an exact-match
// `WHERE target = ?` against that lowercase column.
func TestHandleNodeScopesUppercasePubkeyMatchesLowercaseDeclaredRow(t *testing.T) {
	srv, router := setupNodeScopesServer(t)
	pk := testFullPubkeyA // already a lowercase literal

	if _, err := srv.db.conn.Exec(`
		INSERT INTO node_declared_regions (target, rx_pubkey, observed_at, ingested_at, regions_csv, truncated)
		VALUES (?, ?, ?, ?, ?, 0)`,
		pk, "rxpubkeyhex", "2026-08-18T12:00:00.000Z", "2026-08-18T12:00:05.000Z", "be"); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/nodes/"+strings.ToUpper(pk)+"/scopes", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — an uppercase URL pubkey must still resolve: %s", w.Code, w.Body.String())
	}
	var got NodeScopesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Declared == nil {
		t.Fatal("declared = nil, want a populated object — case must not silently empty the declared lookup")
	}
	if len(got.Declared.Regions) != 1 || got.Declared.Regions[0] != "be" {
		t.Errorf("regions = %v, want [\"be\"]", got.Declared.Regions)
	}
}

// --- FIX 3: response cache tests ---

// TestHandleNodeScopesServesFromCache confirms a second identical request
// (same pubkey + window) is served from the cache rather than recomputed: a
// transmission seeded between the two requests must not appear in the
// second response.
func TestHandleNodeScopesServesFromCache(t *testing.T) {
	srv, router := setupNodeScopesServer(t)
	hop := testFullPubkeyA[:4]
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionRouteAt(t, srv.store, hop, scopeMatched("be-van"), RouteFlood, recent)

	req := httptest.NewRequest("GET", "/api/nodes/"+testFullPubkeyA+"/scopes?window=1h", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var first NodeScopesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(first.Observed) != 1 || first.Observed[0].Packets != 1 {
		t.Fatalf("first response = %+v, want exactly 1 packet observed", first.Observed)
	}

	// Seed a second matching transmission. A recompute would report 2 packets.
	seedTransmissionRouteAt(t, srv.store, hop, scopeMatched("be-van"), RouteFlood, recent)

	req2 := httptest.NewRequest("GET", "/api/nodes/"+testFullPubkeyA+"/scopes?window=1h", nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w2.Code, w2.Body.String())
	}
	var second NodeScopesResponse
	if err := json.Unmarshal(w2.Body.Bytes(), &second); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(second.Observed) != 1 || second.Observed[0].Packets != 1 {
		t.Errorf("second response = %+v, want the cached 1-packet answer, not a recompute", second.Observed)
	}
}

// --- FIX 1: ScopeAuditForwarding ambiguous-hop attribution ---

// TestScopeAuditForwardingAttributesUnambiguousHop confirms a hop matching
// exactly one target still attributes normally, with ambiguousHops left at 0
// — the common case must be unaffected by the ambiguity rule.
func TestScopeAuditForwardingAttributesUnambiguousHop(t *testing.T) {
	s := newScopeTestStore(t)
	hop := testFullPubkeyA[:4]
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionRouteAt(t, s, hop, scopeMatched("#be"), RouteFlood, recent)

	got, err := s.ScopeAuditForwarding("2026-01-01T00:00:00Z", []string{testFullPubkeyA})
	if err != nil {
		t.Fatal(err)
	}
	agg := got[testFullPubkeyA]
	if agg == nil || agg.scopes["be"] == nil || agg.scopes["be"].Packets != 1 {
		t.Fatalf("want the hop attributed to the sole matching target, got %+v", got)
	}
	if agg.ambiguousHops != 0 {
		t.Errorf("ambiguousHops = %d, want 0 — nothing here is ambiguous", agg.ambiguousHops)
	}
}

// TestScopeAuditForwardingAmbiguousHopCreditsNeitherTarget is FIX 1's core
// case: two declared targets share a 2-byte prefix. A hop at that prefix
// must be attributed to NEITHER of them (crediting both would let a
// colliding neighbour's traffic silently paper over a real notObserved
// finding), and BOTH candidates' ambiguousHops counters must increment (so
// the row can surface the caveat regardless of which candidate is being
// looked at).
func TestScopeAuditForwardingAmbiguousHopCreditsNeitherTarget(t *testing.T) {
	s := newScopeTestStore(t)
	pkA := "1a2b" + strings.Repeat("11", 30)
	pkB := "1a2b" + strings.Repeat("22", 30) // shares pkA's first 4 hex chars
	hop := pkA[:4]
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionRouteAt(t, s, hop, scopeMatched("#be"), RouteFlood, recent)

	targets := []string{pkA, pkB}
	got, err := s.ScopeAuditForwarding("2026-01-01T00:00:00Z", targets)
	if err != nil {
		t.Fatal(err)
	}
	for _, pk := range targets {
		agg := got[pk]
		if agg == nil {
			t.Fatalf("%s: want an agg present to carry the ambiguity counter, got none (result = %+v)", pk, got)
		}
		if len(agg.scopes) != 0 {
			t.Errorf("%s: scopes = %+v, want empty — an ambiguous hop must not be attributed to either candidate", pk, agg.scopes)
		}
		if agg.unscopedPackets != 0 {
			t.Errorf("%s: unscopedPackets = %d, want 0", pk, agg.unscopedPackets)
		}
		if agg.ambiguousHops != 1 {
			t.Errorf("%s: ambiguousHops = %d, want 1", pk, agg.ambiguousHops)
		}
	}
}

// --- GET /api/scope-audit handler tests ---

// setupScopeAuditServer extends setupNodeScopesServer's schema with a
// minimal nodes table (public_key/name/role) so scopeAuditNodeIdentities has
// something to join against.
func setupScopeAuditServer(t *testing.T) (*Server, *mux.Router) {
	t.Helper()
	srv, router := setupNodeScopesServer(t)
	if _, err := srv.db.conn.Exec(`CREATE TABLE nodes (public_key TEXT PRIMARY KEY, name TEXT, role TEXT)`); err != nil {
		t.Fatal(err)
	}
	return srv, router
}

// insertDeclared seeds one node_declared_regions row (the latest-observed_at
// row wins per target, mirroring AllCurrentDeclaredRegions).
func insertDeclared(t *testing.T, srv *Server, target, observedAt, regionsCSV string, truncated int) {
	t.Helper()
	if _, err := srv.db.conn.Exec(`
		INSERT INTO node_declared_regions (target, rx_pubkey, observed_at, ingested_at, regions_csv, truncated)
		VALUES (?, 'rxpubkeyhex', ?, ?, ?, ?)`,
		target, observedAt, observedAt, regionsCSV, truncated); err != nil {
		t.Fatal(err)
	}
}

func getScopeAudit(t *testing.T, router *mux.Router, query string) ScopeAuditResponse {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/scope-audit"+query, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var got ScopeAuditResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

// TestHandleScopeAuditNormalisesHashPrefix pins trap 1: transmissions.scope_name
// keeps the '#' (hashRegions config), regions_csv arrives from the firmware
// with it already stripped. Declared "be-van" and observed "#be-van" must be
// recognised as the same scope, not reported as both missing and undeclared.
func TestHandleScopeAuditNormalisesHashPrefix(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	pk := testFullPubkeyA
	hop := pk[:4]
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	insertDeclared(t, srv, pk, time.Now().UTC().Format(time.RFC3339), "be-van", 0)
	seedTransmissionRouteAt(t, srv.store, hop, scopeMatched("#be-van"), RouteFlood, recent)

	// FIX 2: the DECLARED side must be normalised too, not just observed.
	// regions_csv is not guaranteed to arrive with '#' already stripped (a
	// firmware variant, an operator-seeded row, or a future collector could
	// leave it on) — declaring "#be-van" and observing "#be-van" (which the
	// observed side always normalises to "be-van") must still match. Without
	// the fix, declaredNamed/declaredSet keep the raw "#be-van", so this
	// second repeater's row would show BOTH "#be-van" in notObserved (the
	// declared value never matches the normalised agg.scopes key) AND
	// "be-van" in undeclaredObserved (the normalised observed name isn't in
	// declaredSet) — the exact trap normScope exists to prevent, reappearing
	// on the other side of the comparison. Seeded alongside pk (not as a
	// separate getScopeAudit call) so both land in one response and neither
	// is masked by the 30s response cache.
	pk2 := testFullPubkeyB
	hop2 := pk2[:4]
	insertDeclared(t, srv, pk2, time.Now().UTC().Format(time.RFC3339), "#be-van", 0)
	seedTransmissionRouteAt(t, srv.store, hop2, scopeMatched("#be-van"), RouteFlood, recent)

	got := getScopeAudit(t, router, "")
	if len(got.Repeaters) != 2 {
		t.Fatalf("repeaters = %+v, want exactly 2", got.Repeaters)
	}

	row := findScopeAuditRow(t, got.Repeaters, pk)
	if len(row.NotObserved) != 0 {
		t.Errorf("notObserved = %v, want empty — declared \"be-van\" observed as \"#be-van\" must match after normalisation", row.NotObserved)
	}
	if len(row.UndeclaredObserved) != 0 {
		t.Errorf("undeclaredObserved = %+v, want empty", row.UndeclaredObserved)
	}

	row2 := findScopeAuditRow(t, got.Repeaters, pk2)
	if len(row2.DeclaredRegions) != 1 || row2.DeclaredRegions[0] != "be-van" {
		t.Errorf("declaredRegions = %v, want [\"be-van\"] — the leading '#' must be stripped from the declared side too", row2.DeclaredRegions)
	}
	if len(row2.NotObserved) != 0 {
		t.Errorf("notObserved = %v, want empty — declared \"#be-van\" observed as \"#be-van\" must match after normalising BOTH sides", row2.NotObserved)
	}
	if len(row2.UndeclaredObserved) != 0 {
		t.Errorf("undeclaredObserved = %+v, want empty", row2.UndeclaredObserved)
	}
}

// findScopeAuditRow locates the row for pk, failing the test if absent —
// used wherever more than one repeater is seeded in the same response, since
// sort order is not by pubkey and cannot be relied on to pick the right row.
func findScopeAuditRow(t *testing.T, rows []ScopeAuditRow, pk string) ScopeAuditRow {
	t.Helper()
	for _, r := range rows {
		if r.PublicKey == pk {
			return r
		}
	}
	t.Fatalf("no row for pubkey %s in %+v", pk, rows)
	return ScopeAuditRow{}
}

// TestHandleScopeAuditExcludesWildcardFromComparison pins trap 2: '*' is the
// root of the region tree (governs plain FLOOD), not a scope. It must never
// appear in declaredRegions (or its count) or in notObserved/undeclaredObserved.
func TestHandleScopeAuditExcludesWildcardFromComparison(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	pk := testFullPubkeyA
	insertDeclared(t, srv, pk, time.Now().UTC().Format(time.RFC3339), "*,be", 0)

	got := getScopeAudit(t, router, "")
	if len(got.Repeaters) != 1 {
		t.Fatalf("repeaters = %+v, want 1", got.Repeaters)
	}
	row := got.Repeaters[0]
	if !row.DeclaredWildcard {
		t.Error("declaredWildcard = false, want true")
	}
	if len(row.DeclaredRegions) != 1 || row.DeclaredRegions[0] != "be" {
		t.Errorf("declaredRegions = %v, want [\"be\"] — '*' must be excluded from the region list and its count", row.DeclaredRegions)
	}
	for _, r := range row.NotObserved {
		if r == "*" {
			t.Error("notObserved contains '*' — it must never be treated as a scope")
		}
	}
}

// TestHandleScopeAuditOmitsRepeaterWithNoDeclaredRow pins trap 3: a repeater
// that was never successfully asked is absent from the response entirely —
// not shown as a row declaring nothing, which is a distinct, meaningful fact.
func TestHandleScopeAuditOmitsRepeaterWithNoDeclaredRow(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	declaredPk := testFullPubkeyA
	neverAskedPk := testFullPubkeyB
	insertDeclared(t, srv, declaredPk, time.Now().UTC().Format(time.RFC3339), "be", 0)
	if _, err := srv.db.conn.Exec(`INSERT INTO nodes (public_key, name, role) VALUES (?, 'never-asked', 'repeater')`, neverAskedPk); err != nil {
		t.Fatal(err)
	}

	got := getScopeAudit(t, router, "")
	if len(got.Repeaters) != 1 || got.Repeaters[0].PublicKey != declaredPk {
		t.Fatalf("repeaters = %+v, want exactly the one repeater that has declared", got.Repeaters)
	}
}

// TestHandleScopeAuditUnknownNodeNameIsNullNotEmpty proves a declared target
// this instance holds no nodes row for serialises name/role as null, not "".
// A declared-regions answer can name a repeater the network has never recorded,
// and "" would make that indistinguishable from a node we DO know that simply
// has no name — the same absent-is-not-empty rule the declared side follows.
func TestHandleScopeAuditUnknownNodeNameIsNullNotEmpty(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	pk := testFullPubkeyA
	insertDeclared(t, srv, pk, time.Now().UTC().Format(time.RFC3339), "be", 0)
	// deliberately NO nodes row for pk

	req := httptest.NewRequest("GET", "/api/scope-audit", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"name":null`) {
		t.Errorf(`body must carry "name":null for a target with no nodes row, got: %s`, body)
	}
	if strings.Contains(body, `"name":""`) {
		t.Error(`"name":"" collapses "unknown node" into "node with no name"`)
	}

	var got ScopeAuditResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Repeaters) != 1 {
		t.Fatalf("repeaters = %d, want 1", len(got.Repeaters))
	}
	if got.Repeaters[0].Name != nil {
		t.Errorf("Name = %q, want nil", *got.Repeaters[0].Name)
	}
}

// TestHandleScopeAuditWildcardContradiction: observed forwarding unscoped
// (plain-FLOOD) traffic while the declared list omits '*' is the wildcard
// contradiction this endpoint must flag — the repeater says it does NOT
// forward those packets, and the traffic says otherwise.
func TestHandleScopeAuditWildcardContradiction(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	pk := testFullPubkeyA
	hop := pk[:4]
	insertDeclared(t, srv, pk, time.Now().UTC().Format(time.RFC3339), "be", 0) // no '*'
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionRouteAt(t, srv.store, hop, scopeUnscoped(), RouteFlood, recent)

	got := getScopeAudit(t, router, "")
	if len(got.Repeaters) != 1 {
		t.Fatalf("repeaters = %+v, want 1", got.Repeaters)
	}
	row := got.Repeaters[0]
	if !row.WildcardContradiction {
		t.Error("wildcardContradiction = false, want true — observed unscoped forwarding but '*' not declared")
	}
	if row.ObservedUnscopedPackets != 1 {
		t.Errorf("observedUnscopedPackets = %d, want 1", row.ObservedUnscopedPackets)
	}
}

// TestHandleScopeAuditWildcardDeclaredIsNotAContradiction is the other half:
// the same observed unscoped traffic is expected, not a contradiction, once
// '*' is declared.
func TestHandleScopeAuditWildcardDeclaredIsNotAContradiction(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	pk := testFullPubkeyA
	hop := pk[:4]
	insertDeclared(t, srv, pk, time.Now().UTC().Format(time.RFC3339), "*,be", 0)
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionRouteAt(t, srv.store, hop, scopeUnscoped(), RouteFlood, recent)

	got := getScopeAudit(t, router, "")
	if got.Repeaters[0].WildcardContradiction {
		t.Error("wildcardContradiction = true, want false — '*' IS declared, so unscoped forwarding is expected")
	}
}

// TestHandleScopeAuditNotObservedAndUndeclaredObserved exercises both
// comparison directions in one repeater: a declared region with zero
// observed forwarding, and an observed scope absent from the declared list.
func TestHandleScopeAuditNotObservedAndUndeclaredObserved(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	pk := testFullPubkeyA
	hop := pk[:4]
	insertDeclared(t, srv, pk, time.Now().UTC().Format(time.RFC3339), "be,be-vlg", 0)
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionRouteAt(t, srv.store, hop, scopeMatched("#be"), RouteFlood, recent)
	seedTransmissionRouteAt(t, srv.store, hop, scopeMatched("#de-nw"), RouteFlood, recent)

	got := getScopeAudit(t, router, "")
	row := got.Repeaters[0]
	if len(row.NotObserved) != 1 || row.NotObserved[0] != "be-vlg" {
		t.Errorf("notObserved = %v, want [\"be-vlg\"]", row.NotObserved)
	}
	if len(row.UndeclaredObserved) != 1 || row.UndeclaredObserved[0].Scope != "de-nw" {
		t.Errorf("undeclaredObserved = %+v, want exactly \"de-nw\"", row.UndeclaredObserved)
	}
}

// TestHandleScopeAuditSurfacesAmbiguousHops is FIX 1's handler-level case:
// two repeaters both declare "be" and share a 2-byte pubkey prefix. The one
// hop seen in the window can't be attributed to either, so both rows must
// still show "be" as notObserved (an ambiguous hop invents no attribution,
// so it cannot silently satisfy the declared region) AND both rows must
// carry ambiguousHops=1, the caveat that the notObserved finding might be a
// prefix collision rather than a real gap.
func TestHandleScopeAuditSurfacesAmbiguousHops(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	pkA := "1a2b" + strings.Repeat("11", 30)
	pkB := "1a2b" + strings.Repeat("22", 30)
	hop := pkA[:4]
	insertDeclared(t, srv, pkA, time.Now().UTC().Format(time.RFC3339), "be", 0)
	insertDeclared(t, srv, pkB, time.Now().UTC().Format(time.RFC3339), "be", 0)
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionRouteAt(t, srv.store, hop, scopeMatched("#be"), RouteFlood, recent)

	got := getScopeAudit(t, router, "")
	if len(got.Repeaters) != 2 {
		t.Fatalf("repeaters = %+v, want 2", got.Repeaters)
	}
	for _, row := range got.Repeaters {
		if row.AmbiguousHops != 1 {
			t.Errorf("%s: ambiguousHops = %d, want 1", row.PublicKey, row.AmbiguousHops)
		}
		if len(row.NotObserved) != 1 || row.NotObserved[0] != "be" {
			t.Errorf("%s: notObserved = %v, want [\"be\"] — an ambiguous hop must not silently satisfy the declared region", row.PublicKey, row.NotObserved)
		}
	}
}

// TestHandleScopeAuditSortsMissingRegionsFirst: the repeater with a declared
// region it is not forwarding must rank above a repeater in full agreement —
// that's the headline this endpoint exists to surface, not the boring majority.
func TestHandleScopeAuditSortsMissingRegionsFirst(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	agreePk := testFullPubkeyA
	missingPk := testFullPubkeyB
	agreeHop := agreePk[:4]
	missingHop := missingPk[:4]
	insertDeclared(t, srv, agreePk, time.Now().UTC().Format(time.RFC3339), "be", 0)
	insertDeclared(t, srv, missingPk, time.Now().UTC().Format(time.RFC3339), "be,be-vlg", 0)
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionRouteAt(t, srv.store, agreeHop, scopeMatched("#be"), RouteFlood, recent)
	seedTransmissionRouteAt(t, srv.store, missingHop, scopeMatched("#be"), RouteFlood, recent)
	// missingPk never forwards be-vlg -> 1 missing declared region; agreePk has 0.

	got := getScopeAudit(t, router, "")
	if len(got.Repeaters) != 2 {
		t.Fatalf("repeaters = %+v, want 2", got.Repeaters)
	}
	if got.Repeaters[0].PublicKey != missingPk {
		t.Errorf("repeaters[0] = %s, want %s (the row missing a declared region) ranked first", got.Repeaters[0].PublicKey, missingPk)
	}
}

// TestHandleScopeAuditWindowVocabulary confirms this endpoint matches the
// vocabulary used by the sibling /api/scope-stats and /api/nodes/{pubkey}/scopes
// endpoints (1h, 24h, 7d), not the broader ParseTimeWindow alias set.
func TestHandleScopeAuditWindowVocabulary(t *testing.T) {
	_, router := setupScopeAuditServer(t)
	req := httptest.NewRequest("GET", "/api/scope-audit?window=30d", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("window=30d: status = %d, want 400 (not part of this endpoint's vocabulary)", w.Code)
	}
}

// TestHandleScopeAuditFiltersBlacklistedNode confirms a blacklisted repeater
// is dropped from the audit even though it has declared a region list,
// mirroring the blacklist filtering applied by other multi-node endpoints.
func TestHandleScopeAuditFiltersBlacklistedNode(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	pk := testFullPubkeyA
	insertDeclared(t, srv, pk, time.Now().UTC().Format(time.RFC3339), "be", 0)
	srv.cfg.NodeBlacklist = []string{pk}

	got := getScopeAudit(t, router, "")
	if len(got.Repeaters) != 0 {
		t.Errorf("repeaters = %+v, want empty — blacklisted node must be excluded", got.Repeaters)
	}
}

// TestHandleScopeAuditFiltersHiddenNamePrefix is the scope-audit twin of
// TestHandleScopeAuditFiltersBlacklistedNode, covering FIX 5: a repeater
// whose known name matches an operator-configured hidden-name prefix is
// excluded. It also pins the subtler half of the `id.Name != nil` guard in
// handleScopeAudit — a declared target this instance holds NO nodes row for
// has no name to test a hidden-prefix rule against, so it must NOT be
// filtered merely for lacking a name; only a KNOWN, matching name is ever
// grounds for hiding.
func TestHandleScopeAuditFiltersHiddenNamePrefix(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	hiddenPk := testFullPubkeyA
	unknownPk := testFullPubkeyB
	insertDeclared(t, srv, hiddenPk, time.Now().UTC().Format(time.RFC3339), "be", 0)
	insertDeclared(t, srv, unknownPk, time.Now().UTC().Format(time.RFC3339), "be", 0)
	if _, err := srv.db.conn.Exec(`INSERT INTO nodes (public_key, name, role) VALUES (?, ?, 'repeater')`, hiddenPk, "🚫 ban me"); err != nil {
		t.Fatal(err)
	}
	// deliberately no nodes row for unknownPk
	srv.cfg.SetHiddenNamePrefixes([]string{"🚫"})

	got := getScopeAudit(t, router, "")
	if len(got.Repeaters) != 1 || got.Repeaters[0].PublicKey != unknownPk {
		t.Fatalf("repeaters = %+v, want exactly the unknown-name target — the hidden-name-prefixed repeater must be excluded, and the nameless one must NOT be excluded merely for having no name", got.Repeaters)
	}
}

// TestHandleScopeAuditNoDeclaredRegionsTable covers the missing-table
// degrade path (mirrors TestHandleNodeScopesNoDeclaredRegionsTable): an
// older database predating node_declared_regions must not fail the request.
func TestHandleScopeAuditNoDeclaredRegionsTable(t *testing.T) {
	db := setupScopeConformanceDB(t) // no node_declared_regions table at all
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	srv.store = newTestStoreWithDB(t, db, cfg)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	got := getScopeAudit(t, router, "")
	if len(got.Repeaters) != 0 {
		t.Errorf("repeaters = %+v, want empty when node_declared_regions doesn't exist", got.Repeaters)
	}
}

// TestHandleNodeScopesDifferentWindowIsSeparateCacheEntry confirms the cache
// key includes window: a request for a different window must recompute
// rather than reuse another window's cached entry.
func TestHandleNodeScopesDifferentWindowIsSeparateCacheEntry(t *testing.T) {
	srv, router := setupNodeScopesServer(t)
	hop := testFullPubkeyA[:4]
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionRouteAt(t, srv.store, hop, scopeMatched("be-van"), RouteFlood, recent)

	req := httptest.NewRequest("GET", "/api/nodes/"+testFullPubkeyA+"/scopes?window=1h", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	// Seeded after the window=1h request warmed its own cache entry.
	seedTransmissionRouteAt(t, srv.store, hop, scopeMatched("be-van"), RouteFlood, recent)

	req2 := httptest.NewRequest("GET", "/api/nodes/"+testFullPubkeyA+"/scopes?window=24h", nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w2.Code, w2.Body.String())
	}
	var got NodeScopesResponse
	if err := json.Unmarshal(w2.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Observed) != 1 || got.Observed[0].Packets != 2 {
		t.Errorf("window=24h response = %+v, want a fresh compute (2 packets), not the window=1h cache entry", got.Observed)
	}
}
