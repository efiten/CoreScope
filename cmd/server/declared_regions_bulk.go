package main

import (
	"net/http"
	"sort"
)

// GET /api/nodes/declared-regions: every repeater's newest declared region
// list in one compact response, for a mobile client choosing the flood scope
// of what it sends (CoreDrive RX uplink pings). The per-node fields on
// /api/nodes carry the same answer, but a client would have to page through
// every node to collect them.
//
// regions are the flood-allowed named regions without "#" (splitDeclaredRegions);
// wildcard says the repeater also floods unscoped packets ("*").

type DeclaredRegionsEntry struct {
	PubKey    string   `json:"pubkey"`
	Regions   []string `json:"regions"`
	Wildcard  bool     `json:"wildcard"`
	Truncated bool     `json:"truncated,omitempty"`
}

type DeclaredRegionsResponse struct {
	Nodes []DeclaredRegionsEntry `json:"nodes"`
}

func declaredRegionsEntries(answers map[string]declaredAnswer) []DeclaredRegionsEntry {
	out := make([]DeclaredRegionsEntry, 0, len(answers))
	for pk, a := range answers {
		named, wildcard := splitDeclaredRegions(a.RegionsCSV)
		out = append(out, DeclaredRegionsEntry{PubKey: pk, Regions: named, Wildcard: wildcard, Truncated: a.Truncated})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PubKey < out[j].PubKey })
	return out
}

// handleDeclaredRegions answers an empty list when this database carries no
// declared source, or when the lookup failed (logged by declaredRegionsCSV):
// a client then keeps its own fallback scope.
func (s *Server) handleDeclaredRegions(w http.ResponseWriter, r *http.Request) {
	if s.db == nil || s.db.conn == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	answers, _ := s.declaredRegionsCSV()
	writeJSON(w, DeclaredRegionsResponse{Nodes: declaredRegionsEntries(answers)})
}
