package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/mux"
	"golang.org/x/sync/singleflight"
)

// ScopeObservation is one region scope this repeater has been observed
// forwarding traffic for, within the query window.
type ScopeObservation struct {
	Scope     string `json:"scope"` // matched region name (transmissions.scope_name)
	Packets   int64  `json:"packets"`
	FirstSeen string `json:"firstSeen"`
	LastSeen  string `json:"lastSeen"`
}

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

// scopeConformanceForwarderRouteTypesSQL restricts the forwarder join to the
// only route types whose path hops are a record of who transmitted the packet:
// RouteTransportFlood (0) and RouteFlood (1), where every forwarder appends
// its own hash. A DIRECT route consumes hops from the front, so its path is
// the route's remaining plan and its path[last] is the far end rather than a
// forwarder — including RouteDirect (2) / RouteTransportDirect (3) here would
// misattribute a scope to a node that never forwarded the packet.
//
// Since the attribution below reads EVERY hop rather than only path[last],
// this filter is the sole guard against that misattribution. It is pinned by
// TestScopeConformanceIgnoresDirectRoutesMidPath and
// TestScopeAuditForwardingIgnoresDirectRoutes; do not widen it.
const scopeConformanceForwarderRouteTypesSQL = "t.route_type IN (0, 1)"

// minForwarderHopHexLen is the shortest path_json hop ScopeConformance will
// treat as an attribution: 4 hex chars = 2 bytes. Matches deriveHeardKey's
// floor (cmd/ingestor/client_reception.go: "exclude 1-byte (collision-prone),
// matching Reach") — a 1-byte/2-hex-char hash collides too often across a
// real fleet, and attributing a scope to the wrong node is worse than
// attributing none.
const minForwarderHopHexLen = 4

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

// DeclaredRegions is the most recently *declared* region list a repeater
// reported when a mobile app asked it over RF, mirroring the ingestor's
// CurrentDeclaredRegions (cmd/ingestor/client_reception.go). Regions is
// never nil — an empty slice is itself a meaningful answer ("this repeater
// declares nothing flood-allowed"), distinct from *DeclaredRegions being nil
// ("never successfully asked": out of RF range, firmware too old, or the
// request was silently ignored — the repeater only answers DIRECT-routed
// requests, so silence means nothing).
type DeclaredRegions struct {
	Regions    []string `json:"regions"`
	ObservedAt string   `json:"observedAt"`
	Truncated  bool     `json:"truncated"`
}

// splitRegionsCSV parses regions_csv (comma-separated, "#" prefixes already
// stripped by the firmware) into a slice, always non-nil.
func splitRegionsCSV(csv string) []string {
	regions := []string{}
	if csv == "" {
		return regions
	}
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			regions = append(regions, part)
		}
	}
	return regions
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

// DeclaredRegionsRow is one repeater's most recently declared region list,
// as returned in bulk by AllCurrentDeclaredRegions for GET /api/scope-audit.
type DeclaredRegionsRow struct {
	Target     string
	ObservedAt string
	RegionsCSV string
	Truncated  bool
}

// AllCurrentDeclaredRegions returns the latest declared-region row for EVERY
// target that has ever answered, in one indexed query — idx_ndr_target(target,
// observed_at) covers the PARTITION BY/ORDER BY below — rather than one
// CurrentDeclaredRegions call per repeater. "Latest" is the same rule as
// CurrentDeclaredRegions: greatest observed_at, never ingested_at.
//
// A repeater that has never answered is simply absent from the result — it
// is never synthesized as a row declaring nothing, which would be
// indistinguishable from a repeater that genuinely answered with an empty
// list.
func (db *DB) AllCurrentDeclaredRegions() ([]DeclaredRegionsRow, error) {
	if !db.hasDeclaredRegionsTable {
		return nil, nil
	}
	rows, err := db.conn.Query(`
		WITH ranked AS (
			SELECT target, observed_at, regions_csv, truncated,
				ROW_NUMBER() OVER (PARTITION BY target ORDER BY observed_at DESC) AS rn
			FROM node_declared_regions
		)
		SELECT target, observed_at, regions_csv, truncated
		FROM ranked
		WHERE rn = 1
		ORDER BY target
	`)
	if err != nil {
		return nil, fmt.Errorf("all current declared regions: %w", err)
	}
	defer rows.Close()

	var result []DeclaredRegionsRow
	for rows.Next() {
		var d DeclaredRegionsRow
		var truncated int
		if err := rows.Scan(&d.Target, &d.ObservedAt, &d.RegionsCSV, &truncated); err != nil {
			return nil, fmt.Errorf("all current declared regions scan: %w", err)
		}
		d.Truncated = truncated == 1
		result = append(result, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("all current declared regions rows: %w", err)
	}
	return result, nil
}

// scopeAuditNodeIdentity is the name/role display identity for one
// scope-audit row, resolved in bulk (one IN query for every declared
// target) rather than one GetNodeByPubkey call per repeater.
type scopeAuditNodeIdentity struct {
	Name *string
	Role *string
}

// scopeAuditNodeIdentities resolves name/role for pubkeys in a single query.
// A pubkey with no matching nodes row (deleted, pruned) resolves to the zero
// value rather than being an error — GET /api/scope-audit still has a
// PublicKey to identify and link the row.
func (db *DB) scopeAuditNodeIdentities(pubkeys []string) map[string]scopeAuditNodeIdentity {
	result := make(map[string]scopeAuditNodeIdentity, len(pubkeys))
	if len(pubkeys) == 0 {
		return result
	}
	placeholders := make([]string, len(pubkeys))
	args := make([]interface{}, len(pubkeys))
	for i, k := range pubkeys {
		placeholders[i] = "?"
		args[i] = strings.ToLower(k)
	}
	rows, err := db.conn.Query(
		"SELECT public_key, name, role FROM nodes WHERE public_key IN ("+strings.Join(placeholders, ",")+")",
		args...)
	if err != nil {
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var pk string
		var name, role sql.NullString
		if rows.Scan(&pk, &name, &role) != nil {
			continue
		}
		id := scopeAuditNodeIdentity{}
		if name.Valid {
			v := name.String
			id.Name = &v
		}
		if role.Valid {
			v := role.String
			id.Role = &v
		}
		result[strings.ToLower(pk)] = id
	}
	return result
}

// normScope strips a leading '#' so the two sides of the declared/observed
// comparison can be matched at all — mirrors public/node-scopes.js's
// normScope exactly. transmissions.scope_name keeps the '#' (region keys
// are configured as hashRegions: ["#belgium", "#eu"]); regions_csv arrives
// from the firmware with the prefix already stripped. Comparing raw
// silently inverts the whole comparison while looking entirely plausible.
func normScope(s string) string {
	if strings.HasPrefix(s, "#") {
		return s[1:]
	}
	return s
}

// scopeAuditTargetAgg is one declared target's forwarding aggregate for the
// GET /api/scope-audit scan window: named scopes it forwarded (key =
// normScope'd name) plus a count of the plain-FLOOD (unscoped) packets it
// forwarded — the signal the wildcard-contradiction check needs, since '*'
// governs exactly those packets, not any named scope.
type scopeAuditTargetAgg struct {
	scopes          map[string]*ScopeObservation
	unscopedPackets int64
	// ambiguousHops counts forwarder-hop observations in the window whose
	// truncated hash prefix matched this target AND at least one other
	// declared target — see ScopeAuditForwarding's doc comment for why
	// those hops are attributed to neither candidate instead of both.
	ambiguousHops int64
	// unmatchedPackets counts packets this target was observed forwarding
	// that carried a transport scope no configured region key matched
	// (transmissions.scope_name = ""). Deliberately NOT folded into
	// unscopedPackets: those two are opposites. Unscoped means the packet
	// carried no scope at all (scope_name SQL NULL) and is what '*' governs;
	// unmatched means it IS scoped and this instance simply holds no key for
	// that region, so '*' says nothing about it. See scopeNameForDB in the
	// ingestor for the encoding.
	//
	// A non-zero value is a caveat on this target's notObserved entries: any
	// of them may be a region this instance cannot name rather than one the
	// repeater is not forwarding.
	unmatchedPackets int64
}

// scopeAuditPrefixIndex builds, for every even hex length from
// minForwarderHopHexLen up to a full 64-char pubkey, a lowercase prefix ->
// []target map. A forwarder hop of any valid truncated-hash length then
// resolves to its matching declared target(s) via a single map lookup,
// instead of a per-target SQL join — this is what keeps
// ScopeAuditForwarding's underlying scan a single query independent of how
// many repeaters have declared a region list.
func scopeAuditPrefixIndex(targets []string) map[int]map[string][]string {
	byLen := make(map[int]map[string][]string)
	for _, t := range targets {
		t = strings.ToLower(t)
		for l := minForwarderHopHexLen; l <= len(t); l += 2 {
			m, ok := byLen[l]
			if !ok {
				m = map[string][]string{}
				byLen[l] = m
			}
			prefix := t[:l]
			m[prefix] = append(m[prefix], t)
		}
	}
	return byLen
}

// scopeAuditForwarderScanQuery is the single full-window scan behind
// GET /api/scope-audit. Unlike scopeConformanceQuery (one EXISTS-correlated
// call per pubkey — fine for one node, but 37+ repeater-sized loop of them
// would each re-scan the same first_seen index range), this scans the
// FLOOD-family window exactly once and returns every forwarder hop found.
//
// "Every forwarder hop" means every hop of the path, not only path[last] —
// see scopeConformanceQuery's doc comment for why, and note that this query
// returns one ROW PER HOP, so a single transmission now yields as many rows as
// it has hops (mean 7.08 on the live network). ScopeAuditForwarding's
// "<target>|<txID>" de-duplication is what keeps that from counting a
// transmission twice for one target, and it is now load-bearing rather than
// belt-and-braces: one path can carry the same target on several hops.
// It applies the SAME three conditions scopeConformanceQuery does —
// minForwarderHopHexLen, scopeConformanceForwarderRouteTypesSQL, and the
// explicit json_valid guard against a single malformed path_json row
// erroring the whole query — but does not join against any target list:
// attribution to a specific declared target happens in Go
// (ScopeAuditForwarding), against the small in-memory prefix index built by
// scopeAuditPrefixIndex, so the SQL cost stays O(rows in window) regardless
// of len(targets).
var scopeAuditForwarderScanQuery = `
	SELECT t.id, je.value, t.scope_name, t.first_seen
	FROM transmissions t
	JOIN observations o ON o.transmission_id = t.id
	JOIN json_each(o.path_json) je
	WHERE t.first_seen >= ?
	  AND ` + scopeConformanceForwarderRouteTypesSQL + `
	  AND o.path_json IS NOT NULL
	  AND json_valid(o.path_json)
	  AND json_array_length(o.path_json) > 0
	  AND LENGTH(je.value) >= ` + fmt.Sprint(minForwarderHopHexLen) + `
`

// ScopeAuditForwarding runs scopeAuditForwarderScanQuery once for the whole
// window and attributes every forwarder hop it finds to targets, by the same
// truncated-hash prefix match ScopeConformance uses for a single pubkey.
//
// A hop is attributed only when its prefix matches EXACTLY ONE declared
// target. This endpoint exists to find a repeater that declares a region and
// is not actually forwarding it — crediting a hop to every target sharing
// its prefix would let a colliding neighbour's traffic silently paper over a
// real gap, and crediting nobody (the alternative of dropping the hop
// entirely) would invent failures for targets that simply share a collision-
// prone prefix. Instead, an ambiguous hop is credited to NEITHER candidate,
// and every candidate's ambiguousHops counter is incremented instead, so the
// row can say "this notObserved might just be a prefix collision" rather
// than presenting it as a confirmed finding. See scopeAuditTargetAgg's
// ambiguousHops field and ScopeAuditRow.AmbiguousHops.
//
// Each (target, transmission) pair is counted at most once even if seen via
// multiple observations OR via several hops of one path (a routing loop, or two
// hops colliding on the same truncated prefix), for both the attributed and the
// ambiguous count — the same de-duplication scopeConformanceQuery gets for free
// from EXISTS, done explicitly here since this scan is not correlated per
// target. Since the scan reads every hop rather than only path[last], this is
// the only thing keeping one transmission from counting several times for the
// same target; TestScopeAuditForwardingCountsOneTransmissionOncePerTarget pins
// it.
func (s *PacketStore) ScopeAuditForwarding(sinceISO string, targets []string) (map[string]*scopeAuditTargetAgg, error) {
	byLen := scopeAuditPrefixIndex(targets)
	result := make(map[string]*scopeAuditTargetAgg, len(targets))

	rows, err := s.db.conn.Query(scopeAuditForwarderScanQuery, sinceISO)
	if err != nil {
		return nil, fmt.Errorf("scope audit forwarder scan: %w", err)
	}
	defer rows.Close()

	getAgg := func(target string) *scopeAuditTargetAgg {
		agg, ok := result[target]
		if !ok {
			agg = &scopeAuditTargetAgg{scopes: map[string]*ScopeObservation{}}
			result[target] = agg
		}
		return agg
	}

	seen := make(map[string]bool) // "<target>|<txID>" already counted (attributed or ambiguous)
	for rows.Next() {
		var txID int64
		var hop string
		var scopeName sql.NullString
		var firstSeen string
		if err := rows.Scan(&txID, &hop, &scopeName, &firstSeen); err != nil {
			return nil, fmt.Errorf("scope audit forwarder scan scan: %w", err)
		}
		hop = strings.ToLower(hop)
		candidates := byLen[len(hop)][hop]

		if len(candidates) > 1 {
			for _, target := range candidates {
				key := target + "|" + strconv.FormatInt(txID, 10)
				if seen[key] {
					continue
				}
				seen[key] = true
				getAgg(target).ambiguousHops++
			}
			continue
		}
		for _, target := range candidates {
			key := target + "|" + strconv.FormatInt(txID, 10)
			if seen[key] {
				continue
			}
			seen[key] = true

			agg := getAgg(target)
			if !scopeName.Valid {
				agg.unscopedPackets++
				continue
			}
			if scopeName.String == "" {
				// Unmatched: transport-scoped, but no configured region key
				// matched code1. Still not part of the declared/observed
				// comparison — it names no region, so it can never satisfy a
				// declaration — but it is the evidence that a notObserved
				// finding on this row may be a gap in this instance's
				// hashRegions rather than in the repeater's forwarding.
				agg.unmatchedPackets++
				continue
			}
			name := normScope(scopeName.String)
			so, ok := agg.scopes[name]
			if !ok {
				so = &ScopeObservation{Scope: name, FirstSeen: firstSeen, LastSeen: firstSeen}
				agg.scopes[name] = so
			}
			so.Packets++
			if firstSeen < so.FirstSeen {
				so.FirstSeen = firstSeen
			}
			if firstSeen > so.LastSeen {
				so.LastSeen = firstSeen
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scope audit forwarder scan rows: %w", err)
	}
	return result, nil
}

// Scope audit configuration-state values — see ScopeAuditRow.ConfigState and
// scopeAuditConfigState.
const (
	ScopeConfigFull       = "full"        // named regions AND '*'
	ScopeConfigNoScopes   = "no-scopes"   // '*' only, no named regions
	ScopeConfigNoUnscoped = "no-unscoped" // named regions, no '*'
	ScopeConfigNoFlood    = "no-flood"    // neither named regions nor '*'
)

// scopeAuditConfigState classifies a repeater's declared-regions answer into
// one of four configuration states, from ONLY the two fields the caller has
// already parsed out of the raw declared list (namedRegions and wildcard) —
// never re-derived from regions_csv, which splitRegionsCSV/normScope have
// already reduced to exactly these two facts.
//
// The firmware exports the FLOOD-allowed set (region_map.exportNamesTo(...,
// REGION_DENY_FLOOD) in examples/simple_repeater/MyMesh.cpp), so "no named
// regions in the export" does not strictly prove "no regions defined": a
// repeater with regions defined but every one of them marked deny-flood
// exports exactly the same list as one with no region tree at all, and the
// two are indistinguishable from this data alone. That caveat lands on
// ScopeConfigNoScopes and ScopeConfigNoFlood, both of which read "zero named
// regions in the export". It does NOT land on the wildcard half of the
// classification: '*' absent from the export is exact, not an inference —
// it means the wildcard denies flooding, i.e. plain unscoped floods are not
// forwarded, full stop. That exactness is why ScopeConfigNoUnscoped needs no
// caveat, and why ScopeConfigNoFlood's "no unscoped forwarding" half is
// exact even though its "no named regions" half is not.
func scopeAuditConfigState(namedRegions []string, wildcard bool) string {
	switch {
	case len(namedRegions) > 0 && wildcard:
		return ScopeConfigFull
	case len(namedRegions) == 0 && wildcard:
		return ScopeConfigNoScopes
	case len(namedRegions) > 0 && !wildcard:
		return ScopeConfigNoUnscoped
	default:
		return ScopeConfigNoFlood
	}
}

// ScopeAuditRow is one repeater's declared-vs-observed comparison for
// GET /api/scope-audit — the network-wide answer to "which repeaters
// declare a region they are not actually forwarding". Every field is
// already normalised (no leading '#') and '*' is never present in
// DeclaredRegions/NotObserved/UndeclaredObserved — see DeclaredWildcard and
// WildcardContradiction for its counterpart.
type ScopeAuditRow struct {
	PublicKey string `json:"publicKey"`
	// Name/Role are pointers so "we hold no nodes row for this target" serialises
	// as null rather than "". A declared-regions row can name a repeater this
	// instance has never recorded, and an empty string would make that
	// indistinguishable from a node we DO know that simply has no name — the
	// same absent-is-not-empty rule the declared side already follows.
	Name *string `json:"name"`
	Role *string `json:"role"`

	DeclaredRegions  []string `json:"declaredRegions"`  // '*' excluded — see DeclaredWildcard
	DeclaredWildcard bool     `json:"declaredWildcard"` // '*' present in the raw declared list
	// ConfigState is scopeAuditConfigState(DeclaredRegions, DeclaredWildcard)
	// — one of ScopeConfigFull/NoScopes/NoUnscoped/NoFlood, the config-state
	// reading of those same two fields so a caller does not have to
	// re-derive it from them. See scopeAuditConfigState's doc comment for
	// the caveat on the "no named regions" half of NoScopes/NoFlood.
	ConfigState string `json:"configState"`
	DeclaredAt  string `json:"declaredAt"` // ISO — age of the declared answer, not the window
	Truncated   bool   `json:"truncated"`  // declared list may have had entries silently dropped

	// NotObserved is declared regions with zero matched-forwarding observed
	// in the window — the headline this endpoint exists to surface.
	NotObserved []string `json:"notObserved"`
	// UndeclaredObserved is scopes this repeater was observed forwarding
	// that are absent from its declared list.
	UndeclaredObserved []ScopeObservation `json:"undeclaredObserved"`

	// ObservedUnscopedPackets is plain-FLOOD (no transport scope) packets
	// this repeater was observed forwarding in the window — the '*'
	// counterpart, per DeclaredWildcard's doc comment.
	ObservedUnscopedPackets int64 `json:"observedUnscopedPackets"`
	// WildcardContradiction is true when this repeater was observed
	// forwarding unscoped floods but its declared list omits '*' — it says
	// it does NOT forward them, and the traffic says otherwise.
	WildcardContradiction bool `json:"wildcardContradiction"`

	// AmbiguousHops counts forwarder-hop observations in the window whose
	// truncated hash prefix matched this target's pubkey AND at least one
	// other declared target's — see ScopeAuditForwarding's doc comment for
	// why those hops are attributed to neither and instead counted here on
	// every candidate. A non-zero value is a caveat, not a finding: any
	// NotObserved entry on this row could be explained by a colliding
	// neighbour's traffic rather than a real absence, and any
	// UndeclaredObserved entry is unaffected by it (ambiguous hops are never
	// attributed to a scope at all).
	AmbiguousHops int64 `json:"ambiguousHops"`

	// ObservedUnmatchedPackets counts packets this repeater was observed
	// forwarding whose transport scope matched no region key this instance
	// holds. Like AmbiguousHops it is a caveat rather than a finding, but the
	// two have different causes and different fixes: AmbiguousHops is a
	// pubkey-prefix collision between two repeaters and nobody's fault,
	// ObservedUnmatchedPackets is a missing entry in this instance's own
	// hashRegions and the reader can act on it. A non-zero value means any
	// NotObserved entry on this row may name a region this instance cannot
	// name rather than one the repeater is not forwarding.
	//
	// It says nothing about DeclaredWildcard: unmatched traffic IS scoped, so
	// it never feeds WildcardContradiction, which counts only plain unscoped
	// floods.
	ObservedUnmatchedPackets int64 `json:"observedUnmatchedPackets"`
}

// ScopeAuditResponse is the payload for GET /api/scope-audit. Only
// repeaters with at least one declared-regions answer are included — a
// repeater never successfully asked is absent, not shown declaring nothing
// (see AllCurrentDeclaredRegions).
type ScopeAuditResponse struct {
	Window    string          `json:"window"`
	Since     string          `json:"since"` // ISO — start of the observed-forwarding window
	Repeaters []ScopeAuditRow `json:"repeaters"`
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

// scopesState is per-server cache + singleflight state for
// GET /api/nodes/{pubkey}/scopes, mirroring reachState (node_reach.go): a
// bounded TTL cache keyed on (pubkey, window) plus a singleflight group so
// concurrent cold-cache requests for the same key compute once, not N
// times. Lives on *Server (not a package global) for the same reason reach
// does — multiple *Server instances must not share observable state.
type scopesState struct {
	cacheMu sync.RWMutex
	cache   map[string]scopesCacheEntry
	sf      singleflight.Group

	// lastSeenBlacklistGen mirrors reachState's field: when the live
	// blacklist generation advances past this value, the cache is purged
	// wholesale on the next request. The 404 path itself is never cached
	// (the handler returns before a cache key is even computed), so this
	// only guards a pre-existing successful entry from outliving a
	// blacklist/hide edit made after it was filled.
	lastSeenBlacklistGen atomic.Uint64
}

type scopesCacheEntry struct {
	at  time.Time
	raw []byte
}

const (
	// nodeScopesCacheTTL matches the sibling /api/scope-stats endpoint's
	// cache lifetime — also per-window region-scope data recomputed from
	// the same transmissions table.
	nodeScopesCacheTTL = 30 * time.Second
	nodeScopesCacheMax = 256
)

// scopesCacheGet returns the cached marshalled JSON for key. The returned
// slice is shared (not copied) and MUST NOT be mutated by callers.
func (s *Server) scopesCacheGet(key string) ([]byte, bool) {
	s.scopes.cacheMu.RLock()
	defer s.scopes.cacheMu.RUnlock()
	e, ok := s.scopes.cache[key]
	if !ok || time.Since(e.at) > nodeScopesCacheTTL {
		return nil, false
	}
	return e.raw, true
}

func (s *Server) scopesCachePut(key string, raw []byte) {
	s.scopes.cacheMu.Lock()
	defer s.scopes.cacheMu.Unlock()
	if s.scopes.cache == nil {
		s.scopes.cache = map[string]scopesCacheEntry{}
	}
	if _, exists := s.scopes.cache[key]; !exists && len(s.scopes.cache) >= nodeScopesCacheMax {
		s.evictScopesLocked()
	}
	s.scopes.cache[key] = scopesCacheEntry{at: time.Now(), raw: raw}
}

// evictScopesLocked drops expired entries first; if still at the cap it
// evicts the single oldest entry. Caller holds s.scopes.cacheMu (write).
func (s *Server) evictScopesLocked() {
	now := time.Now()
	for k, e := range s.scopes.cache {
		if now.Sub(e.at) > nodeScopesCacheTTL {
			delete(s.scopes.cache, k)
		}
	}
	if len(s.scopes.cache) < nodeScopesCacheMax {
		return
	}
	var oldestKey string
	var oldestAt time.Time
	first := true
	for k, e := range s.scopes.cache {
		if first || e.at.Before(oldestAt) {
			oldestKey, oldestAt, first = k, e.at, false
		}
	}
	if !first {
		delete(s.scopes.cache, oldestKey)
	}
}

// scopesPurgeIfBlacklistGenChanged drops every cached entry when the live
// blacklist generation has advanced past the cache's last-seen value,
// mirroring reachPurgeIfBlacklistGenChanged. CAS gates the purge so
// concurrent callers only do the work once per gen bump.
func (s *Server) scopesPurgeIfBlacklistGenChanged(gen uint64) {
	seen := s.scopes.lastSeenBlacklistGen.Load()
	if gen == seen {
		return
	}
	if !s.scopes.lastSeenBlacklistGen.CompareAndSwap(seen, gen) {
		return
	}
	s.scopes.cacheMu.Lock()
	s.scopes.cache = nil
	s.scopes.cacheMu.Unlock()
}

// nodeScopesWindowLookback maps the ?window= vocabulary to a lookback
// duration. Matches the sibling /api/scope-stats endpoint's vocabulary
// exactly (1h, 24h, 7d) rather than the broader ParseTimeWindow alias set
// (which also accepts 1d/3d/1w/30d) used by unrelated analytics endpoints.
func nodeScopesWindowLookback(window string) (time.Duration, bool) {
	switch window {
	case "1h":
		return time.Hour, true
	case "24h":
		return 24 * time.Hour, true
	case "7d":
		return 7 * 24 * time.Hour, true
	default:
		return 0, false
	}
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
