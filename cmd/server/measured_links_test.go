package main

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/dbschema"
)

const (
	mlA = "aa000000000000000000000000000000000000000000000000000000000000a1"
	mlB = "bb000000000000000000000000000000000000000000000000000000000000b2"
	mlC = "cc000000000000000000000000000000000000000000000000000000000000c3"
	mlD = "dd000000000000000000000000000000000000000000000000000000000000d4"
)

// setupMeasuredServer is setupTestServer with the ingestor schema applied, so
// measured_links, neighbor_reports and the default_flood_scope columns exist
// exactly as the ingestor creates them.
func setupMeasuredServer(t *testing.T) (*Server, *mux.Router) {
	t.Helper()
	srv, router := setupTestServer(t)
	if err := dbschema.Apply(srv.db.conn, nil); err != nil {
		t.Fatalf("dbschema.Apply: %v", err)
	}
	if err := srv.db.detectSchema(t.Context(), srv.db.conn); err != nil {
		t.Fatal(err)
	}
	return srv, router
}

func insertMeasuredLink(t testing.TB, conn *sql.DB, reporter, neighbor string, snr *float64, reportedAt string, verified int, status string) {
	t.Helper()
	if _, err := conn.Exec(`INSERT INTO measured_links (reporter, neighbor, snr, heard_at, reported_at, verified, status)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, reporter, neighbor, snr, reportedAt, reportedAt, verified, status); err != nil {
		t.Fatal(err)
	}
}

func insertMeasuredNode(t *testing.T, conn *sql.DB, pk, name string, lat, lon float64) {
	t.Helper()
	if _, err := conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, ?, 'repeater', ?, ?, '2026-10-28T00:00:00Z', '2026-10-01T00:00:00Z', 1)`, pk, name, lat, lon); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMeasuredLinksBothDirections(t *testing.T) {
	srv, _ := setupMeasuredServer(t)
	now := time.Now().UTC().Format(time.RFC3339)
	insertMeasuredLink(t, srv.db.conn, mlA, mlB, f64(5.5), now, 1, "responded")
	insertMeasuredLink(t, srv.db.conn, mlB, mlA, f64(-2), now, 1, "timeout")

	ix := loadMeasuredLinks(srv.db.conn, defaultMeasuredLinkDays)
	info := measuredInfoByNeighbor(mlA, ix.forNode(mlA))
	got, ok := info[mlB]
	if !ok {
		t.Fatalf("forNode(A) has no link to B: %+v", info)
	}
	if got.SNRIn == nil || *got.SNRIn != 5.5 {
		t.Errorf("snrIn = %v, want 5.5 (A hears B)", got.SNRIn)
	}
	if got.SNROut == nil || *got.SNROut != -2 {
		t.Errorf("snrOut = %v, want -2 (B hears A)", got.SNROut)
	}
	if !got.Verified {
		t.Error("verified = false, want true: both directions are verified")
	}
	if got.Status != "responded" {
		t.Errorf("status = %q, want the status A reported about B", got.Status)
	}

	infoB := measuredInfoByNeighbor(mlB, ix.forNode(strings.ToUpper(mlB)))
	if g := infoB[mlA]; g.SNRIn == nil || *g.SNRIn != -2 || g.SNROut == nil || *g.SNROut != 5.5 {
		t.Errorf("forNode(B) = %+v, want snrIn -2, snrOut 5.5", g)
	}
}

// A row whose neighbour is its own reporter (a crafted report) is not
// indexed: a node never lists itself as a measured neighbour.
func TestLoadMeasuredLinksIgnoresSelfLoop(t *testing.T) {
	srv, _ := setupMeasuredServer(t)
	now := time.Now().UTC().Format(time.RFC3339)
	insertMeasuredLink(t, srv.db.conn, mlA, mlA, f64(9), now, 1, "responded")
	insertMeasuredLink(t, srv.db.conn, mlA, strings.ToUpper(mlA), f64(9), now, 1, "responded")
	insertMeasuredLink(t, srv.db.conn, mlA, mlB, f64(5.5), now, 1, "responded")

	ix := loadMeasuredLinks(srv.db.conn, defaultMeasuredLinkDays)
	links := ix.forNode(mlA)
	if len(links) != 1 || links[0].Neighbor != mlB {
		t.Fatalf("forNode(A) = %+v, want only the link to B", links)
	}
	if _, ok := measuredInfoByNeighbor(mlA, links)[mlA]; ok {
		t.Error("A lists itself as a measured neighbour")
	}
}

// A row older than retention.measuredLinkDays that the ingestor's daily prune
// has not removed yet is not loaded; a row inside the window is.
func TestLoadMeasuredLinksWindow(t *testing.T) {
	srv, _ := setupMeasuredServer(t)
	now := time.Now().UTC()
	insertMeasuredLink(t, srv.db.conn, mlA, mlB, f64(1), now.Add(-8*24*time.Hour).Format(time.RFC3339), 1, "responded")
	insertMeasuredLink(t, srv.db.conn, mlA, mlC, f64(2), now.Add(-6*24*time.Hour).Format(time.RFC3339), 1, "responded")

	links := loadMeasuredLinks(srv.db.conn, 7).forNode(mlA)
	if len(links) != 1 || links[0].Neighbor != mlC {
		t.Fatalf("forNode(A) with a 7-day window = %+v, want only the link to C", links)
	}
	if links := loadMeasuredLinks(srv.db.conn, 9).forNode(mlA); len(links) != 2 {
		t.Errorf("forNode(A) with a 9-day window = %d links, want 2", len(links))
	}
}

// latestNeighborReport picks the newest reported_at, not the last arrival:
// a replay of an old report received later is not the latest.
func TestLatestNeighborReportByReportedAt(t *testing.T) {
	srv, _ := setupMeasuredServer(t)
	for _, r := range []struct {
		received, reported string
		total              int
	}{
		{"2026-10-28T10:00:00Z", "2026-10-28T09:59:00Z", 12},
		{"2026-10-28T11:00:00Z", "2026-10-20T09:59:00Z", 3}, // replay
	} {
		if _, err := srv.db.conn.Exec(`INSERT INTO neighbor_reports (reporter, received_at, reported_at, verified, total, payload)
			VALUES (?, ?, ?, 0, ?, '{}')`, mlA, r.received, r.reported, r.total); err != nil {
			t.Fatal(err)
		}
	}
	nr, ok, err := srv.db.latestNeighborReport(mlA)
	if err != nil || !ok {
		t.Fatalf("latestNeighborReport: ok=%v err=%v", ok, err)
	}
	if nr.ReportedAt != "2026-10-28T09:59:00Z" || nr.Total == nil || *nr.Total != 12 {
		t.Errorf("latest = %+v, want the report of 2026-10-28T09:59:00Z", nr)
	}
}

func TestNodeNeighborsAddsMeasured(t *testing.T) {
	srv, router := setupMeasuredServer(t)
	insertMeasuredNode(t, srv.db.conn, mlA, "node-a", 51.0, 4.0)
	insertMeasuredNode(t, srv.db.conn, mlB, "node-b", 51.1, 4.1)
	srv.store.rebuildNodeCache()

	now := time.Now().UTC()
	// The inferred graph holds A-B (B in upper case, matched case-insensitively);
	// the report adds B (in the graph) and C
	// (heard only by A, no nodes row).
	srv.neighborGraph = makeTestGraph(newEdge(mlA, strings.ToUpper(mlB), "bb", 10, now))
	ts := now.Format(time.RFC3339)
	insertMeasuredLink(t, srv.db.conn, mlA, mlB, f64(7.25), ts, 0, "responded")
	insertMeasuredLink(t, srv.db.conn, mlA, mlC, nil, ts, 1, "timeout")
	srv.store.measured.Store(loadMeasuredLinks(srv.db.conn, defaultMeasuredLinkDays))

	var resp struct {
		Neighbors []struct {
			Pubkey   *string       `json:"pubkey"`
			Prefix   string        `json:"prefix"`
			Name     *string       `json:"name"`
			Count    int           `json:"count"`
			Inferred *bool         `json:"inferred"`
			Measured *MeasuredInfo `json:"measured"`
		} `json:"neighbors"`
	}
	getJSON(t, router, "/api/nodes/"+mlA+"/neighbors", &resp)
	if len(resp.Neighbors) != 2 {
		t.Fatalf("got %d neighbours, want 2 (one inferred, one measured-only): %+v", len(resp.Neighbors), resp.Neighbors)
	}
	byPK := map[string]int{}
	for i, n := range resp.Neighbors {
		if n.Pubkey != nil {
			byPK[strings.ToLower(*n.Pubkey)] = i
		}
	}
	b := resp.Neighbors[byPK[mlB]]
	if b.Measured == nil || b.Measured.SNRIn == nil || *b.Measured.SNRIn != 7.25 || b.Measured.SNROut != nil {
		t.Errorf("inferred entry B measured = %+v, want snrIn 7.25 and snrOut null", b.Measured)
	}
	if b.Measured != nil && b.Measured.Verified {
		t.Error("B measured verified = true, want false")
	}
	if b.Inferred != nil {
		t.Errorf("inferred entry carries inferred = %v, want the field omitted", *b.Inferred)
	}
	if b.Count != 10 {
		t.Errorf("inferred entry count = %d, want 10", b.Count)
	}

	ci, ok := byPK[mlC]
	if !ok {
		t.Fatalf("measured-only neighbour C not appended: %+v", resp.Neighbors)
	}
	c := resp.Neighbors[ci]
	if c.Inferred == nil || *c.Inferred {
		t.Errorf("measured-only entry inferred = %v, want false", c.Inferred)
	}
	if c.Count != 0 || c.Name != nil || c.Prefix != mlC[:4] {
		t.Errorf("measured-only entry = count %d name %v prefix %q, want 0, null, %q", c.Count, c.Name, c.Prefix, mlC[:4])
	}
	if c.Measured == nil || c.Measured.SNRIn != nil || c.Measured.Status != "timeout" || !c.Measured.Verified {
		t.Errorf("measured-only entry measured = %+v, want null snr, status timeout, verified", c.Measured)
	}
}

func TestMeasuredLinksBulkSkipsUnpositioned(t *testing.T) {
	srv, router := setupMeasuredServer(t)
	insertMeasuredNode(t, srv.db.conn, mlA, "node-a", 51.0, 4.0)
	insertMeasuredNode(t, srv.db.conn, mlB, "node-b", 51.1, 4.1)
	insertMeasuredNode(t, srv.db.conn, mlD, "node-d", 0, 0) // a row without a position
	srv.store.rebuildNodeCache()

	old := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	recent := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)
	insertMeasuredLink(t, srv.db.conn, mlB, mlA, f64(-3), old, 1, "responded")
	insertMeasuredLink(t, srv.db.conn, mlA, mlB, f64(4), recent, 0, "responded")
	insertMeasuredLink(t, srv.db.conn, mlA, mlC, f64(1), recent, 1, "responded") // C has no nodes row
	insertMeasuredLink(t, srv.db.conn, mlA, mlD, f64(2), recent, 1, "responded") // D has no position
	srv.store.measured.Store(loadMeasuredLinks(srv.db.conn, defaultMeasuredLinkDays))

	var resp struct {
		Links []MeasuredPair `json:"links"`
	}
	getJSON(t, router, "/api/links/measured", &resp)
	if len(resp.Links) != 1 {
		t.Fatalf("got %d links, want 1 (A-B only): %+v", len(resp.Links), resp.Links)
	}
	l := resp.Links[0]
	if l.A != mlA || l.B != mlB {
		t.Errorf("pair = %s-%s, want %s-%s (ordered a < b)", l.A, l.B, mlA, mlB)
	}
	if l.SNRAB == nil || *l.SNRAB != 4 || l.SNRBA == nil || *l.SNRBA != -3 {
		t.Errorf("snrAB/snrBA = %v/%v, want 4 (A hears B) / -3 (B hears A)", l.SNRAB, l.SNRBA)
	}
	if l.Verified {
		t.Error("verified = true, want false: the A->B direction is not verified")
	}
	if l.AgeSec < 9*60 || l.AgeSec > 11*60 {
		t.Errorf("ageSec = %d, want about 600 (newest of the two directions)", l.AgeSec)
	}
}

func TestMeasuredLinksAbsentTable(t *testing.T) {
	srv, router := setupTestServer(t)
	ix := loadMeasuredLinks(srv.db.conn, defaultMeasuredLinkDays)
	if ix == nil {
		t.Fatal("loadMeasuredLinks returned nil on a database without measured_links")
	}
	if got := ix.forNode(mlA); len(got) != 0 {
		t.Errorf("forNode on an empty index = %v", got)
	}
	srv.store.measured.Store(ix)

	var bulk struct {
		Links []MeasuredPair `json:"links"`
	}
	getJSON(t, router, "/api/links/measured", &bulk)
	if bulk.Links == nil || len(bulk.Links) != 0 {
		t.Errorf("links = %v, want an empty array", bulk.Links)
	}

	var nb struct {
		Neighbors []NeighborEntry `json:"neighbors"`
	}
	getJSON(t, router, "/api/nodes/"+mlA+"/neighbors", &nb)

	var detail struct {
		Node map[string]interface{} `json:"node"`
	}
	getJSON(t, router, "/api/nodes/aabbccdd11223344", &detail)
	if _, ok := detail.Node["neighbor_report"]; ok {
		t.Error("neighbor_report present on a database without neighbor_reports")
	}

	// No index stored at all (a store that never loaded one).
	srv.store.measured.Store(nil)
	getJSON(t, router, "/api/links/measured", &bulk)
	getJSON(t, router, "/api/nodes/"+mlA+"/neighbors", &nb)
}

func TestNodeDetailDefaultFloodScope(t *testing.T) {
	srv, router := setupMeasuredServer(t)
	insertMeasuredNode(t, srv.db.conn, mlA, "node-a", 51.0, 4.0)
	insertMeasuredNode(t, srv.db.conn, mlB, "node-b", 51.1, 4.1)
	if _, err := srv.db.conn.Exec(`UPDATE nodes SET default_flood_scope = '#be', default_flood_scope_at = '2026-10-28T10:00:00Z' WHERE public_key = ?`, mlA); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct {
		received, reported string
		total, queried     interface{}
		truncated          interface{}
		verified           int
	}{
		{"2026-10-27T10:00:00Z", "2026-10-27T09:59:00Z", 3, 3, 0, 0},
		{"2026-10-28T10:00:00Z", "2026-10-28T09:59:00Z", 12, 10, 1, 1},
	} {
		if _, err := srv.db.conn.Exec(`INSERT INTO neighbor_reports (reporter, received_at, reported_at, verified, total, queried, truncated, default_scope, payload)
			VALUES (?, ?, ?, ?, ?, ?, ?, '#be', '{}')`, mlA, r.received, r.reported, r.verified, r.total, r.queried, r.truncated); err != nil {
			t.Fatal(err)
		}
	}

	var resp struct {
		Node struct {
			DefaultFloodScope   *string                `json:"default_flood_scope"`
			DefaultFloodScopeAt *string                `json:"default_flood_scope_at"`
			NeighborReport      *NeighborReportSummary `json:"neighbor_report"`
		} `json:"node"`
	}
	getJSON(t, router, "/api/nodes/"+mlA, &resp)
	if resp.Node.DefaultFloodScope == nil || *resp.Node.DefaultFloodScope != "#be" {
		t.Errorf("default_flood_scope = %v, want #be", resp.Node.DefaultFloodScope)
	}
	if resp.Node.DefaultFloodScopeAt == nil || *resp.Node.DefaultFloodScopeAt != "2026-10-28T10:00:00Z" {
		t.Errorf("default_flood_scope_at = %v", resp.Node.DefaultFloodScopeAt)
	}
	nr := resp.Node.NeighborReport
	if nr == nil {
		t.Fatal("neighbor_report missing")
	}
	if nr.ReportedAt != "2026-10-28T09:59:00Z" || nr.Total == nil || *nr.Total != 12 || nr.Queried == nil || *nr.Queried != 10 ||
		nr.Truncated == nil || !*nr.Truncated || !nr.Verified {
		t.Errorf("neighbor_report = %+v, want the newest report (12/10, truncated, verified)", nr)
	}

	// A node that never reported carries no summary, and its scope is null.
	var other struct {
		Node map[string]interface{} `json:"node"`
	}
	getJSON(t, router, "/api/nodes/"+mlB, &other)
	if _, ok := other.Node["neighbor_report"]; ok {
		t.Error("neighbor_report present on a node that never reported")
	}
	if v, ok := other.Node["default_flood_scope"]; !ok || v != nil {
		t.Errorf("default_flood_scope = %v (present %v), want null", v, ok)
	}
}

// TestMeasuredRefreshKeepsIndexOnReadFailure: a read that fails must not
// replace the loaded index with an empty or partial one. Here measured_links
// exists but the SELECT fails (a column is missing).
func TestMeasuredRefreshKeepsIndexOnReadFailure(t *testing.T) {
	srv, _ := setupTestServer(t)
	if _, err := srv.db.conn.Exec(`CREATE TABLE measured_links (reporter TEXT, neighbor TEXT)`); err != nil {
		t.Fatal(err)
	}
	if ix := loadMeasuredLinks(srv.db.conn, defaultMeasuredLinkDays); ix != nil {
		t.Fatalf("loadMeasuredLinks on a failing read = %+v, want nil", ix)
	}
	prev := &measuredLinkIndex{byNode: map[string][]measuredLink{mlA: {{Reporter: mlA, Neighbor: mlB}}}}
	srv.store.measured.Store(prev)
	srv.store.refreshNeighborGraphFromSnapshot()
	if got := srv.store.measured.Load(); got != prev {
		t.Errorf("refresh after a failed read replaced the index with %+v", got)
	}
}

// TestMeasuredRefreshAbsentTableStoresEmpty: no table is not a failure, the
// refresh stores an empty index.
func TestMeasuredRefreshAbsentTableStoresEmpty(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.store.refreshNeighborGraphFromSnapshot()
	got := srv.store.measured.Load()
	if got == nil || len(got.byNode) != 0 {
		t.Errorf("index after refresh without measured_links = %+v, want empty and non-nil", got)
	}
}

func TestMeasuredLinksBulkOmitsHiddenNodes(t *testing.T) {
	srv, router := setupMeasuredServer(t)
	insertMeasuredNode(t, srv.db.conn, mlA, "node-a", 51.0, 4.0)
	insertMeasuredNode(t, srv.db.conn, mlB, "node-b", 51.1, 4.1)
	insertMeasuredNode(t, srv.db.conn, mlC, "hide-c", 51.2, 4.2)
	srv.store.rebuildNodeCache()
	srv.cfg.SetHiddenNamePrefixes([]string{"hide-"})

	ts := time.Now().UTC().Format(time.RFC3339)
	insertMeasuredLink(t, srv.db.conn, mlA, mlB, f64(1), ts, 1, "responded")
	insertMeasuredLink(t, srv.db.conn, mlA, mlC, f64(2), ts, 1, "responded")
	insertMeasuredLink(t, srv.db.conn, mlC, mlB, f64(3), ts, 1, "responded")
	srv.store.measured.Store(loadMeasuredLinks(srv.db.conn, defaultMeasuredLinkDays))

	var resp struct {
		Links []MeasuredPair `json:"links"`
	}
	getJSON(t, router, "/api/links/measured", &resp)
	if len(resp.Links) != 1 || resp.Links[0].A != mlA || resp.Links[0].B != mlB {
		t.Errorf("links = %+v, want only A-B (C is hidden)", resp.Links)
	}

	var nb struct {
		Neighbors []NeighborEntry `json:"neighbors"`
	}
	getJSON(t, router, "/api/nodes/"+mlA+"/neighbors", &nb)
	for _, n := range nb.Neighbors {
		if n.Pubkey != nil && *n.Pubkey == mlC {
			t.Errorf("hidden node C appended as a measured neighbour: %+v", n)
		}
	}
}

// TestNodeNeighborsMeasuredMatchesAmbiguousEntry: a graph entry without a
// resolved pubkey whose candidate (or prefix) is the measured neighbour gets
// `measured`; the neighbour is not appended a second time.
func TestNodeNeighborsMeasuredMatchesAmbiguousEntry(t *testing.T) {
	srv, router := setupMeasuredServer(t)
	insertMeasuredNode(t, srv.db.conn, mlA, "node-a", 51.0, 4.0)
	srv.store.rebuildNodeCache()

	now := time.Now().UTC()
	srv.neighborGraph = makeTestGraph(
		newAmbiguousEdge(mlA, "bb", []string{strings.ToUpper(mlB), "bbff000000000000000000000000000000000000000000000000000000000000"}, 8, now),
		newAmbiguousEdge(mlA, "cc", nil, 5, now),
	)
	ts := now.Format(time.RFC3339)
	insertMeasuredLink(t, srv.db.conn, mlA, mlB, f64(3.5), ts, 1, "responded")
	insertMeasuredLink(t, srv.db.conn, mlA, mlC, f64(-1), ts, 1, "responded")
	srv.store.measured.Store(loadMeasuredLinks(srv.db.conn, defaultMeasuredLinkDays))

	var resp struct {
		Neighbors []NeighborEntry `json:"neighbors"`
	}
	getJSON(t, router, "/api/nodes/"+mlA+"/neighbors", &resp)
	if len(resp.Neighbors) != 2 {
		t.Fatalf("got %d rows, want 2 (no appended duplicates): %+v", len(resp.Neighbors), resp.Neighbors)
	}
	for _, n := range resp.Neighbors {
		if n.Pubkey != nil || n.Inferred != nil {
			t.Errorf("row %+v: want the graph entry, not an appended one", n)
		}
		if n.Measured == nil || n.Measured.SNRIn == nil {
			t.Errorf("row prefix %q has no measured: %+v", n.Prefix, n)
			continue
		}
		want := map[string]float64{"bb": 3.5, "cc": -1}[n.Prefix]
		if *n.Measured.SNRIn != want {
			t.Errorf("row prefix %q snrIn = %v, want %v", n.Prefix, *n.Measured.SNRIn, want)
		}
	}
}

func BenchmarkLoadMeasuredLinks(b *testing.B) {
	db := setupTestDB(b)
	if err := dbschema.Apply(db.conn, nil); err != nil {
		b.Fatal(err)
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	tx, err := db.conn.Begin()
	if err != nil {
		b.Fatal(err)
	}
	// 100 reporters x 100 neighbours = 10 000 rows.
	for r := 0; r < 100; r++ {
		for n := 0; n < 100; n++ {
			if _, err := tx.Exec(`INSERT INTO measured_links (reporter, neighbor, snr, heard_at, reported_at, verified, status)
				VALUES (?, ?, ?, ?, ?, 1, 'responded')`, fmt.Sprintf("%064x", r), fmt.Sprintf("%064x", 1000+n), float64(n%20)-10, ts, ts); err != nil {
				b.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ix := loadMeasuredLinks(db.conn, defaultMeasuredLinkDays)
		if len(ix.forNode(fmt.Sprintf("%064x", 0))) != 100 {
			b.Fatal("unexpected link count")
		}
	}
}
