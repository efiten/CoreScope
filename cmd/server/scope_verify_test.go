package main

import (
	"encoding/hex"
	"strings"
	"testing"
)

// realTransportFloodPacket is transmission 0a065d41d51f1f77 from the live
// instance, captured 2026-09-07. Header 0x14 = route_type 0 (TRANSPORT_FLOOD),
// payload_type 5 (GRP_TXT); transport codes 9209/0000; path byte 0x41 =
// hash_size 2, one hop "E3D3"; the rest is payload.
//
// A hand-built fixture would only prove the parser agrees with itself. This
// packet is the one that started the investigation: its code1 is exactly the
// code #fm-112 derives over its own payload, which is why the audit showed
// fm-112 as "not observed" for a repeater that was forwarding it.
const realTransportFloodPacket = "149209000041E3D3EC2D4481DA70893CD71B763958B064A9AAC011D54223FF8A0140CBB4093653BC61D67C960E3ECCE6639CC9FF1147AA6D0F9017"

func TestScopeHMACInputsParsesRealPacket(t *testing.T) {
	payloadType, payload, code1, ok := scopeHMACInputs(realTransportFloodPacket)
	if !ok {
		t.Fatal("scopeHMACInputs returned ok=false for a valid transport-flood packet")
	}
	if payloadType != 5 {
		t.Errorf("payloadType = %d, want 5 (GRP_TXT)", payloadType)
	}
	if code1 != "9209" {
		t.Errorf("code1 = %q, want %q", code1, "9209")
	}
	if len(payload) != 51 {
		t.Errorf("len(payload) = %d, want 51", len(payload))
	}
	if got := strings.ToUpper(hex.EncodeToString(payload[:4])); got != "EC2D4481" {
		t.Errorf("payload starts %q, want %q — offset walked wrong", got, "EC2D4481")
	}
}

func TestScopeHMACInputsRejectsNonTransportRoutes(t *testing.T) {
	// A plain FLOOD packet carries no transport codes, so it has no code1 to
	// verify against. Returning ok=false rather than a zero code1 keeps the
	// caller from HMACing packets that can never match anything.
	//
	// Header 0x15 = route_type 1 (FLOOD), payload_type 5. No transport codes,
	// so the path byte follows the header directly.
	_, _, _, ok := scopeHMACInputs("15" + "41" + "E3D3" + "AABBCC")
	if ok {
		t.Error("ok = true for a non-transport route, want false — there is no code1 to verify")
	}
}

func TestScopeHMACInputsRejectsMalformed(t *testing.T) {
	for _, c := range []struct{ hex, why string }{
		{"", "empty"},
		{"zz", "not hex"},
		{"14", "header only, no transport codes"},
		{"1492090000", "transport codes but no path byte"},
		{"149209000041", "path byte claims one 2-byte hop, none present"},
		// pathByte 0xC0: upper two bits 11 -> hash_size 4, which firmware
		// reserves and isValidPathLen rejects even at hash_count 0
		// (cmd/server/decoder.go, mirroring Packet.cpp:13-18).
		{"1492090000C0" + strings.Repeat("00", 8), "hash_size 4 is reserved"},
	} {
		if _, _, _, ok := scopeHMACInputs(c.hex); ok {
			t.Errorf("ok = true for %q (%s), want false", c.hex, c.why)
		}
	}
}

func TestRegionCodeMatchesTheRealPacket(t *testing.T) {
	// The end-to-end arithmetic, against a packet whose true region is known.
	payloadType, payload, code1, ok := scopeHMACInputs(realTransportFloodPacket)
	if !ok {
		t.Fatal("setup: scopeHMACInputs failed")
	}
	if got := regionCode("fm-112", payloadType, payload); got != code1 {
		t.Errorf("regionCode(fm-112) = %q, want %q — this packet IS fm-112", got, code1)
	}
	// Both spellings must agree: the key is SHA256 over "#name", and callers
	// hand us names with the '#' already stripped by normScope.
	if got := regionCode("#fm-112", payloadType, payload); got != code1 {
		t.Errorf("regionCode(#fm-112) = %q, want %q — leading '#' must be optional", got, code1)
	}
	// A region the repeater also declares, which this packet is NOT.
	if got := regionCode("behss", payloadType, payload); got == code1 {
		t.Errorf("regionCode(behss) = %q, must not equal fm-112's code1", got)
	}
}

func TestRegionCodeIsCaseSensitive(t *testing.T) {
	// The key is SHA256 over the raw bytes of "#name", so "#BEHSS" and
	// "#behss" are different regions. Folding case here would silently name
	// traffic for a region nobody configured.
	payloadType, payload, _, _ := scopeHMACInputs(realTransportFloodPacket)
	if regionCode("behss", payloadType, payload) == regionCode("BEHSS", payloadType, payload) {
		t.Error("regionCode folded case — the key is a hash over raw bytes and must not")
	}
}
