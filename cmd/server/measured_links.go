// Package main: read-only measured-links loader and API (#1865).
//
// The ingestor writes measured_links from the observers' neighbours reports:
// one row per direction, "reporter hears neighbor at snr". The server loads
// the table into an immutable index at startup and on every neighbour
// recompute tick (refreshNeighborGraphFromSnapshot), and atomic-swaps it into
// PacketStore.measured. Handlers read the current index without a lock.
package main

import (
	"database/sql"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
)

// MeasuredInfo is one measured neighbour as seen from the queried node.
// SNRIn is the SNR at which the queried node hears the neighbour (the row the
// node reported), SNROut the SNR at which the neighbour hears the queried node
// (the row the neighbour reported); either is null when that direction was
// not reported or carried no SNR. HeardAt and ReportedAt are the newest of
// the measured directions, Verified is true only when every measured
// direction came from a verified report. Status is the scope-query result the
// queried node reported for this neighbour, empty when only the neighbour
// reported the link.
type MeasuredInfo struct {
	SNRIn      *float64 `json:"snrIn"`
	SNROut     *float64 `json:"snrOut"`
	HeardAt    string   `json:"heardAt"`
	ReportedAt string   `json:"reportedAt"`
	Verified   bool     `json:"verified"`
	Status     string   `json:"status"`
}

// MeasuredPair is one unordered measured link for the map, A < B. SNRAB is the
// SNR at which A hears B (the row reporter=A, neighbor=B), SNRBA the SNR at
// which B hears A; either is null when that direction is not measured.
// Verified is true only when every measured direction is verified. AgeSec is
// the age of the newest reported_at of the two directions.
type MeasuredPair struct {
	A        string   `json:"a"`
	B        string   `json:"b"`
	SNRAB    *float64 `json:"snrAB"`
	SNRBA    *float64 `json:"snrBA"`
	Verified bool     `json:"verified"`
	AgeSec   int64    `json:"ageSec"`
}

// MeasuredLinksResponse is the body of GET /api/links/measured.
type MeasuredLinksResponse struct {
	Links []MeasuredPair `json:"links"`
}

// NeighborReportSummary is the newest neighbours report a node sent, on the
// node detail endpoint. Total, Queried and Truncated are null when the report
// did not carry them.
type NeighborReportSummary struct {
	ReportedAt string `json:"reportedAt"`
	Total      *int   `json:"total"`
	Queried    *int   `json:"queried"`
	Truncated  *bool  `json:"truncated"`
	Verified   bool   `json:"verified"`
}

// measuredLink is one measured_links row: Reporter hears Neighbor at SNR.
type measuredLink struct {
	Reporter   string
	Neighbor   string
	SNR        *float64
	HeardAt    string
	ReportedAt string
	Verified   bool
	Status     string
}

// measuredLinkIndex maps a lowercase pubkey to every row it appears in, as
// reporter or as neighbour. Built once per load and never mutated after, so
// readers need no lock. Size is one entry per row per endpoint; measured_links
// is bounded by the ingestor (retention.measuredLinkDays, and at most
// maxStoredNeighbors = 500 links per report).
type measuredLinkIndex struct {
	byNode map[string][]measuredLink
}

// loadMeasuredLinks reads the measured_links reported in the last days days
// through the read-only handle (idx_measured_links_reported), the same window
// the ingestor prunes by, so a row the daily prune has not reached yet is not
// shown as current. A database without the table yields an empty index. A
// failed read returns nil, never a partial index, so the caller can keep the
// one it has.
func loadMeasuredLinks(conn *sql.DB, days int) *measuredLinkIndex {
	ix := &measuredLinkIndex{byNode: make(map[string][]measuredLink)}
	if conn == nil {
		return ix
	}
	var name string
	err := conn.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'measured_links'`).Scan(&name)
	if err == sql.ErrNoRows {
		return ix
	}
	if err != nil {
		log.Printf("[measured-links] failed to probe measured_links: %v", err)
		return nil
	}
	cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339)
	rows, err := conn.Query(`SELECT reporter, neighbor, snr, heard_at, reported_at, verified, status FROM measured_links
		WHERE reported_at >= ?`, cutoff)
	if err != nil {
		log.Printf("[measured-links] failed to load measured_links: %v", err)
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var l measuredLink
		var snr sql.NullFloat64
		var verified int
		if err := rows.Scan(&l.Reporter, &l.Neighbor, &snr, &l.HeardAt, &l.ReportedAt, &verified, &l.Status); err != nil {
			log.Printf("[measured-links] failed to load measured_links: %v", err)
			return nil
		}
		l.Reporter = strings.ToLower(l.Reporter)
		l.Neighbor = strings.ToLower(l.Neighbor)
		if l.Neighbor == l.Reporter {
			// A node is not its own neighbour.
			continue
		}
		if snr.Valid {
			v := snr.Float64
			l.SNR = &v
		}
		l.Verified = verified != 0
		ix.byNode[l.Reporter] = append(ix.byNode[l.Reporter], l)
		ix.byNode[l.Neighbor] = append(ix.byNode[l.Neighbor], l)
	}
	if err := rows.Err(); err != nil {
		log.Printf("[measured-links] failed to load measured_links: %v", err)
		return nil
	}
	return ix
}

// forNode returns a copy of the rows pk appears in. Nil-safe.
func (ix *measuredLinkIndex) forNode(pk string) []measuredLink {
	if ix == nil {
		return nil
	}
	src := ix.byNode[strings.ToLower(pk)]
	if len(src) == 0 {
		return nil
	}
	out := make([]measuredLink, len(src))
	copy(out, src)
	return out
}

// newerTimestamp returns whichever of a and b is later; an unparsable or
// empty value loses.
func newerTimestamp(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	if parseTimestamp(b).After(parseTimestamp(a)) {
		return b
	}
	return a
}

// measuredInfoByNeighbor merges the rows of pk (from forNode) into one
// MeasuredInfo per neighbour, keyed by the neighbour's lowercase pubkey.
func measuredInfoByNeighbor(pk string, links []measuredLink) map[string]MeasuredInfo {
	pk = strings.ToLower(pk)
	out := make(map[string]MeasuredInfo, len(links))
	for _, l := range links {
		other := l.Neighbor
		in := l.Reporter == pk
		if !in {
			other = l.Reporter
		}
		info, ok := out[other]
		if !ok {
			info.Verified = true
		}
		if in {
			info.SNRIn = l.SNR
			info.Status = l.Status
		} else {
			info.SNROut = l.SNR
		}
		info.HeardAt = newerTimestamp(info.HeardAt, l.HeardAt)
		info.ReportedAt = newerTimestamp(info.ReportedAt, l.ReportedAt)
		info.Verified = info.Verified && l.Verified
		out[other] = info
	}
	return out
}

// measuredIndex returns the current index, nil when none is loaded.
func (s *Server) measuredIndex() *measuredLinkIndex {
	if s.store == nil {
		return nil
	}
	return s.store.measured.Load()
}

// measuredNodeHidden reports whether pk is blacklisted or carries a hidden
// name prefix, from the node cache so no row is read per pubkey.
func (s *Server) measuredNodeHidden(pk string, nodeMap map[string]nodeInfo) bool {
	if s.cfg == nil {
		return false
	}
	if s.cfg.IsBlacklisted(pk) {
		return true
	}
	info, ok := nodeMap[pk]
	return ok && s.cfg.IsNameHidden(info.Name)
}

// mergeMeasuredNeighbors adds the measured links of pubkey to the neighbour
// entries. An entry with the same full pubkey gets Measured. A measured
// neighbour without one is matched next against the entries that have no
// resolved pubkey: first by candidate, then by hop prefix; the first such
// entry still without Measured gets it. Every other measured neighbour is
// appended with Inferred false and Count 0.
func (s *Server) mergeMeasuredNeighbors(pubkey string, entries []NeighborEntry, nodeMap map[string]nodeInfo, srcInfo nodeInfo) []NeighborEntry {
	links := s.measuredIndex().forNode(pubkey)
	if len(links) == 0 {
		return entries
	}
	measured := measuredInfoByNeighbor(pubkey, links)
	present := make(map[string]bool, len(entries))
	byCandidate := make(map[string][]int)
	var unresolved []int
	for i := range entries {
		if entries[i].Pubkey == nil {
			unresolved = append(unresolved, i)
			for _, c := range entries[i].Candidates {
				key := strings.ToLower(c.Pubkey)
				byCandidate[key] = append(byCandidate[key], i)
			}
			continue
		}
		key := strings.ToLower(*entries[i].Pubkey)
		present[key] = true
		if m, ok := measured[key]; ok {
			mc := m
			entries[i].Measured = &mc
		}
	}

	others := make([]string, 0, len(measured))
	for pk := range measured {
		if !present[pk] {
			others = append(others, pk)
		}
	}
	sort.Strings(others)
	attach := func(pk string) bool {
		for _, i := range byCandidate[pk] {
			if entries[i].Measured == nil {
				m := measured[pk]
				entries[i].Measured = &m
				return true
			}
		}
		for _, i := range unresolved {
			p := strings.ToLower(entries[i].Prefix)
			if entries[i].Measured == nil && p != "" && strings.HasPrefix(pk, p) {
				m := measured[pk]
				entries[i].Measured = &m
				return true
			}
		}
		return false
	}
	for _, pk := range others {
		if s.measuredNodeHidden(pk, nodeMap) {
			continue
		}
		if attach(pk) {
			continue
		}
		m := measured[pk]
		npk := pk
		inferred := false
		entry := NeighborEntry{
			Pubkey:    &npk,
			Prefix:    pk[:min(4, len(pk))],
			Observers: []string{},
			Measured:  &m,
			Inferred:  &inferred,
		}
		if info, ok := nodeMap[pk]; ok {
			name, role := info.Name, info.Role
			entry.Name = &name
			entry.Role = &role
			if srcInfo.HasGPS && info.HasGPS {
				d := haversineKm(srcInfo.Lat, srcInfo.Lon, info.Lat, info.Lon)
				entry.DistanceKm = &d
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

type measuredPairKey struct{ a, b string }

// measuredPairs merges the index into one MeasuredPair per unordered pair and
// keeps the pairs whose two nodes both have a position in the node cache.
// O(rows): each row is visited once, under its reporter's key.
func (s *Server) measuredPairs(now time.Time) []MeasuredPair {
	out := []MeasuredPair{}
	ix := s.measuredIndex()
	if ix == nil || len(ix.byNode) == 0 {
		return out
	}
	nodeMap := s.buildNodeInfoMap()
	positioned := func(pk string) bool {
		info, ok := nodeMap[pk]
		return ok && info.HasGPS && !s.measuredNodeHidden(pk, nodeMap)
	}

	type acc struct {
		pair     MeasuredPair
		reported string
	}
	pairs := make(map[measuredPairKey]*acc)
	for key, links := range ix.byNode {
		for _, l := range links {
			if l.Reporter != key || l.Reporter == l.Neighbor {
				continue
			}
			k := measuredPairKey{l.Reporter, l.Neighbor}
			if k.b < k.a {
				k = measuredPairKey{l.Neighbor, l.Reporter}
			}
			p, ok := pairs[k]
			if !ok {
				if !positioned(k.a) || !positioned(k.b) {
					continue
				}
				p = &acc{pair: MeasuredPair{A: k.a, B: k.b, Verified: true}}
				pairs[k] = p
			}
			if l.Reporter == k.a {
				p.pair.SNRAB = l.SNR
			} else {
				p.pair.SNRBA = l.SNR
			}
			p.pair.Verified = p.pair.Verified && l.Verified
			p.reported = newerTimestamp(p.reported, l.ReportedAt)
		}
	}

	for _, p := range pairs {
		if t := parseTimestamp(p.reported); !t.IsZero() {
			p.pair.AgeSec = int64(now.Sub(t).Seconds())
		}
		out = append(out, p.pair)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].A != out[j].A {
			return out[i].A < out[j].A
		}
		return out[i].B < out[j].B
	})
	return out
}

func (s *Server) handleMeasuredLinks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, MeasuredLinksResponse{Links: s.measuredPairs(time.Now())})
}

// latestNeighborReport returns the neighbor_reports row of pubkey with the
// newest reported_at, one lookup on idx_neighbor_reports_reporter. Ordering by
// the report's own time keeps a late replay of an old report from becoming
// the latest. ok is false when the node never
// reported or the table does not exist.
func (db *DB) latestNeighborReport(pubkey string) (NeighborReportSummary, bool, error) {
	var nr NeighborReportSummary
	if !db.hasNeighborReportLog {
		return nr, false, nil
	}
	var total, queried, truncated sql.NullInt64
	var verified int
	err := db.conn.QueryRow(`SELECT reported_at, total, queried, truncated, verified FROM neighbor_reports
		WHERE reporter = ? ORDER BY reported_at DESC, received_at DESC LIMIT 1`, strings.ToLower(pubkey)).
		Scan(&nr.ReportedAt, &total, &queried, &truncated, &verified)
	if err == sql.ErrNoRows {
		return nr, false, nil
	}
	if err != nil {
		return nr, false, err
	}
	if total.Valid {
		v := int(total.Int64)
		nr.Total = &v
	}
	if queried.Valid {
		v := int(queried.Int64)
		nr.Queried = &v
	}
	if truncated.Valid {
		v := truncated.Int64 != 0
		nr.Truncated = &v
	}
	nr.Verified = verified != 0
	return nr, true, nil
}
