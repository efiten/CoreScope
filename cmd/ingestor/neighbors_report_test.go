package main

// Tests for the measured neighbour links (#1865,
// docs/superpowers/specs/2026-10-28-measured-neighbour-links-design.md): every
// field of the observer /neighbors report lands in neighbor_reports,
// measured_links and nodes.default_flood_scope.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	nrOriginUpper = "FEEDCA4AD4E2AE615AAAB3CB73FAEC6EF0C7AF4D410F5C58A70FC0F724B7C933"
	nrOrigin      = "feedca4ad4e2ae615aaab3cb73faec6ef0c7af4d410f5c58a70fc0f724b7c933"
	nrNbA         = "0ce5ea7cfa3ab01d11810ef56b73dd899cd6c58644d6a6832a5c1ae89afc5e25"
	nrNbB         = "d19bee16843d242c80da9599b5b996d051655d5ee78e9730f022d3fad5becedd"
	nrNbC         = "b0d17c59fcf580592f8fb78b67d2f0ce9e9187ef3483a765bdff1d7947a5109c"
)

// sample1865 is the report quoted in issue #1865 (ESP32 observer firmware).
const sample1865 = `{
  "timestamp": "2026-07-25T10:46:14.000000+00:00",
  "origin": "MQTT Beta Test 01",
  "origin_id": "FEEDCA4AD4E2AE615AAAB3CB73FAEC6EF0C7AF4D410F5C58A70FC0F724B7C933",
  "self": {"scopes": "*"},
  "neighbors": [
    {"pubkey": "0CE5EA7CFA3AB01D11810EF56B73DD899CD6C58644D6A6832A5C1AE89AFC5E25", "snr": 12.25, "heard_secs_ago": 86, "scopes": "", "status": "timeout"},
    {"pubkey": "D19BEE16843D242C80DA9599B5B996D051655D5EE78E9730F022D3FAD5BECEDD", "snr": 13, "heard_secs_ago": 89, "scopes": "", "status": "timeout"},
    {"pubkey": "B0D17C59FCF580592F8FB78B67D2F0CE9E9187EF3483A765BDFF1D7947A5109C", "snr": 12.25, "heard_secs_ago": 89, "scopes": "*", "status": "responded"}
  ]
}`

// sampleOpenHop has the OpenHop publisher's shape: counters and
// self.default_scope.
const sampleOpenHop = `{
  "timestamp": "2026-07-26T08:00:00Z",
  "origin": "OpenHop test",
  "origin_id": "FEEDCA4AD4E2AE615AAAB3CB73FAEC6EF0C7AF4D410F5C58A70FC0F724B7C933",
  "total_neighbors": 12,
  "queried_neighbors": 2,
  "self": {"scopes": "be", "default_scope": "#be"},
  "neighbors": [
    {"pubkey": "0CE5EA7CFA3AB01D11810EF56B73DD899CD6C58644D6A6832A5C1AE89AFC5E25", "snr": -4.5, "heard_secs_ago": 30, "scopes": "be", "status": "responded"},
    {"pubkey": "D19BEE16843D242C80DA9599B5B996D051655D5EE78E9730F022D3FAD5BECEDD", "snr": 2, "heard_secs_ago": 600, "scopes": "", "status": "timeout"}
  ]
}`

// ingestReport runs the handler the way handleMessage does: raw bytes in,
// unmarshalled map alongside, from a source without verifiesObservers.
func ingestReport(t *testing.T, store *Store, topicID, raw string) {
	t.Helper()
	ingestReportFrom(t, store, false, topicID, raw)
}

// ingestReportFrom is ingestReport through a source whose verifiesObservers
// is the given value.
func ingestReportFrom(t *testing.T, store *Store, verifiesObservers bool, topicID, raw string) {
	t.Helper()
	var msg map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("sample does not parse: %v", err)
	}
	handleNeighborsReport(store, "test", verifiesObservers, topicID, msg, []byte(raw))
}

type nrLinkRow struct {
	snr        sql.NullFloat64
	heardAt    string
	reportedAt string
	verified   int
	status     string
}

func readLink(t *testing.T, store *Store, reporter, neighbor string) (nrLinkRow, bool) {
	t.Helper()
	var r nrLinkRow
	err := store.db.QueryRow(`SELECT snr, heard_at, reported_at, verified, status FROM measured_links WHERE reporter = ? AND neighbor = ?`,
		reporter, neighbor).Scan(&r.snr, &r.heardAt, &r.reportedAt, &r.verified, &r.status)
	if err == sql.ErrNoRows {
		return r, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return r, true
}

func fixReportNow(t *testing.T, at time.Time) {
	t.Helper()
	reportNow = func() time.Time { return at }
	t.Cleanup(func() { reportNow = time.Now })
}

func TestNeighborsReportStoresLinks(t *testing.T) {
	store := openNeighborsStore(t)
	fixReportNow(t, time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC))
	ingestReport(t, store, nrOriginUpper, sample1865)

	if n := countRows(t, store, "measured_links"); n != 3 {
		t.Fatalf("measured_links rows = %d, want 3", n)
	}
	want := map[string]nrLinkRow{
		nrNbA: {snr: sql.NullFloat64{Float64: 12.25, Valid: true}, heardAt: "2026-07-25T10:44:48Z", status: "timeout"},
		nrNbB: {snr: sql.NullFloat64{Float64: 13, Valid: true}, heardAt: "2026-07-25T10:44:45Z", status: "timeout"},
		nrNbC: {snr: sql.NullFloat64{Float64: 12.25, Valid: true}, heardAt: "2026-07-25T10:44:45Z", status: "responded"},
	}
	for nb, w := range want {
		got, ok := readLink(t, store, nrOrigin, nb)
		if !ok {
			t.Fatalf("no link %.8s -> %.8s", nrOrigin, nb)
		}
		if got.snr != w.snr || got.heardAt != w.heardAt || got.status != w.status {
			t.Errorf("link to %.8s = %+v, want snr %v heard_at %s status %s", nb, got, w.snr, w.heardAt, w.status)
		}
		if got.reportedAt != "2026-07-25T10:46:14Z" {
			t.Errorf("reported_at = %q, want canonical report timestamp", got.reportedAt)
		}
	}

	var reporter, receivedAt, reportedAt, payload string
	var total, queried, truncated sql.NullInt64
	var defScope sql.NullString
	if err := store.db.QueryRow(`SELECT reporter, received_at, reported_at, total, queried, truncated, default_scope, payload FROM neighbor_reports`).
		Scan(&reporter, &receivedAt, &reportedAt, &total, &queried, &truncated, &defScope, &payload); err != nil {
		t.Fatal(err)
	}
	if reporter != nrOrigin || receivedAt != "2026-07-26T09:00:00Z" || reportedAt != "2026-07-25T10:46:14Z" {
		t.Errorf("report row = %s %s %s", reporter, receivedAt, reportedAt)
	}
	if total.Valid || queried.Valid || truncated.Valid || defScope.Valid {
		t.Errorf("firmware sample has no counters/truncated/default_scope, got %v %v %v %v", total, queried, truncated, defScope)
	}
	if payload != sample1865 {
		t.Errorf("payload not stored as received")
	}

	// OpenHop: counters and default_scope on the report row.
	ingestReport(t, store, nrOriginUpper, sampleOpenHop)
	if err := store.db.QueryRow(`SELECT total, queried, default_scope FROM neighbor_reports ORDER BY id DESC LIMIT 1`).
		Scan(&total, &queried, &defScope); err != nil {
		t.Fatal(err)
	}
	if total.Int64 != 12 || queried.Int64 != 2 || defScope.String != "#be" {
		t.Errorf("OpenHop report row total=%v queried=%v default_scope=%v", total, queried, defScope)
	}
	if got, _ := readLink(t, store, nrOrigin, nrNbA); got.snr.Float64 != -4.5 || got.heardAt != "2026-07-26T07:59:30Z" {
		t.Errorf("OpenHop link = %+v", got)
	}

	// The firmware's truncated flag.
	ingestReport(t, store, nrOriginUpper, `{"timestamp":"2026-07-26T08:30:00Z","origin_id":"`+nrOriginUpper+`","truncated":true,"neighbors":[]}`)
	if err := store.db.QueryRow(`SELECT truncated FROM neighbor_reports ORDER BY id DESC LIMIT 1`).Scan(&truncated); err != nil {
		t.Fatal(err)
	}
	if !truncated.Valid || truncated.Int64 != 1 {
		t.Errorf("truncated = %v, want 1", truncated)
	}
}

func TestNeighborsReportOriginMismatchDropped(t *testing.T) {
	store := openNeighborsStore(t)
	seedNode(t, store, nrOrigin)
	seedNode(t, store, nrNbC)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	const other = "AA00000000000000000000000000000000000000000000000000000000000001"
	ingestReport(t, store, other, sample1865)
	ingestReport(t, store, other, sample1865)

	for _, table := range []string{"neighbor_reports", "measured_links"} {
		if n := countRows(t, store, table); n != 0 {
			t.Errorf("%s rows = %d, want 0", table, n)
		}
	}
	// The existing scope writes are dropped too.
	if sc, _ := configuredScope(t, store, nrOrigin); sc.Valid {
		t.Errorf("self scope written from a mismatched report: %v", sc)
	}
	if sc, _ := configuredScope(t, store, nrNbC); sc.Valid {
		t.Errorf("neighbour scope written from a mismatched report: %v", sc)
	}
	if n := store.Stats.NeighborsOriginMismatch.Load(); n != 2 {
		t.Errorf("mismatch count = %d, want 2", n)
	}
	if n := strings.Count(buf.String(), "origin_id"); n != 1 {
		t.Errorf("logged %d times, want once per reporter per hour:\n%s", n, buf.String())
	}

	// Case-insensitive match is not a mismatch.
	ingestReport(t, store, nrOrigin, sample1865)
	if n := countRows(t, store, "measured_links"); n != 3 {
		t.Errorf("lowercase topic id: measured_links rows = %d, want 3", n)
	}
}

func TestNeighborsReportMismatchLogIsBounded(t *testing.T) {
	var l reporterLogLimiter
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	for i := 0; i < reporterLogLimiterCap+10; i++ {
		l.allow(fmt.Sprintf("r%d", i))
	}
	if len(l.seen) > reporterLogLimiterCap {
		t.Fatalf("limiter holds %d reporters, cap %d", len(l.seen), reporterLogLimiterCap)
	}
	if l.allow("r0") {
		t.Fatal("same reporter logged twice within the hour")
	}
	now = now.Add(time.Hour)
	if !l.allow("r0") {
		t.Fatal("reporter not logged again after an hour")
	}
	if !l.allow("fresh") {
		t.Fatal("stale entries not evicted to make room")
	}
}

func TestNeighborsReportOmissionKeepsLink(t *testing.T) {
	store := openNeighborsStore(t)
	ingestReport(t, store, nrOriginUpper, sample1865)
	ingestReport(t, store, nrOriginUpper, `{"timestamp":"2026-07-25T22:46:14Z","origin_id":"`+nrOriginUpper+`","neighbors":[
		{"pubkey":"`+strings.ToUpper(nrNbA)+`","snr":7,"heard_secs_ago":4,"status":"timeout"}]}`)

	if n := countRows(t, store, "measured_links"); n != 3 {
		t.Fatalf("measured_links rows = %d, want 3 (omission must not delete)", n)
	}
	if got, _ := readLink(t, store, nrOrigin, nrNbA); got.snr.Float64 != 7 || got.reportedAt != "2026-07-25T22:46:14Z" {
		t.Errorf("reported neighbour not updated: %+v", got)
	}
	if got, _ := readLink(t, store, nrOrigin, nrNbB); got.reportedAt != "2026-07-25T10:46:14Z" {
		t.Errorf("omitted neighbour changed: %+v", got)
	}
}

func TestNeighborsReportOlderReportDoesNotOverwrite(t *testing.T) {
	store := openNeighborsStore(t)
	newer := `{"timestamp":"2026-07-26T10:00:00Z","origin_id":"` + nrOriginUpper + `","neighbors":[
		{"pubkey":"` + nrNbA + `","snr":10,"heard_secs_ago":0,"status":"responded"}]}`
	older := `{"timestamp":"2026-07-26T09:00:00Z","origin_id":"` + nrOriginUpper + `","neighbors":[
		{"pubkey":"` + nrNbA + `","snr":5,"heard_secs_ago":0,"status":"timeout"}]}`
	same := `{"timestamp":"2026-07-26T10:00:00Z","origin_id":"` + nrOriginUpper + `","neighbors":[
		{"pubkey":"` + nrNbA + `","snr":11,"heard_secs_ago":0,"status":"responded"}]}`
	ingestReport(t, store, nrOriginUpper, newer)
	ingestReport(t, store, nrOriginUpper, older)
	got, _ := readLink(t, store, nrOrigin, nrNbA)
	if got.snr.Float64 != 10 || got.status != "responded" || got.reportedAt != "2026-07-26T10:00:00Z" {
		t.Errorf("older report overwrote the link: %+v", got)
	}
	// An equal timestamp may replace (>=).
	ingestReport(t, store, nrOriginUpper, same)
	if got, _ := readLink(t, store, nrOrigin, nrNbA); got.snr.Float64 != 11 {
		t.Errorf("equal-timestamp report not applied: %+v", got)
	}
	// Every report is journalled, older or not.
	if n := countRows(t, store, "neighbor_reports"); n != 3 {
		t.Errorf("neighbor_reports rows = %d, want 3", n)
	}
}

// testObserverBindingsDDL mirrors internal/users observer_bindings at v8.
const testObserverBindingsDDL = `CREATE TABLE IF NOT EXISTS observer_bindings (
	pubkey TEXT PRIMARY KEY,
	user_id INTEGER,
	bound_at INTEGER NOT NULL,
	bound_by TEXT NOT NULL,
	confirmed_at INTEGER
)`

func TestNeighborsReportVerifiedFlag(t *testing.T) {
	const unconfirmed = "AB00000000000000000000000000000000000000000000000000000000000001"
	const orphan = "AB00000000000000000000000000000000000000000000000000000000000002"
	path := filepath.Join(t.TempDir(), "users.db")
	execUsersDB(t, path, testObserverBindingsDDL,
		`INSERT INTO observer_bindings VALUES ('`+nrOriginUpper+`', 1, 10, 'auto', 10)`,
		`INSERT INTO observer_bindings VALUES ('`+unconfirmed+`', 1, 10, 'admin', NULL)`,
		`INSERT INTO observer_bindings VALUES ('`+orphan+`', NULL, 10, 'auto', 10)`)

	store := openNeighborsStore(t)
	cs := newConfirmedObserverSet(path)
	t.Cleanup(cs.Close)
	cs.refresh()
	store.confirmedObservers = cs

	report := func(origin string) string {
		return `{"timestamp":"2026-07-26T10:00:00Z","origin_id":"` + origin + `","neighbors":[
			{"pubkey":"` + nrNbA + `","snr":1,"heard_secs_ago":1,"status":"timeout"}]}`
	}
	ingestReportFrom(t, store, true, nrOriginUpper, report(nrOriginUpper))
	ingestReportFrom(t, store, true, unconfirmed, report(unconfirmed))
	ingestReportFrom(t, store, true, orphan, report(orphan))

	if _, err := cs.db.Exec(`UPDATE observer_bindings SET confirmed_at = 1`); err == nil || !strings.Contains(strings.ToLower(err.Error()), "readonly") {
		t.Fatalf("write through the confirmed-observer users.db handle: err = %v; want a read-only refusal", err)
	}

	for _, c := range []struct {
		reporter string
		want     int
	}{{nrOrigin, 1}, {strings.ToLower(unconfirmed), 0}, {strings.ToLower(orphan), 0}} {
		var v int
		if err := store.db.QueryRow(`SELECT verified FROM neighbor_reports WHERE reporter = ?`, c.reporter).Scan(&v); err != nil {
			t.Fatal(err)
		}
		if v != c.want {
			t.Errorf("report from %.8s verified = %d, want %d", c.reporter, v, c.want)
		}
		if got, _ := readLink(t, store, c.reporter, nrNbA); got.verified != c.want {
			t.Errorf("link from %.8s verified = %d, want %d", c.reporter, got.verified, c.want)
		}
	}

	lastVerified := func() int {
		t.Helper()
		var v int
		if err := store.db.QueryRow(`SELECT verified FROM neighbor_reports ORDER BY id DESC LIMIT 1`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	// The same confirmed pubkey through a source without verifiesObservers:
	// unverified, and the link it refreshes is downgraded with it.
	later := `{"timestamp":"2026-07-26T11:00:00Z","origin_id":"` + nrOriginUpper + `","neighbors":[
		{"pubkey":"` + nrNbA + `","snr":1,"heard_secs_ago":1,"status":"timeout"}]}`
	ingestReportFrom(t, store, false, nrOriginUpper, later)
	if v := lastVerified(); v != 0 {
		t.Errorf("unflagged source: report verified = %d, want 0", v)
	}
	if got, _ := readLink(t, store, nrOrigin, nrNbA); got.verified != 0 {
		t.Errorf("unflagged source: link verified = %d, want 0", got.verified)
	}

	// No confirmed set at all: unverified, even through a flagged source.
	store.confirmedObservers = nil
	ingestReportFrom(t, store, true, nrOriginUpper, report(nrOriginUpper))
	if v := lastVerified(); v != 0 {
		t.Errorf("nil confirmed set: verified = %d, want 0", v)
	}
}

// The MQTT source's verifiesObservers flag reaches the report through
// handleMessage: the same confirmed pubkey is verified only via a flagged
// source.
func TestNeighborsReportVerifiedNeedsFlaggedSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	execUsersDB(t, path, testObserverBindingsDDL,
		`INSERT INTO observer_bindings VALUES ('`+nrOriginUpper+`', 1, 10, 'auto', 10)`)
	store := openNeighborsStore(t)
	cs := newConfirmedObserverSet(path)
	t.Cleanup(cs.Close)
	cs.refresh()
	store.confirmedObservers = cs

	var cfg Config
	if err := json.Unmarshal([]byte(`{"mqttSources":[{"name":"hooked","broker":"tcp://x:1883","topics":["meshcore/#"],"verifiesObservers":true},{"name":"other","broker":"tcp://y:1883","topics":["meshcore/#"]}]}`), &cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.MQTTSources[0].VerifiesObservers || cfg.MQTTSources[1].VerifiesObservers {
		t.Fatalf("verifiesObservers parsed as %v, %v; want true, false (default)", cfg.MQTTSources[0].VerifiesObservers, cfg.MQTTSources[1].VerifiesObservers)
	}

	topic := "meshcore/BRU/" + nrOriginUpper + "/neighbors"
	for i, c := range []struct {
		source MQTTSource
		want   int
	}{{cfg.MQTTSources[0], 1}, {cfg.MQTTSources[1], 0}} {
		payload := fmt.Sprintf(`{"timestamp":"2026-07-26T1%d:00:00Z","origin_id":"%s","neighbors":[{"pubkey":"%s","snr":1,"heard_secs_ago":1,"status":"timeout"}]}`, i, nrOriginUpper, nrNbA)
		handleMessage(store, c.source.Name, c.source, newMockMsg(topic, payload), nil, nil, &Config{})
		var v int
		if err := store.db.QueryRow(`SELECT verified FROM neighbor_reports ORDER BY id DESC LIMIT 1`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		if v != c.want {
			t.Errorf("source %s: verified = %d, want %d", c.source.Name, v, c.want)
		}
	}
}

func TestConfirmedObserverQueryMatchesUsersSchema(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "internal", "users", "schema.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"CREATE TABLE observer_bindings (", "ADD COLUMN confirmed_at INTEGER", "user_id INTEGER"} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("internal/users/schema.go no longer has %q; confirmedObserversQuery reads it", want)
		}
	}
}

func TestNeighborsReportDefaultFloodScope(t *testing.T) {
	store := openNeighborsStore(t)
	seedNode(t, store, nrOrigin)
	ingestReport(t, store, nrOriginUpper, sampleOpenHop)

	read := func(table string) (sql.NullString, sql.NullString) {
		var sc, at sql.NullString
		if err := store.db.QueryRow(`SELECT default_flood_scope, default_flood_scope_at FROM `+table+` WHERE public_key = ?`, nrOrigin).Scan(&sc, &at); err != nil {
			t.Fatal(err)
		}
		return sc, at
	}
	for _, table := range []string{"nodes", "inactive_nodes"} {
		if sc, at := read(table); sc.String != "#be" || at.String != "2026-07-26T08:00:00Z" {
			t.Errorf("%s default_flood_scope = %v at %v, want #be at the report time", table, sc, at)
		}
	}
	// An older report does not overwrite; a newer one does.
	if err := store.UpdateNodeDefaultFloodScope(nrOrigin, "*", "2026-07-25T08:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if sc, _ := read("nodes"); sc.String != "#be" {
		t.Errorf("older report overwrote default_flood_scope: %v", sc)
	}
	if err := store.UpdateNodeDefaultFloodScope(nrOrigin, "*", "2026-07-27T08:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if sc, _ := read("nodes"); sc.String != "*" {
		t.Errorf("newer report did not update default_flood_scope: %v", sc)
	}
	// Oversized values are not stored.
	if err := store.UpdateNodeDefaultFloodScope(nrOrigin, strings.Repeat("x", maxConfiguredScopeLen+1), "2026-07-28T08:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if sc, _ := read("nodes"); sc.String != "*" {
		t.Errorf("oversized default_flood_scope stored")
	}
	// The firmware sample has no default_scope: nothing changes.
	ingestReport(t, store, nrOriginUpper, sample1865)
	if sc, _ := read("nodes"); sc.String != "*" {
		t.Errorf("report without default_scope changed it: %v", sc)
	}
}

func TestNeighborsReportBadFields(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	fixReportNow(t, now)
	store := openNeighborsStore(t)

	ingestReport(t, store, nrOriginUpper, `{"timestamp":"2026-07-26T11:00:00Z","origin_id":"`+nrOriginUpper+`","total_neighbors":"lots","truncated":"yes","neighbors":[
		{"pubkey":"`+nrNbA+`","snr":"loud","heard_secs_ago":-50,"status":7},
		{"pubkey":"`+nrNbB+`","heard_secs_ago":99999999,"status":"responded"},
		{"pubkey":"`+nrNbC+`","snr":3,"heard_secs_ago":"recent","status":"timeout"},
		{"pubkey":"../etc/passwd","snr":3,"heard_secs_ago":1,"status":"timeout"},
		"not an object",
		{"pubkey":"`+strings.Repeat("e", 64)+`","snr":1,"status":"`+strings.Repeat("s", 100)+`"},
		{"pubkey":"`+strings.Repeat("f", 64)+`","snr":1,"heard_secs_ago":1,"status":"`+strings.Repeat("s", 100)+`"}
	]}`)

	a, ok := readLink(t, store, nrOrigin, nrNbA)
	if !ok || a.snr.Valid || a.heardAt != "2026-07-26T11:00:00Z" || a.status != "unknown" {
		t.Errorf("neighbour A (bad snr, negative age, numeric status) = %+v ok=%v", a, ok)
	}
	b, ok := readLink(t, store, nrOrigin, nrNbB)
	if !ok || b.snr.Valid || b.heardAt != "2026-07-19T11:00:00Z" || b.status != "responded" {
		t.Errorf("neighbour B (missing snr, huge age) = %+v ok=%v", b, ok)
	}
	if _, ok := readLink(t, store, nrOrigin, nrNbC); ok {
		t.Error("neighbour with non-numeric heard_secs_ago was stored")
	}
	if _, ok := readLink(t, store, nrOrigin, strings.Repeat("e", 64)); ok {
		t.Error("neighbour without heard_secs_ago was stored")
	}
	if f, ok := readLink(t, store, nrOrigin, strings.Repeat("f", 64)); !ok || f.status != "unknown" {
		t.Errorf("oversized status = %+v ok=%v, want unknown", f, ok)
	}
	if n := countRows(t, store, "measured_links"); n != 3 {
		t.Errorf("measured_links rows = %d, want 3", n)
	}
	var total, truncated sql.NullInt64
	if err := store.db.QueryRow(`SELECT total, truncated FROM neighbor_reports`).Scan(&total, &truncated); err != nil {
		t.Fatal(err)
	}
	if total.Valid || truncated.Valid {
		t.Errorf("non-numeric counter / non-bool truncated stored: %v %v", total, truncated)
	}

	// Missing timestamp: ingest time.
	ingestReport(t, store, nrOriginUpper, `{"origin_id":"`+nrOriginUpper+`","neighbors":[
		{"pubkey":"`+strings.Repeat("1", 64)+`","snr":1,"heard_secs_ago":60,"status":"timeout"}]}`)
	if got, _ := readLink(t, store, nrOrigin, strings.Repeat("1", 64)); got.reportedAt != "2026-07-26T12:00:00Z" || got.heardAt != "2026-07-26T11:59:00Z" {
		t.Errorf("no timestamp: link = %+v, want ingest time", got)
	}

	// Unparseable timestamp: ingest time too, logged once per reporter.
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	for i := 0; i < 2; i++ {
		ingestReport(t, store, nrOriginUpper, `{"timestamp":"yesterday","origin_id":"`+nrOriginUpper+`","neighbors":[
			{"pubkey":"`+strings.Repeat("5", 64)+`","snr":1,"heard_secs_ago":0,"status":"timeout"}]}`)
	}
	if got, _ := readLink(t, store, nrOrigin, strings.Repeat("5", 64)); got.reportedAt != "2026-07-26T12:00:00Z" {
		t.Errorf("unparseable timestamp: reported_at = %q, want ingest time", got.reportedAt)
	}
	if n := strings.Count(buf.String(), `[neighbors] feedca4a: unparseable timestamp "yesterday", using ingest time`); n != 1 {
		t.Errorf("unparseable timestamp logged %d times, want once:\n%s", n, buf.String())
	}

	// A report stamped far in the future writes nothing.
	before := countRows(t, store, "neighbor_reports")
	ingestReport(t, store, nrOriginUpper, `{"timestamp":"2027-07-26T12:00:00Z","origin_id":"`+nrOriginUpper+`","neighbors":[
		{"pubkey":"`+strings.Repeat("2", 64)+`","snr":1,"heard_secs_ago":1,"status":"timeout"}]}`)
	if countRows(t, store, "neighbor_reports") != before {
		t.Error("future-stamped report journalled")
	}
	if _, ok := readLink(t, store, nrOrigin, strings.Repeat("2", 64)); ok {
		t.Error("future-stamped report stored a link")
	}

	// A topic id that is not a node key cannot own links.
	ingestReport(t, store, "obs", `{"timestamp":"2026-07-26T11:00:00Z","neighbors":[
		{"pubkey":"`+strings.Repeat("3", 64)+`","snr":1,"heard_secs_ago":1,"status":"timeout"}]}`)
	var n int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM measured_links WHERE neighbor = ?`, strings.Repeat("3", 64)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("report from a non-pubkey topic id stored a link")
	}

	// Oversized payload: the report row is journalled without payload, and
	// no links are stored.
	var sb strings.Builder
	sb.WriteString(`{"timestamp":"2026-07-26T11:30:00Z","origin_id":"` + nrOriginUpper + `","neighbors":[`)
	sb.WriteString(`{"pubkey":"` + strings.Repeat("4", 64) + `","snr":1,"heard_secs_ago":1,"status":"timeout"}`)
	sb.WriteString(`],"pad":"` + strings.Repeat("p", maxNeighborReportPayload) + `"}`)
	ingestReport(t, store, nrOriginUpper, sb.String())
	if _, ok := readLink(t, store, nrOrigin, strings.Repeat("4", 64)); ok {
		t.Error("oversized report stored a link")
	}
	var payload string
	if err := store.db.QueryRow(`SELECT payload FROM neighbor_reports ORDER BY id DESC LIMIT 1`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if payload != "" {
		t.Errorf("oversized payload stored (%d bytes)", len(payload))
	}
}

// A report that lists its own reporter as a neighbour stores no self-loop
// link, in any case of the key, and keeps the other neighbours.
func TestNeighborsReportSkipsSelfLoop(t *testing.T) {
	fixReportNow(t, time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC))
	store := openNeighborsStore(t)
	ingestReport(t, store, nrOriginUpper, `{"timestamp":"2026-07-26T11:00:00Z","origin_id":"`+nrOriginUpper+`","neighbors":[
		{"pubkey":"`+nrOriginUpper+`","snr":9,"heard_secs_ago":1,"status":"responded"},
		{"pubkey":"`+nrOrigin+`","snr":9,"heard_secs_ago":1,"status":"responded"},
		{"pubkey":"`+nrNbA+`","snr":1,"heard_secs_ago":1,"status":"timeout"}]}`)
	if _, ok := readLink(t, store, nrOrigin, nrOrigin); ok {
		t.Error("self-loop link stored")
	}
	if n := countRows(t, store, "measured_links"); n != 1 {
		t.Errorf("measured_links rows = %d, want 1", n)
	}
}

func TestNeighborsReportCapsNeighbours(t *testing.T) {
	store := openNeighborsStore(t)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	var sb strings.Builder
	sb.WriteString(`{"timestamp":"2026-07-26T11:00:00Z","origin_id":"` + nrOriginUpper + `","neighbors":[`)
	for i := 0; i < 600; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"pubkey":"%064x","heard_secs_ago":1}`, i+1)
	}
	sb.WriteString(`]}`)
	if sb.Len() > maxNeighborReportPayload {
		t.Fatalf("fixture is %d bytes, over the payload cap", sb.Len())
	}
	ingestReport(t, store, nrOriginUpper, sb.String())
	ingestReport(t, store, nrOriginUpper, sb.String())

	if n := countRows(t, store, "measured_links"); n != maxNeighborsPerReport {
		t.Errorf("measured_links rows = %d, want %d", n, maxNeighborsPerReport)
	}
	if n := store.Stats.NeighborLinksDropped.Load(); n != 2*(600-maxNeighborsPerReport) {
		t.Errorf("dropped = %d, want %d", n, 2*(600-maxNeighborsPerReport))
	}
	if n := strings.Count(buf.String(), "report lists 600 neighbours"); n != 1 {
		t.Errorf("overflow logged %d times, want once per reporter:\n%s", n, buf.String())
	}
}

func TestNeighborsReportFractionalOffsetTimestamp(t *testing.T) {
	store := openNeighborsStore(t)
	ingestReport(t, store, nrOriginUpper, `{"timestamp":"2026-07-26T08:00:00.123456+00:00","origin_id":"`+nrOriginUpper+`","neighbors":[
		{"pubkey":"`+nrNbA+`","snr":1,"heard_secs_ago":0,"status":"timeout"}]}`)
	if got, _ := readLink(t, store, nrOrigin, nrNbA); got.reportedAt != "2026-07-26T08:00:00Z" {
		t.Errorf("link reported_at = %q, want 2026-07-26T08:00:00Z", got.reportedAt)
	}
	var at string
	if err := store.db.QueryRow(`SELECT reported_at FROM neighbor_reports`).Scan(&at); err != nil {
		t.Fatal(err)
	}
	if at != "2026-07-26T08:00:00Z" {
		t.Errorf("report reported_at = %q, want 2026-07-26T08:00:00Z", at)
	}
}

func TestConfirmedObserverSetMissingDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	s := newConfirmedObserverSet(path)
	defer s.Close()
	s.refresh()
	if s.Has(nrOriginUpper) {
		t.Fatal("missing users.db verified a reporter")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the ingestor created users.db")
	}
	// A users.db without observer_bindings (older server): not verified either.
	writeUsersDB(t, path)
	s2 := newConfirmedObserverSet(path)
	defer s2.Close()
	s2.refresh()
	if s2.Has(nrOriginUpper) {
		t.Fatal("users.db without observer_bindings verified a reporter")
	}
}

// Without a verifiesObservers source nothing can be verified, so the ingestor
// builds no confirmed-observer set and never reads users.db for it.
func TestConfirmedObserverFilterNeedsFlaggedSource(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{DBPath: filepath.Join(dir, "meshcore.db"), MQTTSources: []MQTTSource{{Name: "a"}, {Name: "b"}}}
	if s := newConfirmedObserverFilter(cfg); s != nil {
		s.Close()
		t.Fatal("no flagged source: got a confirmed-observer set, want nil")
	}
	cfg.MQTTSources[1].VerifiesObservers = true
	s := newConfirmedObserverFilter(cfg)
	if s == nil {
		t.Fatal("flagged source: got nil, want a confirmed-observer set")
	}
	defer s.Close()
	if s.path != cfg.UsersDBPath() {
		t.Errorf("set reads %q, want %q", s.path, cfg.UsersDBPath())
	}
}

func TestPruneMeasuredLinksAndReports(t *testing.T) {
	store := openNeighborsStore(t)
	old := time.Now().UTC().Add(-10 * 24 * time.Hour).Format(time.RFC3339)
	recent := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	for i, at := range []string{old, recent} {
		if err := store.InsertNeighborReport(NeighborReport{Reporter: nrOrigin, ReceivedAt: at, ReportedAt: at, Payload: "{}"}); err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertMeasuredLink(MeasuredLink{Reporter: nrOrigin, Neighbor: []string{nrNbA, nrNbB}[i], HeardAt: at, ReportedAt: at, Status: "timeout"}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := store.PruneMeasuredLinks(7)
	if err != nil || n != 1 {
		t.Fatalf("PruneMeasuredLinks = %d, %v; want 1", n, err)
	}
	if _, ok := readLink(t, store, nrOrigin, nrNbB); !ok {
		t.Error("recent link pruned")
	}
	if n, err := store.PruneNeighborReports(7); err != nil || n != 1 {
		t.Fatalf("PruneNeighborReports = %d, %v; want 1", n, err)
	}
	if countRows(t, store, "neighbor_reports") != 1 {
		t.Error("recent report pruned")
	}
	if n, _ := store.PruneMeasuredLinks(0); n != 0 {
		t.Error("days 0 pruned rows")
	}

	var cfg Config
	if cfg.NeighborReportsDaysOrDefault() != 90 || cfg.MeasuredLinkDaysOrDefault() != 7 {
		t.Errorf("defaults = %d/%d, want 90/7", cfg.NeighborReportsDaysOrDefault(), cfg.MeasuredLinkDaysOrDefault())
	}
	cfg.Retention = &RetentionConfig{NeighborReportsDays: 30, MeasuredLinkDays: 3}
	if cfg.NeighborReportsDaysOrDefault() != 30 || cfg.MeasuredLinkDaysOrDefault() != 3 {
		t.Errorf("configured = %d/%d, want 30/3", cfg.NeighborReportsDaysOrDefault(), cfg.MeasuredLinkDaysOrDefault())
	}
}
