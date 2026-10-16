package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestDeclaredRegionsEndpoint(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	router.HandleFunc("/api/nodes/declared-regions", srv.handleDeclaredRegions).Methods("GET")
	get := func() DeclaredRegionsResponse {
		t.Helper()
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest("GET", "/api/nodes/declared-regions", nil))
		if rr.Code != 200 {
			t.Fatalf("status %d", rr.Code)
		}
		var resp DeclaredRegionsResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}
	if resp := get(); resp.Nodes == nil || len(resp.Nodes) != 0 {
		t.Fatalf("no declared source: %+v, want an empty list", resp)
	}

	createDeclaredRegionsTable(t, srv)
	mustExecDB(t, srv.db, `INSERT INTO node_declared_regions (target, rx_pubkey, observed_at, ingested_at, regions_csv, truncated) VALUES
		('AABB01', 'c1', '2026-10-16T10:00:00Z', 'x', '*,be,nl', 0),
		('aabb01', 'c2', '2026-10-16T12:00:00Z', 'x', '*,#nl,nl-li', 1),
		('ccdd02', 'c1', '2026-10-16T11:00:00Z', 'x', 'be', 0)`)
	srv.declaredRegionsCache = nil
	resp := get()
	if len(resp.Nodes) != 2 {
		t.Fatalf("entries = %+v, want one per repeater", resp.Nodes)
	}
	a, c := resp.Nodes[0], resp.Nodes[1]
	if a.PubKey != "aabb01" || !a.Wildcard || !a.Truncated || len(a.Regions) != 2 || a.Regions[0] != "nl" || a.Regions[1] != "nl-li" {
		t.Errorf("aabb01 = %+v, want its newest answer, lowercased key, # stripped, * as wildcard", a)
	}
	if c.PubKey != "ccdd02" || c.Wildcard || len(c.Regions) != 1 || c.Regions[0] != "be" {
		t.Errorf("ccdd02 = %+v, want be only, no wildcard", c)
	}
}
