package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Uplink coverage: per hex cell, whether the mesh heard the mobile companions
// that pinged there. A ping is one "cdrx <tag>" channel message from CoreDrive
// RX (cmd/ingestor/client_uplink.go stores it). Two facts decide its class:
//
//	echo      the companion heard a repeater re-broadcast the ping
//	observed  a CoreScope observer received the ping (by its packet hash)
//
// "observed" depends on where observers stand: a ping no observer received
// may still have travelled. The legend says so; the data cannot.

const (
	uplinkBoth     = "both"     // echo and observed
	uplinkObserved = "observed" // observed, no echo heard
	uplinkEcho     = "echo"     // echo, no observer received it
	uplinkNone     = "none"     // neither
)

// uplinkRank orders classes from best to worst for a cell's summary colour.
var uplinkRank = map[string]int{uplinkBoth: 3, uplinkObserved: 2, uplinkEcho: 1, uplinkNone: 0}

// uplinkFeatureCap bounds the response like the other coverage layers.
const uplinkFeatureCap = 5000

// uplinkMaxFirstHops bounds the first-hop list per cell.
const uplinkMaxFirstHops = 12

type uplinkPingRow struct {
	Lat, Lon  float64
	SentAt    string
	EchoCount int
	FirstHops string // comma-separated, from the ingestor
	PktHash   string // from the echo, else from client_uplink_tx; "" when unknown
}

type uplinkArrival struct {
	Observers int
	MaxHops   int
}

func uplinkClass(echo, observed bool) string {
	switch {
	case echo && observed:
		return uplinkBoth
	case observed:
		return uplinkObserved
	case echo:
		return uplinkEcho
	}
	return uplinkNone
}

type UplinkFeatureCollection struct {
	Type      string          `json:"type"` // "FeatureCollection"
	Features  []UplinkFeature `json:"features"`
	Truncated bool            `json:"truncated,omitempty"`
}

type UplinkFeature struct {
	Type       string           `json:"type"` // "Feature"
	Geometry   CoveragePolygon  `json:"geometry"`
	Properties UplinkProperties `json:"properties"`
}

// UplinkProperties summarises the pings in one cell.
type UplinkProperties struct {
	Cell         string   `json:"cell"`
	Pings        int      `json:"pings"`
	Both         int      `json:"both"`
	Observed     int      `json:"observed"`
	Echo         int      `json:"echo"`
	None         int      `json:"none"`
	Best         string   `json:"best"`
	FirstHops    []string `json:"first_hops"`
	MaxObservers int      `json:"max_observers"`
	MaxHops      int      `json:"max_hops"`
	LastAt       string   `json:"last_at"`
}

func aggregateUplink(rows []uplinkPingRow, arrivals map[string]uplinkArrival, res int) UplinkFeatureCollection {
	type agg struct {
		p    UplinkProperties
		hops map[string]bool
	}
	byCell := map[string]*agg{}
	for _, r := range rows {
		cell := hexCellAt(r.Lat, r.Lon, res)
		a := byCell[cell]
		if a == nil {
			a = &agg{p: UplinkProperties{Cell: cell, Best: uplinkNone, FirstHops: []string{}}, hops: map[string]bool{}}
			byCell[cell] = a
		}
		arr := arrivals[r.PktHash]
		class := uplinkClass(r.EchoCount > 0, r.PktHash != "" && arr.Observers > 0)
		a.p.Pings++
		switch class {
		case uplinkBoth:
			a.p.Both++
		case uplinkObserved:
			a.p.Observed++
		case uplinkEcho:
			a.p.Echo++
		default:
			a.p.None++
		}
		if uplinkRank[class] > uplinkRank[a.p.Best] {
			a.p.Best = class
		}
		if arr.Observers > a.p.MaxObservers {
			a.p.MaxObservers = arr.Observers
		}
		if arr.MaxHops > a.p.MaxHops {
			a.p.MaxHops = arr.MaxHops
		}
		if r.SentAt > a.p.LastAt {
			a.p.LastAt = r.SentAt
		}
		for _, h := range strings.Split(r.FirstHops, ",") {
			if h != "" && !a.hops[h] && len(a.p.FirstHops) < uplinkMaxFirstHops {
				a.hops[h] = true
				a.p.FirstHops = append(a.p.FirstHops, h)
			}
		}
	}
	fc := UplinkFeatureCollection{Type: "FeatureCollection", Features: []UplinkFeature{}}
	for cell, a := range byCell {
		ring := hexBoundary(cell)
		if ring == nil {
			continue
		}
		fc.Features = append(fc.Features, UplinkFeature{
			Type:       "Feature",
			Geometry:   CoveragePolygon{Type: "Polygon", Coordinates: [][][2]float64{ring}},
			Properties: a.p,
		})
	}
	if len(fc.Features) > uplinkFeatureCap {
		sort.Slice(fc.Features, func(i, j int) bool {
			pi, pj := fc.Features[i].Properties.Pings, fc.Features[j].Properties.Pings
			if pi != pj {
				return pi > pj
			}
			return fc.Features[i].Properties.Cell < fc.Features[j].Properties.Cell
		})
		fc.Features = fc.Features[:uplinkFeatureCap]
		fc.Truncated = true
	}
	sort.Slice(fc.Features, func(i, j int) bool { return fc.Features[i].Properties.Cell < fc.Features[j].Properties.Cell })
	return fc
}

// queryUplinkPings returns pings with a position inside the bbox, within
// `days` (0 = all time). The packet hash comes from the ping when it had an
// echo, otherwise from the ingestor's own tag -> hash link.
func (s *Server) queryUplinkPings(days int, b bbox) ([]uplinkPingRow, error) {
	where := []string{"p.lat IS NOT NULL", "p.lat BETWEEN ? AND ?", "p.lon BETWEEN ? AND ?"}
	args := []interface{}{b.MinLat, b.MaxLat, b.MinLon, b.MaxLon}
	if days > 0 {
		where = append(where, "p.sent_at >= ?")
		args = append(args, time.Now().UTC().AddDate(0, 0, -days).Format(rxTimeMillisLayoutServer))
	}
	rows, err := s.db.conn.Query(`
		SELECT p.lat, p.lon, p.sent_at, p.echo_count, p.first_hops, COALESCE(p.pkt_hash, t.pkt_hash, '')
		FROM client_uplink_pings p
		LEFT JOIN client_uplink_tx t ON t.tag = p.tag
		WHERE `+strings.Join(where, " AND "), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []uplinkPingRow{}
	for rows.Next() {
		var r uplinkPingRow
		if err := rows.Scan(&r.Lat, &r.Lon, &r.SentAt, &r.EchoCount, &r.FirstHops, &r.PktHash); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// queryUplinkArrivals counts, per packet hash, the distinct observers that
// received it and the longest path they saw. One query per 500 hashes.
func (s *Server) queryUplinkArrivals(rows []uplinkPingRow) (map[string]uplinkArrival, error) {
	seen := map[string]bool{}
	var hashes []string
	for _, r := range rows {
		if r.PktHash != "" && !seen[r.PktHash] {
			seen[r.PktHash] = true
			hashes = append(hashes, r.PktHash)
		}
	}
	out := make(map[string]uplinkArrival, len(hashes))
	observerCol := "o.observer_idx"
	if !s.db.isV3 {
		observerCol = "o.observer_id"
	}
	const chunk = 500
	for i := 0; i < len(hashes); i += chunk {
		end := i + chunk
		if end > len(hashes) {
			end = len(hashes)
		}
		batch := hashes[i:end]
		args := make([]interface{}, len(batch))
		for j, h := range batch {
			args[j] = h
		}
		q, err := s.db.conn.Query(`
			SELECT t.hash, COUNT(DISTINCT `+observerCol+`), MAX(json_array_length(o.path_json))
			FROM transmissions t JOIN observations o ON o.transmission_id = t.id
			WHERE t.hash IN (`+sqlPlaceholders(len(batch))+`)
			GROUP BY t.hash`, args...)
		if err != nil {
			return nil, err
		}
		for q.Next() {
			var h string
			var a uplinkArrival
			var hops *int
			if err := q.Scan(&h, &a.Observers, &hops); err != nil {
				q.Close()
				return nil, err
			}
			if hops != nil {
				a.MaxHops = *hops
			}
			out[h] = a
		}
		q.Close()
	}
	return out, nil
}

// rxTimeMillisLayoutServer matches the ingestor's sent_at format
// (rxTimeMillisLayout in cmd/ingestor), so the cutoff compares
// lexicographically against stored values.
const rxTimeMillisLayoutServer = "2006-01-02T15:04:05.000Z07:00"

func (s *Server) requireClientUplink(w http.ResponseWriter, r *http.Request) bool {
	if s == nil || s.cfg == nil || !s.cfg.ClientUplinkEnabled() {
		http.NotFound(w, r)
		return false
	}
	return true
}

// handleUplinkCoverage serves the uplink layer as GeoJSON hex cells. An
// instance whose ingestor has not created the tables yet answers an empty
// collection.
func (s *Server) handleUplinkCoverage(w http.ResponseWriter, r *http.Request) {
	if !s.requireClientUplink(w, r) {
		return
	}
	b, ok := parseBBox(r.URL.Query().Get("bbox"))
	if !ok {
		http.Error(w, "bbox required as minLat,minLon,maxLat,maxLon", http.StatusBadRequest)
		return
	}
	if s.db == nil || s.db.conn == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	days := clampDays(atoiDefault(r.URL.Query().Get("days"), 7))
	z, _ := strconv.Atoi(r.URL.Query().Get("z"))
	fc := UplinkFeatureCollection{Type: "FeatureCollection", Features: []UplinkFeature{}}
	if s.db.tablePresent("client_uplink_pings") && s.db.tablePresent("client_uplink_tx") {
		rows, err := s.queryUplinkPings(days, b)
		if err != nil {
			http.Error(w, "query failed", http.StatusInternalServerError)
			return
		}
		arrivals, err := s.queryUplinkArrivals(rows)
		if err != nil {
			http.Error(w, "query failed", http.StatusInternalServerError)
			return
		}
		fc = aggregateUplink(rows, arrivals, zoomToHexRes(z))
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(fc)
}
