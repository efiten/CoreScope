package main

import "testing"

func TestDeclaredRegionsCurrentIsLatestObservation(t *testing.T) {
	s := newTestStore(t)
	ins := func(at, csv string) {
		if _, err := s.InsertClientDeclaredRegions(&ClientDeclaredRegions{
			Target: "bb11", RxPubkey: "aa11", ObservedAt: at, IngestedAt: "2026-08-18T00:00:00.000Z",
			RegionsCSV: csv,
		}); err != nil {
			t.Fatalf("insert %s: %v", at, err)
		}
	}
	// Deliberately inserted newest-first, so a query ordering by rowid or by
	// ingest order would pick the wrong one.
	ins("2026-08-18T10:00:00.000Z", "be,be-vlg,be-van")
	ins("2026-08-17T10:00:00.000Z", "be,be-vlg")

	cur, err := s.CurrentDeclaredRegions("bb11")
	if err != nil {
		t.Fatal(err)
	}
	if cur.RegionsCSV != "be,be-vlg,be-van" {
		t.Errorf("current = %q, want the 18th's list — a late-arriving older drive must not win", cur.RegionsCSV)
	}
}

func TestDeclaredRegionsIdempotent(t *testing.T) {
	s := newTestStore(t)
	o := &ClientDeclaredRegions{Target: "bb11", RxPubkey: "aa11",
		ObservedAt: "2026-08-18T10:00:00.000Z", IngestedAt: "2026-08-18T10:00:01.000Z", RegionsCSV: "be"}
	if ins, err := s.InsertClientDeclaredRegions(o); err != nil || !ins {
		t.Fatalf("first: ins=%v err=%v", ins, err)
	}
	if ins, err := s.InsertClientDeclaredRegions(o); err != nil || ins {
		t.Fatalf("duplicate must be a no-op: ins=%v err=%v", ins, err)
	}
}

// TestDeclaredRegionsEmptyListRoundTrips proves an empty regions_csv is a
// valid, retrievable observation — "a repeater with nothing flood-allowed
// genuinely declares nothing, and that gets a row." Nothing in the insert or
// query path may special-case the empty string into a NULL or a dropped row;
// this test would fail against a later "helpful" `if regionsCSV == "" {
// return }` guard.
func TestDeclaredRegionsEmptyListRoundTrips(t *testing.T) {
	s := newTestStore(t)
	o := &ClientDeclaredRegions{Target: "bb11", RxPubkey: "aa11",
		ObservedAt: "2026-08-18T10:00:00.000Z", IngestedAt: "2026-08-18T10:00:01.000Z", RegionsCSV: ""}
	if ins, err := s.InsertClientDeclaredRegions(o); err != nil || !ins {
		t.Fatalf("insert: ins=%v err=%v", ins, err)
	}

	cur, err := s.CurrentDeclaredRegions("bb11")
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil {
		t.Fatal("current = nil, want a row for the empty-list observation")
	}
	if cur.RegionsCSV != "" {
		t.Errorf("RegionsCSV = %q, want empty string — an empty list is a valid observation, not absence", cur.RegionsCSV)
	}
}

// TestDeclaredRegionsDifferentReportersPersistBoth pins the UNIQUE
// constraint's shape from the permitting side: two different companions
// (rx_pubkey) reporting the same target's regions at the same observed_at
// are two distinct observations and must both persist. Only a genuine
// duplicate delivery from the SAME reporter (same target, rx_pubkey AND
// observed_at — see TestDeclaredRegionsIdempotent) collapses. A later
// constraint change to (target, observed_at) would fail this test loudly
// instead of silently discarding the second reporter's reading.
func TestDeclaredRegionsDifferentReportersPersistBoth(t *testing.T) {
	s := newTestStore(t)
	at := "2026-08-18T10:00:00.000Z"
	for _, rx := range []string{"aa11", "aa22"} {
		if _, err := s.InsertClientDeclaredRegions(&ClientDeclaredRegions{
			Target: "bb11", RxPubkey: rx, ObservedAt: at, IngestedAt: at, RegionsCSV: "be",
		}); err != nil {
			t.Fatalf("insert %s: %v", rx, err)
		}
	}

	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM node_declared_regions WHERE target = ? AND observed_at = ?`, "bb11", at).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("rows = %d, want 2 — two different reporters observing the same target at the same time must both persist", n)
	}
}

// TestCurrentDeclaredRegionsNormalizesTargetCase proves CurrentDeclaredRegions
// lowercases its target argument before querying — mirroring every write
// path's normalization. Without it, a target pasted in uppercase (e.g. from a
// URL) silently returns zero rows and no error, exactly the sibling failure
// this table's read query must not repeat.
func TestCurrentDeclaredRegionsNormalizesTargetCase(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.InsertClientDeclaredRegions(&ClientDeclaredRegions{
		Target: "bb11", RxPubkey: "aa11",
		ObservedAt: "2026-08-18T10:00:00.000Z", IngestedAt: "2026-08-18T10:00:01.000Z", RegionsCSV: "be",
	}); err != nil {
		t.Fatal(err)
	}

	cur, err := s.CurrentDeclaredRegions("BB11")
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil {
		t.Fatal("current = nil, want a hit for uppercase target BB11")
	}
	if cur.RegionsCSV != "be" {
		t.Errorf("current.RegionsCSV = %q, want %q", cur.RegionsCSV, "be")
	}
}
