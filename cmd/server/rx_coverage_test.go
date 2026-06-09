package main

import (
	"encoding/json"
	"testing"
)

func covF(f float64) *float64 { return &f }

func TestAggregateCoverageBucketsBestSNR(t *testing.T) {
	rows := []coverageRow{
		{Lat: 51.05000, Lon: 3.72000, SNR: covF(-12)},
		{Lat: 51.05001, Lon: 3.72001, SNR: covF(-6)}, // same cell, stronger
	}
	fc := aggregateCoverage(rows, 9)
	if len(fc.Features) != 1 {
		t.Fatalf("expected 1 cell, got %d", len(fc.Features))
	}
	if p := fc.Features[0].Properties; p.BestSNR == nil || *p.BestSNR != -6 || p.Count != 2 || !p.HasSig {
		t.Fatalf("bad props: %+v", fc.Features[0].Properties)
	}
	if g := fc.Features[0].Geometry; g.Type != "Polygon" || len(g.Coordinates) != 1 {
		t.Fatalf("bad geometry: %+v", g)
	}
	if _, err := json.Marshal(fc); err != nil {
		t.Fatalf("marshal: %v", err)
	}
}

func TestAggregateCoverageGreyWhenNoSignal(t *testing.T) {
	fc := aggregateCoverage([]coverageRow{{Lat: 51.05, Lon: 3.72}}, 9)
	if len(fc.Features) != 1 || fc.Features[0].Properties.HasSig {
		t.Fatalf("expected one grey (no-sig) cell, got %+v", fc.Features)
	}
}

func TestZoomToHexRes(t *testing.T) {
	if zoomToHexRes(16) != 11 || zoomToHexRes(8) != 7 || zoomToHexRes(3) != 6 {
		t.Fatalf("zoom→res mapping wrong: %d %d %d", zoomToHexRes(16), zoomToHexRes(8), zoomToHexRes(3))
	}
}
