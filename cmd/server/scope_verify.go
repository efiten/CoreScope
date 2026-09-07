package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
