package main

import (
	"database/sql"
	"fmt"
	"strings"
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
// observed FORWARDING — i.e. packets on which this pubkey was the last hop
// of a FLOOD-family route (RouteTransportFlood, RouteFlood). It does NOT
// mean "packets in which this node appears anywhere in the path": a DIRECT
// or TRANSPORT_DIRECT packet's last path hop is the route's far end, never
// the transmitter, so crediting it here would attribute forwarding this node
// never did. Direct and TransportDirect are therefore always zero by
// construction — the forwarder join can never match those route types.
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
// only route types whose path[last] is the packet's actual transmitter:
// RouteTransportFlood (0) and RouteFlood (1). A DIRECT route consumes hops
// from the front, so its path[last] is the route's far end rather than the
// forwarder — including RouteDirect (2) / RouteTransportDirect (3) here
// would misattribute a scope to a node that never forwarded the packet.
const scopeConformanceForwarderRouteTypesSQL = "t.route_type IN (0, 1)"

// minForwarderHopHexLen is the shortest path_json hop ScopeConformance will
// treat as an attribution: 4 hex chars = 2 bytes. Matches deriveHeardKey's
// floor (cmd/ingestor/client_reception.go: "exclude 1-byte (collision-prone),
// matching Reach") — a 1-byte/2-hex-char hash collides too often across a
// real fleet, and attributing a scope to the wrong node is worse than
// attributing none.
const minForwarderHopHexLen = 4

// scopeConformanceQuery finds every transmission this pubkey forwarded
// (path[last] on a FLOOD-family route matches the pubkey), bounded by the
// since window so the scan stays an index range on first_seen rather than a
// full table scan.
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
var scopeConformanceQuery = `
	SELECT t.scope_name, t.route_type, t.first_seen
	FROM transmissions t
	WHERE t.first_seen >= ?
	  AND ` + scopeConformanceForwarderRouteTypesSQL + `
	  AND EXISTS (
	      SELECT 1
	      FROM observations o
	      JOIN json_each(o.path_json) je ON je.key = json_array_length(o.path_json) - 1
	      WHERE o.transmission_id = t.id
	        AND o.path_json IS NOT NULL
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
