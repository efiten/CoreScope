package main

import (
	"database/sql"
	"math"
	"strings"
	"sync/atomic"
)

// GeoHopMaxRangeKM is the maximum distance between a mobile client's GPS
// position and a candidate node's last-known position for that candidate to
// be considered a match for a 1-byte hop prefix (see resolveGeoHop).
const GeoHopMaxRangeKM = 50.0

// GeoCandidate is one positioned node considered for geographic hop
// resolution: its full pubkey plus its last-known lat/lon from the nodes
// table.
type GeoCandidate struct {
	Pubkey   string
	Lat, Lon float64
}

// geoIndex maps a 1-byte (2 hex char) lowercase pubkey prefix to every
// positioned node sharing that prefix. A node with no recorded position is
// never indexed — see resolveGeoHop's doc comment for why that is an
// assumption, not a fact.
type geoIndex map[string][]GeoCandidate

// geoIndexHolder caches the geo index for the client-reception hot path.
// atomic.Value lets the periodic rebuild publish without a read-side lock,
// mirroring prefixIdxHolder/neighborGraphHolder (path_resolver.go).
type geoIndexHolder struct {
	v atomic.Value // holds geoIndex
}

func (h *geoIndexHolder) load() geoIndex {
	if v := h.v.Load(); v != nil {
		return v.(geoIndex)
	}
	return nil
}

func (h *geoIndexHolder) store(idx geoIndex) {
	h.v.Store(idx)
}

// buildGeoIndex reads every positioned node (lat AND lon non-NULL) from the
// nodes table and indexes it by its 1-byte pubkey prefix. Nodes with no
// recorded position are read but excluded from the index — they are never
// candidates for geographic resolution (see resolveGeoHop).
func buildGeoIndex(db *sql.DB) (geoIndex, error) {
	rows, err := db.Query(`SELECT public_key, lat, lon FROM nodes WHERE lat IS NOT NULL AND lon IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	idx := make(geoIndex, 256)
	for rows.Next() {
		var pk string
		var lat, lon float64
		if err := rows.Scan(&pk, &lat, &lon); err != nil {
			continue
		}
		pkLower := strings.ToLower(pk)
		if len(pkLower) < 2 {
			continue
		}
		prefix := pkLower[:2]
		idx[prefix] = append(idx[prefix], GeoCandidate{Pubkey: pkLower, Lat: lat, Lon: lon})
	}
	return idx, nil
}

// RefreshGeoIndex rebuilds the in-memory geo index from the nodes table and
// publishes it atomically. Called on startup and from the neighbor-edges
// builder tick (60s, neighbor_builder.go) — the same cadence already used to
// refresh prefixIdx/neighborGraph, since all three are derived from the same
// nodes table and there is no reason for this one to run on a different
// schedule. A node whose position is added or changed since the last refresh
// is simply not reflected yet; the next tick picks it up. That staleness
// window (up to 60s) is accepted, not hidden.
func (s *Store) RefreshGeoIndex() error {
	idx, err := buildGeoIndex(s.db)
	if err != nil {
		return err
	}
	s.geoIdx.store(idx)
	return nil
}

// haversineKm returns the great-circle distance in kilometres between two
// lat/lon points. Mirrors cmd/server/store.go's haversineKm — duplicated
// rather than imported because cmd/ingestor and cmd/server are separate Go
// modules.
func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadiusKm = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	return earthRadiusKm * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// resolveGeoHop resolves a 1-byte (2 hex char) FLOOD hop prefix to a single
// full pubkey using the reception's GPS position, for the case deriveHeardKey
// rejects outright: a 1-byte prefix is 8 bits wide (256 possible values), and
// on the live database's 2531 known nodes those 256 values produce 254
// distinct prefixes with not one of them unique — a 1-byte hop alone can
// never identify a node.
//
// A mobile reception is a direct RF reception, so the transmitter cannot be
// far away. This narrows the candidate pool geographically instead: among
// positioned nodes sharing the prefix, if exactly one sits within
// GeoHopMaxRangeKM of the reception, the hop resolves to it (returned as its
// FULL pubkey, never the prefix). Zero or more than one candidate in range
// means no attribution, same as today.
//
// THE ASSUMPTION THIS RESTS ON (owner-approved, recorded here rather than
// hidden): idx (built by buildGeoIndex) only contains nodes with a known
// position — a node with no recorded position is excluded from the
// candidate pool entirely, treated as if it cannot compete for this
// attribution because it has no distance to be excluded by. That is not
// free of risk. Measured against the live database: of 72 repeaters with no
// recorded position, all 72 were seen within the last 7 days and 24 within
// the last 24 hours — these are active nodes, not dormant or test ones.
// MeshCore has an explicit advert_loc_policy = ADVERT_LOC_NONE setting, so
// broadcasting no position is a supported privacy choice, not a sign of a
// broken node. An unpositioned active repeater sharing the same prefix
// cannot be ruled out by distance, and this function does not try to.
//
// Every row this function attributes is written with src="geo" (never
// "rxlog" or "advert"), specifically so a geographically-resolved
// attribution is never indistinguishable from a directly-observed one, and
// so these rows can be found and reconsidered if the assumption above
// proves wrong. See docs/client-rx-coverage.md.
//
// idx reflects whatever buildGeoIndex last read (up to 60s stale, see
// RefreshGeoIndex) — a node positioned since the last refresh simply is not
// a candidate yet.
func resolveGeoHop(prefix string, lat, lon float64, idx geoIndex) (string, bool) {
	if idx == nil {
		return "", false
	}
	var match string
	found := 0
	for _, c := range idx[prefix] {
		if haversineKm(lat, lon, c.Lat, c.Lon) <= GeoHopMaxRangeKM {
			found++
			if found > 1 {
				return "", false
			}
			match = c.Pubkey
		}
	}
	if found == 1 {
		return match, true
	}
	return "", false
}
