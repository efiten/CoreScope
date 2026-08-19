package main

import (
	"strings"
	"testing"

	"github.com/meshcore-analyzer/packetpath"
)

// geoTestPubkey builds a syntactically-plausible 64-hex-char pubkey (32
// bytes) starting with the given 1-byte prefix, so tests exercise realistic
// key lengths (heard_keylen=32) rather than short placeholder strings.
func geoTestPubkey(prefix string) string {
	return geoTestPubkeyFill(prefix, "a")
}

// geoTestPubkeyFill is geoTestPubkey with a caller-chosen fill hex digit, so
// two candidates sharing the same 1-byte prefix get distinct full pubkeys
// (the fill never touches the prefix itself, unlike a blind string.Replace
// which can eat into it when the prefix's own hex digits match the fill).
func geoTestPubkeyFill(prefix, fill string) string {
	return prefix + strings.Repeat(fill, 62)
}

// Reception position used throughout: matches the lat/lon already used by
// the existing client_reception_test.go fixtures (Belgium).
const (
	geoRecLat = 51.05
	geoRecLon = 3.72
)

// ─── resolveGeoHop: pure-function coverage ─────────────────────────────────

// TestResolveGeoHop_SingleCandidateInRangeResolves covers the core rule:
// exactly one positioned candidate within GeoHopMaxRangeKM resolves.
func TestResolveGeoHop_SingleCandidateInRangeResolves(t *testing.T) {
	near := geoTestPubkey("1a")
	idx := geoIndex{"1a": {{Pubkey: near, Lat: 51.10, Lon: 3.75}}} // ~6km from geoRecLat/Lon
	pk, ok := resolveGeoHop("1a", geoRecLat, geoRecLon, idx)
	if !ok || pk != near {
		t.Fatalf("single in-range candidate: want (%q,true), got (%q,%v)", near, pk, ok)
	}
}

// TestResolveGeoHop_TwoCandidatesInRangeNotAttributed: two positioned
// candidates both within range → cannot disambiguate, no attribution.
func TestResolveGeoHop_TwoCandidatesInRangeNotAttributed(t *testing.T) {
	a := geoTestPubkey("1a")
	b := geoTestPubkeyFill("1a", "b")
	idx := geoIndex{"1a": {
		{Pubkey: a, Lat: 51.10, Lon: 3.75}, // ~6km
		{Pubkey: b, Lat: 51.06, Lon: 3.80}, // ~6km
	}}
	if _, ok := resolveGeoHop("1a", geoRecLat, geoRecLon, idx); ok {
		t.Fatal("two in-range candidates must not be attributed")
	}
}

// TestResolveGeoHop_OneCandidateOutsideRangeNotAttributed: the sole
// candidate sharing the prefix is farther than GeoHopMaxRangeKM away.
func TestResolveGeoHop_OneCandidateOutsideRangeNotAttributed(t *testing.T) {
	far := geoTestPubkey("1a")
	// ~161km north of geoRecLat/Lon — well past the 50km ceiling.
	idx := geoIndex{"1a": {{Pubkey: far, Lat: 52.5, Lon: geoRecLon}}}
	if d := haversineKm(geoRecLat, geoRecLon, 52.5, geoRecLon); d <= GeoHopMaxRangeKM {
		t.Fatalf("test fixture invalid: fixture distance %.1fkm is not > %vkm", d, GeoHopMaxRangeKM)
	}
	if _, ok := resolveGeoHop("1a", geoRecLat, geoRecLon, idx); ok {
		t.Fatal("out-of-range sole candidate must not be attributed")
	}
}

// TestResolveGeoHop_NoCandidatesNotAttributed: unknown prefix → nothing to
// resolve.
func TestResolveGeoHop_NoCandidatesNotAttributed(t *testing.T) {
	if _, ok := resolveGeoHop("ff", geoRecLat, geoRecLon, geoIndex{}); ok {
		t.Fatal("empty candidate set must not be attributed")
	}
	if _, ok := resolveGeoHop("ff", geoRecLat, geoRecLon, nil); ok {
		t.Fatal("nil index must not be attributed")
	}
}

// TestBuildGeoIndexExcludesUnpositionedNodes is the integration-level test
// for the deliberate assumption documented on resolveGeoHop: an unpositioned
// node sharing the same prefix as a positioned one is NOT a candidate, so it
// cannot block attribution to the positioned node. This is the
// owner-approved behavior, not an oversight — see resolveGeoHop's doc
// comment in geo_hop.go and docs/client-rx-coverage.md for the measured
// counts (72 unpositioned repeaters, all active within 7 days) this rests
// on.
func TestBuildGeoIndexExcludesUnpositionedNodes(t *testing.T) {
	s := newTestStore(t)
	positioned := geoTestPubkey("1a")
	unpositioned := geoTestPubkeyFill("1a", "b")
	if _, err := s.db.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES (?, 'positioned', 51.10, 3.75)`, positioned); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES (?, 'unpositioned', NULL, NULL)`, unpositioned); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshGeoIndex(); err != nil {
		t.Fatal(err)
	}
	idx := s.geoIdx.load()
	if got := len(idx["1a"]); got != 1 {
		t.Fatalf("geo index for prefix 1a: want 1 (unpositioned node excluded), got %d", got)
	}
	pk, ok := resolveGeoHop("1a", geoRecLat, geoRecLon, idx)
	if !ok || pk != positioned {
		t.Fatalf("expected resolution to the positioned node despite an unpositioned same-prefix node: pk=%q ok=%v", pk, ok)
	}
}

// ─── deriveHeardKeyGeo ──────────────────────────────────────────────────────

// TestDeriveHeardKeyGeo_OneByteHopResolvesToGeoSrc: the case deriveHeardKey
// rejects outright (1-byte FLOOD hop) resolves via geography here, and is
// tagged src="geo" with the candidate's FULL pubkey — never the 1-byte
// prefix.
func TestDeriveHeardKeyGeo_OneByteHopResolvesToGeoSrc(t *testing.T) {
	full := geoTestPubkey("1a")
	idx := geoIndex{"1a": {{Pubkey: full, Lat: 51.10, Lon: 3.75}}}
	k, l, src, ok := deriveHeardKeyGeo("rx", packetpath.RouteFlood, PayloadGRP_TXT, []string{"1a"}, "", false, geoRecLat, geoRecLon, idx)
	if !ok || src != "geo" || k != full || l != 32 {
		t.Fatalf("1-byte hop geo resolution: got k=%q l=%d src=%q ok=%v, want k=%q l=32 src=geo ok=true", k, l, src, ok, full)
	}
}

// TestDeriveHeardKeyGeo_TwoCandidatesNotAttributed mirrors the ambiguous
// case at the deriveHeardKeyGeo layer, not just resolveGeoHop.
func TestDeriveHeardKeyGeo_TwoCandidatesNotAttributed(t *testing.T) {
	a := geoTestPubkey("1a")
	b := geoTestPubkeyFill("1a", "b")
	idx := geoIndex{"1a": {
		{Pubkey: a, Lat: 51.10, Lon: 3.75},
		{Pubkey: b, Lat: 51.06, Lon: 3.80},
	}}
	if _, _, _, ok := deriveHeardKeyGeo("rx", packetpath.RouteFlood, PayloadGRP_TXT, []string{"1a"}, "", false, geoRecLat, geoRecLon, idx); ok {
		t.Fatal("two in-range candidates must not be attributed")
	}
}

// TestDeriveHeardKeyGeo_OutOfRangeNotAttributed mirrors the out-of-range
// case at the deriveHeardKeyGeo layer.
func TestDeriveHeardKeyGeo_OutOfRangeNotAttributed(t *testing.T) {
	far := geoTestPubkey("1a")
	idx := geoIndex{"1a": {{Pubkey: far, Lat: 52.5, Lon: geoRecLon}}}
	if _, _, _, ok := deriveHeardKeyGeo("rx", packetpath.RouteFlood, PayloadGRP_TXT, []string{"1a"}, "", false, geoRecLat, geoRecLon, idx); ok {
		t.Fatal("out-of-range sole candidate must not be attributed")
	}
}

// TestDeriveHeardKeyGeo_MultiByteHopStillRxlog: a 2-byte-or-longer hop must
// resolve exactly as deriveHeardKey already does (src "rxlog") and must
// never enter the geographic path at all — proven here by passing a nil
// geoIndex, which would panic/misbehave if resolveGeoHop were consulted for
// this hop.
func TestDeriveHeardKeyGeo_MultiByteHopStillRxlog(t *testing.T) {
	k, l, src, ok := deriveHeardKeyGeo("rx", packetpath.RouteFlood, PayloadGRP_TXT, []string{"aa", "bbccdd"}, "", false, geoRecLat, geoRecLon, nil)
	if !ok || k != "bbccdd" || l != 3 || src != "rxlog" {
		t.Fatalf("2-byte hop: got k=%q l=%d src=%q ok=%v, want k=bbccdd l=3 src=rxlog ok=true", k, l, src, ok)
	}
}

// TestDeriveHeardKeyGeo_AdvertUnaffected: the 0-hop advert branch passes
// through deriveHeardKeyGeo unchanged.
func TestDeriveHeardKeyGeo_AdvertUnaffected(t *testing.T) {
	full := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	k, l, src, ok := deriveHeardKeyGeo("rx", packetpath.RouteFlood, PayloadADVERT, nil, strings.ToUpper(full), true, geoRecLat, geoRecLon, nil)
	if !ok || l != 32 || src != "advert" || k != full {
		t.Fatalf("0-hop advert: got k=%q l=%d src=%q ok=%v", k, l, src, ok)
	}
}

// TestDeriveHeardKeyGeo_DirectRouteNeverEntersGeoPath: a DIRECT-route
// 1-byte hop must stay rejected even with a geo index that WOULD resolve it
// — this feature must not create an exception to the DIRECT-route rule
// (path[last] on a DIRECT route is the route's far end, not who was heard).
func TestDeriveHeardKeyGeo_DirectRouteNeverEntersGeoPath(t *testing.T) {
	full := geoTestPubkey("1a")
	idx := geoIndex{"1a": {{Pubkey: full, Lat: 51.10, Lon: 3.75}}}
	if _, _, _, ok := deriveHeardKeyGeo("rx", packetpath.RouteDirect, PayloadGRP_TXT, []string{"1a"}, "", false, geoRecLat, geoRecLon, idx); ok {
		t.Fatal("DIRECT route must never be attributed, even via the geo path")
	}
	if _, _, _, ok := deriveHeardKeyGeo("rx", packetpath.RouteTransportDirect, PayloadGRP_TXT, []string{"1a"}, "", false, geoRecLat, geoRecLon, idx); ok {
		t.Fatal("TRANSPORT_DIRECT route must never be attributed, even via the geo path")
	}
}

// TestDeriveHeardKeyGeo_TraceNeverEntersGeoPath: TRACE repurposes the path
// bytes as per-hop SNR values, not node hashes — same guard as
// deriveHeardKey, must hold in the geo-aware wrapper too.
func TestDeriveHeardKeyGeo_TraceNeverEntersGeoPath(t *testing.T) {
	full := geoTestPubkey("1a")
	idx := geoIndex{"1a": {{Pubkey: full, Lat: 51.10, Lon: 3.75}}}
	if _, _, _, ok := deriveHeardKeyGeo("rx", packetpath.RouteFlood, PayloadTRACE, []string{"1a"}, "", false, geoRecLat, geoRecLon, idx); ok {
		t.Fatal("FLOOD-routed TRACE must be rejected, even via the geo path")
	}
}

// ─── buildClientReceptionGeo ────────────────────────────────────────────────

// TestBuildClientReceptionGeo_OneByteHopResolvesToGeoSrc mirrors
// TestBuildClientReception but for the geo-aware wrapper: exactly one
// positioned candidate in range produces a ClientReception with src="geo"
// and the candidate's full pubkey.
func TestBuildClientReceptionGeo_OneByteHopResolvesToGeoSrc(t *testing.T) {
	full := geoTestPubkey("1a")
	idx := geoIndex{"1a": {{Pubkey: full, Lat: 51.10, Lon: 3.75}}}
	acc := 8.0
	rec, ok := buildClientReceptionGeo("companionpk", "rx", packetpath.RouteFlood, PayloadGRP_TXT, []string{"1a"}, "", false,
		crF(-7.5), crI(-92), geoRecLat, geoRecLon, &acc, "2026-06-09T12:00:00Z", "2026-06-09T12:00:01Z", idx)
	if !ok || rec.HeardKey != full || rec.HeardKeyLen != 32 || rec.Src != "geo" {
		t.Fatalf("bad geo reception: %+v ok=%v", rec, ok)
	}
}

// TestBuildClientReceptionGeo_ValidationGuardsPreserved: the two guards
// buildClientReception already enforces (empty rxPubkey/rxAt, out-of-range
// lat) must still hold on the geo-aware wrapper.
func TestBuildClientReceptionGeo_ValidationGuardsPreserved(t *testing.T) {
	full := geoTestPubkey("1a")
	idx := geoIndex{"1a": {{Pubkey: full, Lat: 51.10, Lon: 3.75}}}
	if _, ok := buildClientReceptionGeo("", "rx", packetpath.RouteFlood, PayloadGRP_TXT, []string{"1a"}, "", false,
		nil, nil, geoRecLat, geoRecLon, nil, "t", "t", idx); ok {
		t.Fatal("empty rxPubkey must be rejected")
	}
	if _, ok := buildClientReceptionGeo("c", "rx", packetpath.RouteFlood, PayloadGRP_TXT, []string{"1a"}, "", false,
		nil, nil, 99.0, geoRecLon, nil, "t", "t", idx); ok {
		t.Fatal("out-of-range lat must be rejected")
	}
}

// ─── end-to-end via handleClientPacket ─────────────────────────────────────

// TestHandleClientPacketOneByteHopResolvesViaGeoIndex exercises the full
// wiring: a real node seeded with a position in the `nodes` table, the
// periodic geo index refresh (RefreshGeoIndex, normally run by the
// neighbor-edges builder tick — invoked directly here since the test does
// not run that ticker), then a FLOOD packet with a 1-byte hop matching the
// node's prefix. Confirms the client_receptions row lands with src='geo'
// and the node's full pubkey, not the 1-byte prefix.
func TestHandleClientPacketOneByteHopResolvesViaGeoIndex(t *testing.T) {
	s := newTestStore(t)
	full := geoTestPubkey("1a")
	if _, err := s.db.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES (?, 'near-node', 51.10, 3.75)`, full); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshGeoIndex(); err != nil {
		t.Fatal(err)
	}

	// header 0x15 = route_type 1 (FLOOD), payload_type 5 (GRP_TXT).
	// path byte 0x01 = hash_size 1, hop_count 1 → a single 1-byte hop.
	raw := "15" + "01" + "1a" + strings.Repeat("33", 16)
	msg := map[string]interface{}{
		"raw": raw, "direction": "rx", "SNR": 4.5, "RSSI": -101.0,
		"timestamp": "2026-08-17T10:00:00.123Z",
		"gps":       map[string]interface{}{"lat": geoRecLat, "lon": geoRecLon, "acc_m": 8.0},
	}
	handleClientPacket(s, &Config{}, "test", "aa11", msg, nil, nil)

	var n, keylen int
	var src, heardKey string
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(heard_keylen),0), COALESCE(MAX(src),''), COALESCE(MAX(heard_key),'') FROM client_receptions`).
		Scan(&n, &keylen, &src, &heardKey); err != nil {
		t.Fatal(err)
	}
	if n != 1 || src != "geo" || keylen != 32 || heardKey != full {
		t.Fatalf("expected 1 geo reception attributed to %s, got n=%d src=%q keylen=%d heardKey=%q", full, n, src, keylen, heardKey)
	}
}

// TestHandleClientPacketOneByteHopAmbiguousWritesNothing: same shape as
// above, but with two positioned nodes sharing the prefix — no attribution,
// same as the pre-geo behavior for a 1-byte hop.
func TestHandleClientPacketOneByteHopAmbiguousWritesNothing(t *testing.T) {
	s := newTestStore(t)
	a := geoTestPubkey("1a")
	b := geoTestPubkeyFill("1a", "b")
	if _, err := s.db.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES (?, 'a', 51.10, 3.75), (?, 'b', 51.06, 3.80)`, a, b); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshGeoIndex(); err != nil {
		t.Fatal(err)
	}

	raw := "15" + "01" + "1a" + strings.Repeat("33", 16)
	msg := map[string]interface{}{
		"raw": raw, "direction": "rx", "SNR": 4.5, "RSSI": -101.0,
		"timestamp": "2026-08-17T10:00:00.123Z",
		"gps":       map[string]interface{}{"lat": geoRecLat, "lon": geoRecLon, "acc_m": 8.0},
	}
	handleClientPacket(s, &Config{}, "test", "aa11", msg, nil, nil)

	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM client_receptions`).Scan(&n)
	if n != 0 {
		t.Fatalf("ambiguous 1-byte hop must write nothing, got %d rows", n)
	}
}
