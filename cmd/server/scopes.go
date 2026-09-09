package main

// Per-node scope conformance: GET /api/nodes/{pubkey}/scopes. Fork-only, and
// deliberately separate from the network-wide audit in scope_audit.go: this
// one answers "what did THIS repeater forward", one repeater at a time, with
// an EXISTS-correlated query rather than a single full-window scan.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
)

// RouteTypeMix is the route-type breakdown of packets this node was
// observed FORWARDING — i.e. packets carrying this pubkey as any path hop of
// a FLOOD-family route (RouteTransportFlood, RouteFlood). On those routes
// every forwarder APPENDS its own hash to the end of the path
// (internal/packetpath/route.go), so each hop is a node that transmitted the
// packet; the last hop is merely the one an uplinked observer heard directly.
//
// It does NOT mean "packets in which this node appears anywhere in the path",
// because DIRECT and TRANSPORT_DIRECT routes are excluded entirely: those
// consume hops from the FRONT, so their path is the route's remaining plan
// rather than a record of who transmitted, and crediting any of their hops
// would attribute forwarding this node never did. Direct and TransportDirect
// are therefore always zero by construction — the route-type filter
// (scopeConformanceForwarderRouteTypesSQL) can never match them.
type RouteTypeMix struct {
	TransportFlood  int64 `json:"transportFlood"`
	Flood           int64 `json:"flood"`
	Direct          int64 `json:"direct"`
	TransportDirect int64 `json:"transportDirect"`
}

// ScopeConformance is the per-repeater answer to "which region scopes does
// this node forward, and how does that compare to what it should forward".
//
// The three scope states below are kept distinct on purpose and must never
// be folded together — see scopeNameForDB in the ingestor, which is the
// single source of truth for this encoding:
//   - a row lands in Observed when transmissions.scope_name holds a matched
//     region name.
//   - Unmatched counts rows where code1 carried a transport scope but no
//     configured region key matched it (scope_name stored as an empty
//     string) — this is information: a neighbouring region exists that this
//     CoreScope instance holds no key for.
//   - Unscoped counts rows where code1 was the all-zero "no scope" value
//     (scope_name stored as SQL NULL) — the packet carried no scope at all.
type ScopeConformance struct {
	Observed  []ScopeObservation `json:"observed"`
	Unmatched int64              `json:"unmatched"`
	Unscoped  int64              `json:"unscoped"`
	Routes    RouteTypeMix       `json:"routes"`
}

// scopeConformanceQuery finds every transmission this pubkey forwarded (any
// path hop on a FLOOD-family route matches the pubkey), bounded by the since
// window so the scan stays an index range on first_seen rather than a full
// table scan.
//
// It reads every hop rather than only path[last] because on a flood route
// every hop appended itself after forwarding. Attributing only path[last]
// answered a narrower question — "which of this node's forwards did an
// uplinked observer hear directly" — and for a node with no observer in RF
// range the answer is nothing at all: measured on the live instance, that
// restriction kept 14% of hop observations network-wide and left 65% of
// declared repeaters with no evidence of any kind, while the same nodes'
// transported_scopes (byPathHop, every hop) listed scopes from the same
// database. See docs/specs/2026-09-07-auto-region-keys-design.md, M0.
//
// Two case/length mismatches make this join easy to get silently wrong
// instead of erroring:
//
//   - The decoder emits path hops uppercase (packetpath.DecodePathFromRawHex
//     does strings.ToUpper on every hop) into the classic observations.path_json
//     column, so the comparison lower-cases the extracted hop rather than
//     requiring the caller to match that case.
//   - path_json hops are truncated hashes (1-4 bytes per the packet's own
//     hash_size), never full pubkeys, while callers pass a full 64-char
//     pubkey (e.g. from a /api/nodes/{pubkey}/... URL). An exact-equality
//     join would therefore match nothing for any real node. The join instead
//     treats the hop as a PREFIX match against the caller's pubkey — the hop
//     matches when it equals the pubkey's own first len(hop) characters —
//     and excludes hops shorter than minForwarderHopHexLen as too
//     collision-prone to trust (see its doc comment).
//   - json_valid(o.path_json) is required explicitly: SQLite evaluates the
//     json_each() join independently of the IS NOT NULL predicate in this
//     same WHERE clause, so a single row anywhere in the window with
//     malformed path_json (an empty string or non-JSON text) fails
//     json_each() and errors the ENTIRE query — not just that row — for
//     every pubkey.
var scopeConformanceQuery = `
	SELECT t.scope_name, t.route_type, t.first_seen
	FROM transmissions t
	WHERE t.first_seen >= ?
	  AND ` + scopeConformanceForwarderRouteTypesSQL + `
	  AND EXISTS (
	      SELECT 1
	      FROM observations o
	      JOIN json_each(o.path_json) je
	      WHERE o.transmission_id = t.id
	        AND o.path_json IS NOT NULL
	        AND json_valid(o.path_json)
	        AND json_array_length(o.path_json) > 0
	        AND LENGTH(je.value) >= ` + fmt.Sprint(minForwarderHopHexLen) + `
	        AND LOWER(je.value) = SUBSTR(?, 1, LENGTH(je.value))
	  )
	ORDER BY t.first_seen ASC
`

// ScopeConformance answers "which region scopes has this repeater forwarded,
// and what does its forwarded route-type mix look like", for the window
// starting at sinceISO. Read-only: all writes live in the ingestor.
//
// A repeater never heard forwarding anything in the window is a valid
// question with an empty answer, not an error.
func (s *PacketStore) ScopeConformance(pubkey string, sinceISO string) (*ScopeConformance, error) {
	pubkey = strings.ToLower(strings.TrimSpace(pubkey))

	rows, err := s.db.conn.Query(scopeConformanceQuery, sinceISO, pubkey)
	if err != nil {
		return nil, fmt.Errorf("scope conformance query: %w", err)
	}
	defer rows.Close()

	type scopeAgg struct {
		packets   int64
		firstSeen string
		lastSeen  string
	}
	named := make(map[string]*scopeAgg)
	var namedOrder []string

	result := &ScopeConformance{Observed: []ScopeObservation{}}

	for rows.Next() {
		var scopeName sql.NullString
		var routeType int
		var firstSeen string
		if err := rows.Scan(&scopeName, &routeType, &firstSeen); err != nil {
			return nil, fmt.Errorf("scope conformance scan: %w", err)
		}

		// Branch the three scope states explicitly in Go rather than in SQL
		// string-comparison gymnastics.
		switch {
		case !scopeName.Valid:
			result.Unscoped++
		case scopeName.String == "":
			result.Unmatched++
		default:
			agg, ok := named[scopeName.String]
			if !ok {
				agg = &scopeAgg{firstSeen: firstSeen, lastSeen: firstSeen}
				named[scopeName.String] = agg
				namedOrder = append(namedOrder, scopeName.String)
			}
			agg.packets++
			if firstSeen < agg.firstSeen {
				agg.firstSeen = firstSeen
			}
			if firstSeen > agg.lastSeen {
				agg.lastSeen = firstSeen
			}
		}

		switch routeType {
		case RouteTransportFlood:
			result.Routes.TransportFlood++
		case RouteFlood:
			result.Routes.Flood++
		case RouteDirect:
			result.Routes.Direct++
		case RouteTransportDirect:
			result.Routes.TransportDirect++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scope conformance rows: %w", err)
	}

	for _, name := range namedOrder {
		agg := named[name]
		result.Observed = append(result.Observed, ScopeObservation{
			Scope:     name,
			Packets:   agg.packets,
			FirstSeen: agg.firstSeen,
			LastSeen:  agg.lastSeen,
		})
	}

	return result, nil
}

// CurrentDeclaredRegions returns pubkey's most recently declared region
// list, or nil (not an error) when the repeater has never successfully
// answered, or when node_declared_regions is absent (an older database that
// predates this table).
//
// "Most recent" is ordered by the greatest observed_at, NEVER ingested_at —
// mirrors the ingestor's own CurrentDeclaredRegions exactly: a drive
// buffered offline can arrive days late, and ordering by arrival would let
// that stale reading overwrite a fresher one.
func (db *DB) CurrentDeclaredRegions(pubkey string) (*DeclaredRegions, error) {
	if !db.hasDeclaredRegionsTable {
		return nil, nil
	}
	pubkey = strings.ToLower(strings.TrimSpace(pubkey))

	row := db.conn.QueryRow(`
		SELECT observed_at, regions_csv, truncated
		FROM node_declared_regions
		WHERE target = ?
		ORDER BY observed_at DESC LIMIT 1`, pubkey)
	var observedAt, regionsCSV string
	var truncated int
	if err := row.Scan(&observedAt, &regionsCSV, &truncated); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("current declared regions: %w", err)
	}
	return &DeclaredRegions{
		Regions:    splitRegionsCSV(regionsCSV),
		ObservedAt: observedAt,
		Truncated:  truncated == 1,
	}, nil
}

// NodeScopesResponse is the payload for GET /api/nodes/{pubkey}/scopes: the
// observed-forwarding side (ScopeConformance, embedded BY VALUE so its three
// scope states sit at the JSON top level — unmatched and unscoped are
// separate counts and a matched scope never has an empty name) plus the
// declared side (DeclaredRegions), returned together so the UI needs only
// one request per node rather than a second per-item call.
//
// ScopeConformance is embedded by value rather than as *ScopeConformance:
// encoding/json silently skips fields promoted through a nil embedded
// pointer instead of erroring, which would drop observed/unmatched/unscoped
// from the body entirely rather than surfacing the failure.
type NodeScopesResponse struct {
	PublicKey string `json:"publicKey"`
	Window    string `json:"window"`
	ScopeConformance
	Declared *DeclaredRegions `json:"declared"`
}

// handleNodeScopes serves GET /api/nodes/{pubkey}/scopes?window=1h|24h|7d.
//
// A pubkey never heard forwarding anything is a valid question with an
// empty answer (200), not a 404 — ScopeConformance already treats it that
// way, and this handler performs no node-existence lookup that would
// override it.
func (s *Server) handleNodeScopes(w http.ResponseWriter, r *http.Request) {
	pubkey := strings.ToLower(mux.Vars(r)["pubkey"])
	if !isHexPubkey(pubkey) {
		writeError(w, 400, "invalid pubkey: expected 64 hex chars")
		return
	}
	if s.cfg != nil && s.cfg.IsBlacklisted(pubkey) {
		writeError(w, 404, "Not found")
		return
	}
	if s.isPubkeyHidden(pubkey) {
		writeError(w, 404, "Not found")
		return
	}

	window := r.URL.Query().Get("window")
	if window == "" {
		window = "24h"
	}
	lookback, ok := nodeScopesWindowLookback(window)
	if !ok {
		writeError(w, 400, "window must be 1h, 24h, or 7d")
		return
	}

	// cacheKey includes the blacklist generation so any mutation via
	// SetNodeBlacklist invalidates all prior scopes cache entries on the
	// next request, mirroring handleNodeReach's cache key exactly. The
	// validation above (blacklisted/hidden -> 404) already runs before this
	// point, so that path is never cached.
	var gen uint64
	if s.cfg != nil {
		gen = s.cfg.BlacklistGeneration()
	}
	s.scopesPurgeIfBlacklistGenChanged(gen)
	cacheKey := pubkey + "|" + window + "|g" + strconv.FormatUint(gen, 10)
	if raw, ok := s.scopesCacheGet(cacheKey); ok {
		w.Header().Set("Content-Type", "application/json")
		w.Write(raw)
		return
	}

	// singleflight: collapse a thundering herd on a cold key to one scan.
	v, err, _ := s.scopes.sf.Do(cacheKey, func() (interface{}, error) {
		if raw, ok := s.scopesCacheGet(cacheKey); ok {
			return raw, nil
		}
		sinceISO := time.Now().Add(-lookback).UTC().Format(time.RFC3339)

		conformance := &ScopeConformance{Observed: []ScopeObservation{}}
		if s.store != nil {
			var cErr error
			conformance, cErr = s.store.ScopeConformance(pubkey, sinceISO)
			if cErr != nil {
				return nil, cErr
			}
		}

		declared, dErr := s.db.CurrentDeclaredRegions(pubkey)
		if dErr != nil {
			return nil, dErr
		}

		raw, mErr := json.Marshal(NodeScopesResponse{
			PublicKey:        pubkey,
			Window:           window,
			ScopeConformance: *conformance,
			Declared:         declared,
		})
		if mErr != nil {
			return nil, mErr
		}
		s.scopesCachePut(cacheKey, raw)
		return raw, nil
	})
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	raw, ok := v.([]byte)
	if !ok {
		writeError(w, 500, "internal error: unexpected scopes result type")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw)
}
