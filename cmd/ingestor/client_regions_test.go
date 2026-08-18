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
