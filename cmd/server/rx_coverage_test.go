package main

import (
	"encoding/json"
	"math"
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
	// Resolution tracks zoom 1:1 within [3,18], clamped at the edges (z=0 is the
	// missing-param case).
	cases := map[int]int{0: 3, 3: 3, 8: 8, 16: 16, 18: 18, 25: 18}
	for z, want := range cases {
		if got := zoomToHexRes(z); got != want {
			t.Fatalf("zoomToHexRes(%d)=%d, want %d", z, got, want)
		}
	}
}

// TestHexSizeRendersConstantPx verifies the core fix: a hex sized for resolution
// res renders at a constant ~hexTargetPx on screen at the corresponding zoom level,
// instead of the old fixed-meter buckets that were ~2px when zoomed out.
func TestHexSizeRendersConstantPx(t *testing.T) {
	for res := 4; res <= 16; res++ {
		// On-screen point-to-point height = 2*circumradius / mercUnitsPerPixel(zoom),
		// where mercUnitsPerPixel = mercUPPZ0 / 2^zoom and zoom == res.
		px := 2 * hexSizeForRes(res) * math.Pow(2, float64(res)) / mercUPPZ0
		if math.Abs(px-hexTargetPx) > 0.001 {
			t.Fatalf("res %d renders %.2fpx, want %.2fpx", res, px, hexTargetPx)
		}
		// Size must halve each zoom step (finer grid as you zoom in).
		if ratio := hexSizeForRes(res) / hexSizeForRes(res+1); math.Abs(ratio-2) > 1e-9 {
			t.Fatalf("res %d→%d size ratio %.4f, want 2", res, res+1, ratio)
		}
	}
}
