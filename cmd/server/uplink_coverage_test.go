package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

func TestUplinkClass(t *testing.T) {
	cases := map[[2]bool]string{
		{true, true}: uplinkBoth, {false, true}: uplinkObserved, {true, false}: uplinkEcho, {false, false}: uplinkNone,
	}
	for in, want := range cases {
		if got := uplinkClass(in[0], in[1]); got != want {
			t.Errorf("uplinkClass(echo=%v, observed=%v) = %s, want %s", in[0], in[1], got, want)
		}
	}
}

func TestAggregateUplinkCountsClassesPerCell(t *testing.T) {
	rows := []uplinkPingRow{
		{Lat: 50.998, Lon: 5.384, SentAt: "2026-10-16T14:51:15.018Z", EchoCount: 3, FirstHops: "c0cd,1403", PktHash: "aaaa"},
		{Lat: 50.998, Lon: 5.384, SentAt: "2026-10-16T14:02:45.000Z", EchoCount: 0, FirstHops: "", PktHash: ""},
		{Lat: 50.998, Lon: 5.3841, SentAt: "2026-10-16T15:00:00.000Z", EchoCount: 1, FirstHops: "1403", PktHash: "bbbb"},
		{Lat: 51.2, Lon: 5.6, SentAt: "2026-10-16T15:10:00.000Z", EchoCount: 0, FirstHops: "", PktHash: "cccc"},
	}
	arrivals := map[string]uplinkArrival{"aaaa": {Observers: 17, MaxHops: 11}, "cccc": {Observers: 2, MaxHops: 3}}
	fc := aggregateUplink(rows, arrivals, 9)
	if len(fc.Features) != 2 {
		t.Fatalf("cells = %d, want 2", len(fc.Features))
	}
	byCell := map[string]UplinkProperties{}
	for _, f := range fc.Features {
		byCell[f.Properties.Cell] = f.Properties
	}
	home := byCell[hexCellAt(50.998, 5.384, 9)]
	if home.Pings != 3 || home.Both != 1 || home.Echo != 1 || home.None != 1 || home.Observed != 0 {
		t.Fatalf("home cell = %+v", home)
	}
	if home.Best != uplinkBoth || home.MaxObservers != 17 || home.MaxHops != 11 {
		t.Errorf("home summary = %+v", home)
	}
	if len(home.FirstHops) != 2 || home.FirstHops[0] != "c0cd" || home.FirstHops[1] != "1403" {
		t.Errorf("first hops = %v, want distinct c0cd,1403", home.FirstHops)
	}
	if home.LastAt != "2026-10-16T15:00:00.000Z" {
		t.Errorf("last_at = %s", home.LastAt)
	}
	far := byCell[hexCellAt(51.2, 5.6, 9)]
	if far.Observed != 1 || far.Best != uplinkObserved {
		t.Errorf("far cell = %+v, want observed only", far)
	}
}

func seedUplinkDB(t *testing.T) *DB {
	t.Helper()
	db := setupTestDBv2(t)
	mustExecDB(t, db, `CREATE TABLE client_uplink_pings (
		id INTEGER PRIMARY KEY AUTOINCREMENT, rx_pubkey TEXT NOT NULL, tag TEXT NOT NULL, sent_at TEXT NOT NULL,
		ingested_at TEXT NOT NULL, channel TEXT NOT NULL DEFAULT '', scope TEXT NOT NULL DEFAULT '', pkt_hash TEXT,
		echo_count INTEGER NOT NULL DEFAULT 0, first_hops TEXT NOT NULL DEFAULT '', echoes_json TEXT NOT NULL DEFAULT '[]',
		lat REAL, lon REAL, pos_acc_m REAL, UNIQUE(rx_pubkey, tag))`)
	mustExecDB(t, db, `CREATE TABLE client_uplink_tx (tag TEXT PRIMARY KEY, pkt_hash TEXT NOT NULL, sender TEXT NOT NULL DEFAULT '', seen_at TEXT NOT NULL)`)
	recent := time.Now().UTC().Add(-time.Hour).Format(rxTimeMillisLayoutServer)
	old := time.Now().UTC().AddDate(0, 0, -20).Format(rxTimeMillisLayoutServer)
	// Echo plus observers: both.
	mustExecDB(t, db, `INSERT INTO client_uplink_pings (rx_pubkey, tag, sent_at, ingested_at, pkt_hash, echo_count, first_hops, lat, lon)
		VALUES ('aa11','78277c0e','`+recent+`','x','b76c6a2777f9b0e9',3,'1403,c0cd',50.998,5.384)`)
	// No echo, hash learned by the ingestor's decrypt: observed.
	mustExecDB(t, db, `INSERT INTO client_uplink_pings (rx_pubkey, tag, sent_at, ingested_at, echo_count, lat, lon)
		VALUES ('aa11','11111111','`+recent+`','x',0,51.10,5.50)`)
	mustExecDB(t, db, `INSERT INTO client_uplink_tx (tag, pkt_hash, seen_at) VALUES ('11111111','1111aaaa2222bbbb','x')`)
	// Old ping: outside a 7-day window.
	mustExecDB(t, db, `INSERT INTO client_uplink_pings (rx_pubkey, tag, sent_at, ingested_at, echo_count, lat, lon)
		VALUES ('aa11','22222222','`+old+`','x',0,50.90,5.30)`)
	// No position: never on the map.
	mustExecDB(t, db, `INSERT INTO client_uplink_pings (rx_pubkey, tag, sent_at, ingested_at, echo_count)
		VALUES ('aa11','33333333','`+recent+`','x',1)`)
	for _, h := range []string{"b76c6a2777f9b0e9", "1111aaaa2222bbbb"} {
		mustExecDB(t, db, `INSERT INTO transmissions (raw_hex, hash, first_seen, payload_type) VALUES ('00','`+h+`','x',5)`)
	}
	mustExecDB(t, db, `INSERT INTO observations (transmission_id, observer_id, path_json, timestamp) VALUES
		(1,'obsA','["1403","23C6"]',1),(1,'obsB','["1403","23C6","907C"]',1),(1,'obsA','["1403"]',1),
		(2,'obsC','["5A5A"]',1)`)
	// 1403 resolves to one node; c0cd matches two, so it stays unnamed.
	mustExecDB(t, db, `INSERT INTO nodes (public_key, name) VALUES ('1403aa', 'BE-ZHV-ON4AZP'), ('c0cd01', 'one'), ('c0cd02', 'two')`)
	return db
}

func getUplink(t *testing.T, srv *Server, q string) (int, UplinkFeatureCollection) {
	t.Helper()
	router := mux.NewRouter()
	router.HandleFunc("/api/uplink-coverage", srv.handleUplinkCoverage).Methods("GET")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest("GET", "/api/uplink-coverage?"+q, nil))
	var fc UplinkFeatureCollection
	if rr.Code == 200 {
		if err := json.Unmarshal(rr.Body.Bytes(), &fc); err != nil {
			t.Fatal(err)
		}
	}
	return rr.Code, fc
}

func TestUplinkCoverageEndpoint(t *testing.T) {
	srv := &Server{db: seedUplinkDB(t), cfg: &Config{ClientUplink: &ClientUplinkConfig{Enabled: true}}}
	code, fc := getUplink(t, srv, "bbox=50,5,52,6&z=12&days=7")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(fc.Features) != 2 {
		t.Fatalf("cells = %d, want 2 (old and position-less pings left out)", len(fc.Features))
	}
	got := map[string]UplinkProperties{}
	for _, f := range fc.Features {
		got[f.Properties.Best] = f.Properties
	}
	both, ok := got[uplinkBoth]
	if !ok || both.MaxObservers != 2 || both.MaxHops != 3 {
		t.Fatalf("both cell = %+v, want 2 distinct observers and 3 hops", both)
	}
	if obs, ok := got[uplinkObserved]; !ok || obs.MaxObservers != 1 {
		t.Fatalf("observed cell (hash from client_uplink_tx) = %+v", obs)
	}
	if len(both.FirstHops) != 2 || len(both.FirstHopNames) != 2 || both.FirstHopNames[0] != "BE-ZHV-ON4AZP" || both.FirstHopNames[1] != "" {
		t.Fatalf("first hops %v named %q, want 1403 named and the ambiguous c0cd left empty", both.FirstHops, both.FirstHopNames)
	}
	if code, fc := getUplink(t, srv, "bbox=50,5,52,6&z=12&days=30"); code != 200 || len(fc.Features) != 3 {
		t.Fatalf("30 days: status %d, cells %d, want 3", code, len(fc.Features))
	}
}

func TestUplinkCoverageGatedAndTolerant(t *testing.T) {
	off := &Server{db: seedUplinkDB(t), cfg: &Config{}}
	if code, _ := getUplink(t, off, "bbox=50,5,52,6"); code != 404 {
		t.Fatalf("feature off: status %d, want 404", code)
	}
	on := &Server{db: seedUplinkDB(t), cfg: &Config{ClientUplink: &ClientUplinkConfig{Enabled: true}}}
	if code, _ := getUplink(t, on, "bbox=nope"); code != 400 {
		t.Fatalf("bad bbox: status %d, want 400", code)
	}
	bare := &Server{db: setupTestDBv2(t), cfg: &Config{ClientUplink: &ClientUplinkConfig{Enabled: true}}}
	if code, fc := getUplink(t, bare, "bbox=50,5,52,6"); code != 200 || len(fc.Features) != 0 {
		t.Fatalf("no tables yet: status %d, cells %d, want 200 and none", code, len(fc.Features))
	}
}

// The uplink layer narrows to one companion (?rx=) or to the caller's linked
// companions (?mine=1) like the signal and noise layers.
func TestUplinkCoverageRxAndMine(t *testing.T) {
	f := newAuthFixture(t)
	f.srv.db = seedUplinkDB(t)
	f.srv.cfg.ClientUplink = &ClientUplinkConfig{Enabled: true}
	f.router.HandleFunc("/api/uplink-coverage", f.srv.handleUplinkCoverage).Methods("GET")
	mine := pubHex(companionKey)
	recent := time.Now().UTC().Add(-time.Hour).Format(rxTimeMillisLayoutServer)
	mustExecDB(t, f.srv.db, `INSERT INTO client_uplink_pings (rx_pubkey, tag, sent_at, ingested_at, echo_count, lat, lon)
		VALUES ('`+mine+`','44444444','`+recent+`','x',0,51.30,5.70)`)
	const q = "/api/uplink-coverage?bbox=50,5,52,6&z=12&days=7"
	get := func(path string, mods ...reqMod) *httptest.ResponseRecorder { return f.do("GET", path, nil, mods...) }
	cells := func(w *httptest.ResponseRecorder) int { return strings.Count(w.Body.String(), `"Feature"`) }
	if n := cells(get(q)); n != 3 {
		t.Fatalf("everyone: %d cells, want 3", n)
	}
	if n := cells(get(q + "&rx=AA11")); n != 2 {
		t.Fatalf("rx=aa11 (upper case): %d cells, want its 2", n)
	}

	expectStatus(t, get(q+"&mine=1"), http.StatusUnauthorized)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	w := get(q+"&mine=1", as(alice))
	expectStatus(t, w, http.StatusOK)
	if n := cells(w); n != 0 {
		t.Fatalf("nothing linked: %d cells, want none", n)
	}
	tok := f.deviceToken(t, "alice@example.org", pw, "Pixel").Token
	expectStatus(t, f.linkCompanion(t, tok, companionKey, "Car"), http.StatusOK)
	w = get(q+"&mine=1", as(alice))
	expectStatus(t, w, http.StatusOK)
	if w.Body.String() != get(q+"&rx="+mine).Body.String() || cells(w) != 1 {
		t.Fatalf("mine=1 is not the linked companion's ping: %s", w.Body.String())
	}
	expectStatus(t, get(q+"&mine=1", bearer(tok)), http.StatusForbidden)
}
