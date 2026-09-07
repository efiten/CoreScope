package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
// region. (Written out rather than as the two-quote literal: gofmt rewrites
// that digraph into a typographic quote inside doc comments, which silently
// misstates the value this whole query keys on.)
// The route filter matches the forwarder scan's, so the two agree on which
// packets count as forwarded.
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
