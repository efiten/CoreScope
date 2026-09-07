package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// scopeVerifyMaxPacketsPerTarget bounds the per-target evidence list. AGENTS.md
// rule 0 forbids unbounded structures, and the corroboration threshold is 2 —
// past a few hundred packets more evidence changes no verdict, it only costs
// memory. scopeAuditTargetAgg.unmatchedPackets keeps counting past this: the
// count is the honest total, the list is the working set.
const scopeVerifyMaxPacketsPerTarget = 512

// scopeHMACInputs pulls the three values needed to test a region hypothesis
// against one packet: the payload type and raw payload bytes the sender HMACed,
// and the resulting two-byte code it put on the wire.
//
// It deliberately does NOT call DecodePacket. That runs decodePayload, which
// attempts channel decryption and signature validation — work this has no use
// for, repeated over every unmatched packet on every audit refresh. Walking the
// offsets is all that is needed, and it reuses decodeHeader/isTransportRoute/
// decodePath so the offset arithmetic is not duplicated from DecodePacket.
//
// ok is false for anything that cannot carry a region scope: malformed hex, a
// truncated header, an invalid path byte, or a non-transport route. A plain
// FLOOD packet has no transport codes at all, so there is no code1 to compare
// against and HMACing it could only waste time.
func scopeHMACInputs(rawHex string) (payloadType byte, payload []byte, code1 string, ok bool) {
	buf, err := hex.DecodeString(strings.TrimSpace(rawHex))
	if err != nil || len(buf) < 2 {
		return 0, nil, "", false
	}
	header := decodeHeader(buf[0])
	if !isTransportRoute(header.RouteType) {
		return 0, nil, "", false
	}
	offset := 1
	if len(buf) < offset+4 {
		return 0, nil, "", false
	}
	code1 = strings.ToUpper(hex.EncodeToString(buf[offset : offset+2]))
	offset += 4 // code1 and code2

	if offset >= len(buf) {
		return 0, nil, "", false
	}
	pathByte := buf[offset]
	offset++
	_, consumed, decodeErr := decodePath(pathByte, buf, offset)
	if decodeErr != nil {
		return 0, nil, "", false
	}
	offset += consumed
	if offset > len(buf) {
		return 0, nil, "", false
	}
	rest := buf[offset:]
	if len(rest) == 0 {
		return 0, nil, "", false
	}
	return byte(header.PayloadType), rest, code1, true
}

// regionCode derives the on-wire code1 a sender in region name would emit for
// this payload — the forward direction of what matchingRegions inverts in the
// ingestor (cmd/ingestor/main.go). The two must stay in step: key is
// SHA256("#name")[:16], the MAC covers payloadType followed by the payload, the
// code is the first two MAC bytes little-endian, and 0x0000/0xFFFF are reserved
// and nudged. Any divergence here silently produces regions that never verify.
//
// The leading '#' is optional because callers hold normScope'd names (the audit
// strips it) while the key is over the '#'-prefixed form.
//
// Case is significant and must stay so: the key is a hash over the raw bytes of
// "#name", so "#BEHSS" and "#behss" are different regions on the wire.
func regionCode(name string, payloadType byte, payload []byte) string {
	if !strings.HasPrefix(name, "#") {
		name = "#" + name
	}
	sum := sha256.Sum256([]byte(name))
	mac := hmac.New(sha256.New, sum[:16])
	mac.Write([]byte{payloadType})
	mac.Write(payload)
	h := mac.Sum(nil)
	code := uint16(h[0]) | uint16(h[1])<<8
	if code == 0 {
		code = 1
	} else if code == 0xFFFF {
		code = 0xFFFE
	}
	return strings.ToUpper(hex.EncodeToString([]byte{byte(code & 0xFF), byte(code >> 8)}))
}

// unmatchedTransmissionRow is one transmission that carried a transport scope
// no configured region key matched, with the raw bytes needed to test a region
// hypothesis against it.
type unmatchedTransmissionRow struct {
	txID   int64
	rawHex string
}

// unmatchedTransmissionsInWindow is the SECOND, narrow query behind the audit -
// deliberately not a widening of scopeAuditForwarderScanQuery.
//
// That scan returns one row per hop per flood packet: on a 2,000-packet sample
// after M0 that is 19,049 rows, and carrying raw_hex on every one of them would
// load the hot path to serve a few hundred packets. This selects only the
// transmissions that are actually candidates - an empty scope_name inside the
// window, ~400 over 7 days on the reference deployment - and the main scan is
// left exactly as it is.
//
// An empty scope_name is the "transport-scoped but unnameable" state; NULL
// means the packet carried no scope at all and can never verify against a
// region. The route filter matches the forwarder scan's, so the two agree on
// which packets count as forwarded.
//
// "Empty" is spelled out above rather than written as the two-single-quote
// literal on purpose: gofmt applies the old godoc typographic substitution
// inside doc comments and rewrites that digraph into a closing curly quote,
// which silently misstates the one value this query keys on — and puts it back
// on every gofmt run.
//
// Selection only: a row whose raw_hex cannot be walked is still returned, and
// dropped by newScopeVerifier. Filtering that in SQL is not possible and
// filtering it here would hide how many candidates the window actually held.
func (s *PacketStore) unmatchedTransmissionsInWindow(sinceISO string) ([]unmatchedTransmissionRow, error) {
	rows, err := s.db.conn.Query(`
		SELECT t.id, t.raw_hex
		FROM transmissions t
		WHERE t.first_seen >= ?
		  AND t.scope_name = ''
		  AND `+scopeConformanceForwarderRouteTypesSQL, sinceISO)
	if err != nil {
		return nil, fmt.Errorf("unmatched transmissions scan: %w", err)
	}
	defer rows.Close()

	var out []unmatchedTransmissionRow
	for rows.Next() {
		var r unmatchedTransmissionRow
		if err := rows.Scan(&r.txID, &r.rawHex); err != nil {
			return nil, fmt.Errorf("unmatched transmissions scan row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("unmatched transmissions rows: %w", err)
	}
	return out, nil
}

// scopeVerifyMinCorroboration is how many of a repeater's own unmatched packets
// must derive to a declared region before that region counts as observed.
//
// One is not enough, and the arithmetic is the whole argument: code1 is two
// bytes, so an unrelated name matches a given packet with probability 1/65536.
// Across ~400 unmatched packets and ~124 distinct declared names, chance alone
// produces roughly one false match per refresh. Two matches on the same region
// for the same repeater is (1/65536)^2 - about one in four billion. Raising
// this costs recall on quiet regions; lowering it to 1 makes the feature
// unsound, not merely noisy.
const scopeVerifyMinCorroboration = 2

// scopeVerifier answers "how many of these transmissions are region X" while
// computing each (region, transmission) pair at most once.
//
// The memo is not a nicety. Naively the audit would do
// targets x declaredNames x unmatchedPackets HMACs - 205 x 9 x 400 is roughly
// 740,000, about 0.7s per refresh. The work depends only on the pair, and
// distinct pairs are distinctNames x packets = 124 x 400, roughly 50,000 and
// ~50ms. That difference is what puts this inside AGENTS.md rule 0.
//
// Not safe for concurrent use: one verifier is built per audit computation,
// which handleScopeAudit already serialises behind its cache.
type scopeVerifier struct {
	packets map[int64]scopeVerifyInputs
	memo    map[scopeVerifyKey]bool
	// hmacCount is incremented per actual derivation, asserted by the memo
	// test so a future refactor cannot quietly reintroduce the naive cost.
	hmacCount int
}

type scopeVerifyInputs struct {
	payloadType byte
	payload     []byte
	code1       string
	ok          bool
}

type scopeVerifyKey struct {
	region string
	txID   int64
}

// newScopeVerifier parses each row once. A row whose raw_hex cannot be walked
// is kept with ok=false rather than dropped, so the memo still short-circuits
// repeat lookups for it.
func newScopeVerifier(rows []unmatchedTransmissionRow) *scopeVerifier {
	v := &scopeVerifier{
		packets: make(map[int64]scopeVerifyInputs, len(rows)),
		memo:    map[scopeVerifyKey]bool{},
	}
	for _, r := range rows {
		pt, payload, code1, ok := scopeHMACInputs(r.rawHex)
		v.packets[r.txID] = scopeVerifyInputs{payloadType: pt, payload: payload, code1: code1, ok: ok}
	}
	return v
}

// matches reports whether transmission txID is region, deriving at most once
// per pair.
func (v *scopeVerifier) matches(region string, txID int64) bool {
	key := scopeVerifyKey{region: region, txID: txID}
	if got, seen := v.memo[key]; seen {
		return got
	}
	in, known := v.packets[txID]
	got := false
	if known && in.ok {
		v.hmacCount++
		got = regionCode(region, in.payloadType, in.payload) == in.code1
	}
	v.memo[key] = got
	return got
}

// evidence counts, for each declared region, how many of txIDs derive to it.
// Regions with zero matches are absent from the result rather than present
// with 0, so the map is directly the "we found something" set.
func (v *scopeVerifier) evidence(txIDs []int64, declaredRegions []string) map[string]int {
	out := map[string]int{}
	for _, region := range declaredRegions {
		n := 0
		for _, txID := range txIDs {
			if v.matches(region, txID) {
				n++
			}
		}
		if n > 0 {
			out[region] = n
		}
	}
	return out
}

// verified returns the regions in an evidence map that clear the corroboration
// threshold, sorted so the response is stable across refreshes.
func (v *scopeVerifier) verified(evidence map[string]int) []string {
	var out []string
	for region, n := range evidence {
		if n >= scopeVerifyMinCorroboration {
			out = append(out, region)
		}
	}
	sort.Strings(out)
	return out
}
