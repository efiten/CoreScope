package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
)

// coverageRow is one raw reception read from client_receptions.
type coverageRow struct {
	Lat, Lon float64
	SNR      *float64
	RSSI     *int
}

// GeoJSON output (named structs, no map[string]interface{} — AGENTS.md).
type CoverageFeatureCollection struct {
	Type     string            `json:"type"` // "FeatureCollection"
	Features []CoverageFeature `json:"features"`
}
type CoverageFeature struct {
	Type       string             `json:"type"` // "Feature"
	Geometry   CoveragePolygon    `json:"geometry"`
	Properties CoverageProperties `json:"properties"`
}
type CoveragePolygon struct {
	Type        string           `json:"type"`        // "Polygon"
	Coordinates [][][2]float64   `json:"coordinates"` // one ring: [ [ [lon,lat], ... ] ]
}
type CoverageProperties struct {
	Cell    string   `json:"cell"`
	Count   int      `json:"count"`
	BestSNR *float64 `json:"best_snr"`
	HasSig  bool     `json:"has_sig"` // false → render grey (no signal metric)
}

type covAgg struct {
	count   int
	bestSNR *float64
	hasSig  bool
}

// aggregateCoverage bins raw rows into display-resolution hex cells, keeping the
// best (max) SNR per cell, and emits GeoJSON polygons.
func aggregateCoverage(rows []coverageRow, res int) CoverageFeatureCollection {
	byCell := map[string]*covAgg{}
	for _, row := range rows {
		cell := hexCellAt(row.Lat, row.Lon, res)
		a := byCell[cell]
		if a == nil {
			a = &covAgg{}
			byCell[cell] = a
		}
		a.count++
		if row.SNR != nil {
			a.hasSig = true
			if a.bestSNR == nil || *row.SNR > *a.bestSNR {
				v := *row.SNR
				a.bestSNR = &v
			}
		}
	}
	fc := CoverageFeatureCollection{Type: "FeatureCollection", Features: []CoverageFeature{}}
	for cell, a := range byCell {
		ring := hexBoundary(cell)
		if ring == nil {
			continue
		}
		fc.Features = append(fc.Features, CoverageFeature{
			Type:     "Feature",
			Geometry: CoveragePolygon{Type: "Polygon", Coordinates: [][][2]float64{ring}},
			Properties: CoverageProperties{
				Cell: cell, Count: a.count, BestSNR: a.bestSNR, HasSig: a.hasSig,
			},
		})
	}
	return fc
}

type bbox struct{ MinLat, MinLon, MaxLat, MaxLon float64 }

// queryCoverageRows returns raw coverage rows where the directly-heard node
// matches the target pubkey by its 2-3 byte prefix (or full pubkey), within the
// bbox. Read-only (server RO connection).
func (s *Server) queryCoverageRows(pubkey string, b bbox) ([]coverageRow, error) {
	pk := strings.ToLower(pubkey)
	rows, err := s.db.conn.Query(`
		SELECT lat, lon, snr, rssi
		FROM client_receptions
		WHERE lat BETWEEN ? AND ? AND lon BETWEEN ? AND ?
		  AND ( (heard_keylen = 32 AND heard_key = ?)
		     OR (heard_keylen IN (2,3) AND substr(?, 1, heard_keylen*2) = heard_key) )`,
		b.MinLat, b.MaxLat, b.MinLon, b.MaxLon, pk, pk)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []coverageRow{}
	for rows.Next() {
		var lat, lon float64
		var snr sql.NullFloat64
		var rssi sql.NullInt64
		if err := rows.Scan(&lat, &lon, &snr, &rssi); err != nil {
			return nil, err
		}
		cr := coverageRow{Lat: lat, Lon: lon}
		if snr.Valid {
			v := snr.Float64
			cr.SNR = &v
		}
		if rssi.Valid {
			v := int(rssi.Int64)
			cr.RSSI = &v
		}
		out = append(out, cr)
	}
	return out, rows.Err()
}

// zoomToHexRes maps a Leaflet zoom level to a display hex resolution.
func zoomToHexRes(z int) int {
	switch {
	case z >= 15:
		return 11
	case z >= 13:
		return 10
	case z >= 11:
		return 9
	case z >= 9:
		return 8
	case z >= 7:
		return 7
	default:
		return 6
	}
}

func parseBBox(s string) (bbox, bool) {
	p := strings.Split(s, ",")
	if len(p) != 4 {
		return bbox{}, false
	}
	v := make([]float64, 4)
	for i := range p {
		f, err := strconv.ParseFloat(strings.TrimSpace(p[i]), 64)
		if err != nil {
			return bbox{}, false
		}
		v[i] = f
	}
	return bbox{MinLat: v[0], MinLon: v[1], MaxLat: v[2], MaxLon: v[3]}, true
}

// handleNodeRxCoverage serves per-node mobile RX coverage as a GeoJSON hex grid.
func (s *Server) handleNodeRxCoverage(w http.ResponseWriter, r *http.Request) {
	pubkey := strings.ToLower(mux.Vars(r)["pubkey"])
	b, ok := parseBBox(r.URL.Query().Get("bbox"))
	if !ok {
		http.Error(w, "bbox required as minLat,minLon,maxLat,maxLon", http.StatusBadRequest)
		return
	}
	if s.db == nil || s.db.conn == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	z, _ := strconv.Atoi(r.URL.Query().Get("z"))
	rows, err := s.queryCoverageRows(pubkey, b)
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	fc := aggregateCoverage(rows, zoomToHexRes(z))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(fc)
}
