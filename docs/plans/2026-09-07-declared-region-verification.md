# Declared-Region Verification (M1b) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn a declared region's chip green when this instance can *prove* the repeater forwards it, instead of leaving it grey with a footnote — by testing the repeater's own declared names against its own unnameable traffic.

**Architecture:** For repeater R declaring region X, derive `SHA256("#X")[:16]`, HMAC each of R's unmatched forwarded packets with it, and compare to that packet's `code1`. Same computation `matchingRegions` performs in the ingestor, with the candidate set narrowed from every configured key to R's own ~9 declarations. Read-time in `cmd/server/`, nothing written, no config. A region needs **two** corroborating packets to go green.

**Tech Stack:** Go 1.x (`cmd/server`, stdlib `crypto/hmac`, `crypto/sha256`, `testing`), vanilla JS frontend (`public/`).

**Spec:** `docs/specs/2026-09-07-auto-region-keys-design.md`, "Architecture 3b" and "M1b" (`55f46a1d`).

**Depends on M0 and M1**, both landed (`d93b4463`, and `70e6bcd5`/`ca464b59`/`79f38ef1`/`93a0c385`). M0 is what makes a mid-path repeater attributable at all; M1 is what counts its unmatched traffic. This plan turns that count into an answer.

**Independent of M2** (`docs/plans/2026-09-07-auto-derived-region-keys.md`), which is entirely `cmd/ingestor/`. No shared files. Per the spec amendment, M2 no longer fixes the audit — this does.

---

## Why two packets, not one

`code1` is two bytes, so an unrelated region name matches a given packet with probability 1/65536. One match is therefore not evidence: across a network with 400 unmatched packets and 124 declared names, chance alone produces roughly one false match per audit refresh. Two matches on the *same* region for the *same* repeater is (1/65536)² — about one in four billion. That threshold is the whole reason this approach is sound where per-packet naming is not, so it is a named constant with a test, not a literal.

---

## Cost — where it actually sits (measured, not estimated)

Naively this is `targets × declaredNames × unmatchedPackets` HMACs — 205 × 124 × 400 ≈ 10,000,000.

The first cut cached per `(region, transmission)` pair, which cuts the HMACs to `names × packets` ≈ 50,000. **That was not enough, and the plan's original estimate of ~50ms was wrong about why.** Benchmarked at audit scale it took **501ms**: the HMACs had become a rounding error, but the *iteration* was still cubic — 10.2M map lookups at ~49ns each.

The cache is therefore keyed per **region**, holding the set of transmissions deriving to it. A region is HMACed over every packet once; a target then asks one question per declared region instead of one per (region, packet). Most declared regions match nothing, so the common case is a single lookup and no packet loop at all.

Measured after that change: **36ms** at the same worst-case shape (every one of 205 targets declaring all 124 names over all 400 packets). Real rows declare ~9 names and hold far fewer packets, so this is an upper bound with a lot of headroom under the 30s cache.

The lesson worth keeping: caching the expensive operation is not the same as removing the expensive loop. `hmacCount` exists so a test can assert the first, and the benchmark exists because only it catches the second.

---

## File Structure

| File | Responsibility | Change |
|---|---|---|
| `cmd/server/scope_verify.go` | HMAC-input extraction, the memo, the verification pass | **Create** |
| `cmd/server/scope_verify_test.go` | Unit tests + benchmark | **Create** |
| `cmd/server/scopes.go` | `unmatchedTxIDs` on the agg; `RegionEvidence` on the row | Modify |
| `cmd/server/routes.go` | Run verification, subtract from `notObserved` | Modify |
| `cmd/server/scopes_test.go` | Handler-level tests | Modify |
| `public/scope-audit.js` | Evidence-aware chip title and marker; narrow the row caveat | Modify |
| `public/scope-audit.css` | `.sa-chip-verified` | Modify |
| `test-frontend-helpers.js` | Chip and caveat assertions | Modify |
| `docs/api-spec.md` | `regionEvidence` field + note | Modify |

---

### Task 1: Extract the HMAC inputs from `raw_hex`

No payload decoding: `decodePayload` attempts decryption and signature validation, none of which this needs, and running it on every unmatched packet every refresh is pure waste. This walks the offsets only, reusing the decoder helpers that already exist in the package so the offset logic is not duplicated.

**Files:**
- Create: `cmd/server/scope_verify.go`
- Create: `cmd/server/scope_verify_test.go`

- [ ] **Step 1: Write the failing test**

Create `cmd/server/scope_verify_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/server && go test ./... -run 'TestScopeHMACInputs|TestRegionCode' -v`
Expected: FAIL to compile — `undefined: scopeHMACInputs`, `undefined: regionCode`

- [ ] **Step 3: Implement**

Create `cmd/server/scope_verify.go`:

```go
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/server && go test ./... -run 'TestScopeHMACInputs|TestRegionCode' -v`
Expected: PASS (5 tests)

- [ ] **Step 5: Commit**

```bash
git add cmd/server/scope_verify.go cmd/server/scope_verify_test.go
git commit -m "feat(scope-audit): derive a region's on-wire code from a packet's own payload"
```

---

### Task 2: Remember which transmissions were unmatched, per target

M1 counts them. Verification needs to know *which*.

**Files:**
- Modify: `cmd/server/scopes.go` (`scopeAuditTargetAgg`, `ScopeAuditForwarding`)
- Test: `cmd/server/scopes_test.go`

- [ ] **Step 1: Write the failing test**

Append to `cmd/server/scopes_test.go`, after `TestScopeAuditForwardingCountsUnmatchedOnMidPathHop`:

```go
// TestScopeAuditForwardingRecordsUnmatchedTxIDs: the counter M1 added says how
// many, verification needs to know which. The IDs must be de-duplicated the
// same way the counter is — a target appearing twice in one path contributed
// one packet, and counting it twice would let a single packet reach the
// two-corroboration threshold on its own.
func TestScopeAuditForwardingRecordsUnmatchedTxIDs(t *testing.T) {
	s := newScopeTestStore(t)
	hop := testFullPubkeyA[:4]
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionPathAt(t, s, []string{hop, "AAAA", hop}, scopeUnmatched(), RouteFlood, recent)
	seedTransmissionPathAt(t, s, []string{"BBBB", hop}, scopeUnmatched(), RouteFlood, recent)
	seedTransmissionPathAt(t, s, []string{hop}, scopeMatched("#be"), RouteFlood, recent)

	got, err := s.ScopeAuditForwarding("2026-01-01T00:00:00Z", []string{testFullPubkeyA})
	if err != nil {
		t.Fatal(err)
	}
	agg := got[testFullPubkeyA]
	if agg == nil {
		t.Fatalf("want an agg, got none (result = %+v)", got)
	}
	if len(agg.unmatchedTxIDs) != 2 {
		t.Errorf("unmatchedTxIDs = %v, want 2 distinct ids — the twice-hopped packet counts once, and the matched packet not at all", agg.unmatchedTxIDs)
	}
	if agg.unmatchedPackets != int64(len(agg.unmatchedTxIDs)) {
		t.Errorf("unmatchedPackets = %d but %d ids recorded — the count and the ids must not drift", agg.unmatchedPackets, len(agg.unmatchedTxIDs))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/server && go test ./... -run TestScopeAuditForwardingRecordsUnmatchedTxIDs -v`
Expected: FAIL to compile — `agg.unmatchedTxIDs undefined`

- [ ] **Step 3: Add the field**

In `cmd/server/scopes.go`, in `scopeAuditTargetAgg`, immediately after `unmatchedPackets`:

```go
	// unmatchedTxIDs are the transmissions behind unmatchedPackets, kept so
	// declared-region verification can test this target's own declarations
	// against this target's own unnameable traffic (scope_verify.go). Bounded
	// by the window and by scopeVerifyMaxPacketsPerTarget; the same
	// (target, txID) de-duplication that guards unmatchedPackets guards this,
	// so one packet reaching a target by two hops cannot corroborate twice.
	unmatchedTxIDs []int64
```

- [ ] **Step 4: Record them**

In `ScopeAuditForwarding`, extend the unmatched branch added by M1:

```go
			if scopeName.String == "" {
				// Unmatched: transport-scoped, but no configured region key
				// matched code1. Still not part of the declared/observed
				// comparison — it names no region, so it can never satisfy a
				// declaration — but it is the evidence that a notObserved
				// finding on this row may be a gap in this instance's
				// hashRegions rather than in the repeater's forwarding.
				agg.unmatchedPackets++
				if len(agg.unmatchedTxIDs) < scopeVerifyMaxPacketsPerTarget {
					agg.unmatchedTxIDs = append(agg.unmatchedTxIDs, txID)
				}
				continue
			}
```

- [ ] **Step 5: Add the bound**

In `cmd/server/scope_verify.go`:

```go
// scopeVerifyMaxPacketsPerTarget bounds the per-target evidence list. AGENTS.md
// rule 0 forbids unbounded structures, and the corroboration threshold is 2 —
// past a few hundred packets more evidence changes no verdict, it only costs
// memory. Note that unmatchedPackets keeps counting past this: the count is the
// honest total, the list is the working set.
const scopeVerifyMaxPacketsPerTarget = 512
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd cmd/server && go test ./... -run 'TestScopeAuditForwarding' -v`
Expected: PASS, including M1's two unmatched tests.

Note the deliberate asymmetry the new test pins: `unmatchedPackets` counts every unmatched packet, `unmatchedTxIDs` stops at 512. The test uses 2, so they agree there; a comment in the field doc explains the divergence above the cap.

- [ ] **Step 7: Commit**

```bash
git add cmd/server/scopes.go cmd/server/scopes_test.go cmd/server/scope_verify.go
git commit -m "feat(scope-audit): record which transmissions were unmatched, per target"
```

---

### Task 3: The narrow query

**Files:**
- Modify: `cmd/server/scope_verify.go`
- Test: `cmd/server/scope_verify_test.go`

- [ ] **Step 1: Write the failing test**

Append to `cmd/server/scope_verify_test.go`:

```go
func TestUnmatchedTransmissionsInWindow(t *testing.T) {
	s := newScopeTestStore(t)
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	old := "2020-01-01T00:00:00Z"
	seedTransmissionRouteAt(t, s, "E3D3", scopeUnmatched(), RouteFlood, recent)
	seedTransmissionRouteAt(t, s, "E3D3", scopeMatched("#be"), RouteFlood, recent)
	seedTransmissionRouteAt(t, s, "E3D3", scopeUnscoped(), RouteFlood, recent)
	seedTransmissionRouteAt(t, s, "E3D3", scopeUnmatched(), RouteFlood, old)

	since := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	got, err := s.unmatchedTransmissionsInWindow(since)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1 — only the recent scope_name='' row qualifies", len(got))
	}
	// scopeUnmatched() seeds raw_hex 'AA', which scopeHMACInputs rejects. The
	// query's job is selection; unparseable rows are dropped by the caller, so
	// they must still be returned here rather than filtered in SQL.
	if got[0].txID == 0 {
		t.Error("txID = 0, want the transmission's real id")
	}
}
```

Add `"time"` to the test file's imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/server && go test ./... -run TestUnmatchedTransmissionsInWindow -v`
Expected: FAIL to compile — `s.unmatchedTransmissionsInWindow undefined`

- [ ] **Step 3: Implement**

Append to `cmd/server/scope_verify.go`:

```go
// unmatchedTransmissionRow is one transmission that carried a transport scope
// no configured region key matched, with the raw bytes needed to test a region
// hypothesis against it.
type unmatchedTransmissionRow struct {
	txID   int64
	rawHex string
}

// unmatchedTransmissionsInWindow is the SECOND, narrow query behind the audit —
// deliberately not a widening of scopeAuditForwarderScanQuery.
//
// That scan returns one row per hop per flood packet: on a 2,000-packet sample
// after M0 that is 19,049 rows, and carrying raw_hex on every one of them would
// load the hot path to serve a few hundred packets. This selects only the
// transmissions that are actually candidates — scope_name = '' inside the
// window, ~400 over 7 days on the reference deployment — and the main scan is
// left exactly as it is.
//
// scope_name = '' is the "transport-scoped but unnameable" state; NULL means
// the packet carried no scope at all and can never verify against a region.
// The route filter matches the forwarder scan's, so the two agree on which
// packets count as forwarded.
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
```

Add `"fmt"` to the file's imports.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/server && go test ./... -run TestUnmatchedTransmissionsInWindow -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/server/scope_verify.go cmd/server/scope_verify_test.go
git commit -m "feat(scope-audit): narrow query for the window's unmatched transmissions"
```

---

### Task 4: The memoised verification pass

**Files:**
- Modify: `cmd/server/scope_verify.go`
- Test: `cmd/server/scope_verify_test.go`

- [ ] **Step 1: Write the failing test**

Append to `cmd/server/scope_verify_test.go`:

```go
// buildVerifierFromPackets is a test helper: wraps raw hex strings as rows the
// verifier consumes, with ids 1..N in order.
func buildVerifierFromPackets(t *testing.T, hexes ...string) *scopeVerifier {
	t.Helper()
	rows := make([]unmatchedTransmissionRow, 0, len(hexes))
	for i, h := range hexes {
		rows = append(rows, unmatchedTransmissionRow{txID: int64(i + 1), rawHex: h})
	}
	return newScopeVerifier(rows)
}

func TestScopeVerifierNeedsTwoCorroboratingPackets(t *testing.T) {
	// One match is 1-in-65536 and must not be enough; a second makes it
	// (1/65536)^2. This threshold is the reason the approach is sound.
	v := buildVerifierFromPackets(t, realTransportFloodPacket)
	one := v.evidence([]int64{1}, []string{"fm-112"})
	if one["fm-112"] != 1 {
		t.Fatalf("evidence = %v, want fm-112:1", one)
	}
	if v.verified(one) != nil && len(v.verified(one)) != 0 {
		t.Errorf("verified = %v, want none — one corroborating packet is not evidence", v.verified(one))
	}

	// The same packet twice under different ids: two distinct transmissions
	// both deriving to fm-112.
	v2 := buildVerifierFromPackets(t, realTransportFloodPacket, realTransportFloodPacket)
	two := v2.evidence([]int64{1, 2}, []string{"fm-112"})
	if two["fm-112"] != 2 {
		t.Fatalf("evidence = %v, want fm-112:2", two)
	}
	got := v2.verified(two)
	if len(got) != 1 || got[0] != "fm-112" {
		t.Errorf("verified = %v, want [fm-112]", got)
	}
}

func TestScopeVerifierIgnoresRegionsThatDoNotMatch(t *testing.T) {
	v := buildVerifierFromPackets(t, realTransportFloodPacket, realTransportFloodPacket)
	got := v.evidence([]int64{1, 2}, []string{"behss", "be", "eu"})
	if len(got) != 0 {
		t.Errorf("evidence = %v, want empty — none of these regions is this packet", got)
	}
}

func TestScopeVerifierSkipsUnparseablePackets(t *testing.T) {
	// A row whose raw_hex cannot be walked contributes nothing and must not
	// error the pass: one malformed row in the window would otherwise blank
	// the verification for every repeater.
	v := buildVerifierFromPackets(t, "AA", realTransportFloodPacket)
	got := v.evidence([]int64{1, 2}, []string{"fm-112"})
	if got["fm-112"] != 1 {
		t.Errorf("evidence = %v, want fm-112:1 — the malformed row is skipped, the good one still counts", got)
	}
}

func TestScopeVerifierMemoisesAcrossTargets(t *testing.T) {
	// The cost argument: work depends on (region, transmission), not on which
	// target asked. Two targets declaring the same region over the same packets
	// must not double the HMACs.
	v := buildVerifierFromPackets(t, realTransportFloodPacket, realTransportFloodPacket)
	v.evidence([]int64{1, 2}, []string{"fm-112"})
	after := v.hmacCount
	v.evidence([]int64{1, 2}, []string{"fm-112"})
	if v.hmacCount != after {
		t.Errorf("hmacCount %d -> %d on a repeat query, want unchanged — the memo is what keeps this inside rule 0", after, v.hmacCount)
	}
}

func TestScopeVerifierUnknownTxIDIsHarmless(t *testing.T) {
	// A target's unmatchedTxIDs come from a different query than the verifier's
	// rows. They are taken in the same window, but a row pruned between the two
	// must degrade to "no evidence", not panic.
	v := buildVerifierFromPackets(t, realTransportFloodPacket)
	got := v.evidence([]int64{1, 999}, []string{"fm-112"})
	if got["fm-112"] != 1 {
		t.Errorf("evidence = %v, want fm-112:1 — the unknown id contributes nothing", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/server && go test ./... -run TestScopeVerifier -v`
Expected: FAIL to compile — `undefined: newScopeVerifier`

- [ ] **Step 3: Implement**

Append to `cmd/server/scope_verify.go`:

```go
// scopeVerifyMinCorroboration is how many of a repeater's own unmatched packets
// must derive to a declared region before that region counts as observed.
//
// One is not enough and the arithmetic is the whole argument: code1 is two
// bytes, so an unrelated name matches a given packet with probability 1/65536.
// Across ~400 unmatched packets and ~124 distinct declared names, chance alone
// produces roughly one false match per refresh. Two matches on the same region
// for the same repeater is (1/65536)^2 — about one in four billion. Raising
// this costs recall on quiet regions; lowering it to 1 makes the feature
// unsound, not merely noisy.
const scopeVerifyMinCorroboration = 2

// scopeVerifier answers "how many of these transmissions are region X" while
// computing each (region, transmission) pair at most once.
//
// The memo is not a nicety. Naively the audit would do
// targets x declaredNames x unmatchedPackets HMACs — 205 x 9 x 400 is roughly
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
```

Add `"sort"` to the file's imports.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/server && go test ./... -run TestScopeVerifier -v`
Expected: PASS (5 tests)

- [ ] **Step 5: Commit**

```bash
git add cmd/server/scope_verify.go cmd/server/scope_verify_test.go
git commit -m "feat(scope-audit): memoised declared-region verification with a 2-packet threshold"
```

---

### Task 5: Wire it into the handler

**Files:**
- Modify: `cmd/server/scopes.go` (`ScopeAuditRow`)
- Modify: `cmd/server/routes.go` (`handleScopeAudit`)
- Test: `cmd/server/scopes_test.go`

- [ ] **Step 1: Write the failing test**

Append to `cmd/server/scopes_test.go`, after `TestHandleScopeAuditSurfacesUnmatchedPackets`:

```go
// TestHandleScopeAuditVerifiesDeclaredRegion is the case this milestone exists
// for, built from the real packet that started the investigation. A repeater
// declares "fm-112"; this instance holds no key for it, so both packets it
// forwarded are stored unmatched. Verification derives the key from the
// repeater's own declaration, finds two corroborating packets, and the region
// must leave notObserved with its evidence count reported.
func TestHandleScopeAuditVerifiesDeclaredRegion(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	pk := testFullPubkeyA
	insertDeclared(t, srv, pk, time.Now().UTC().Format(time.RFC3339), "fm-112,behss", 0)
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedUnmatchedRawAt(t, srv.store, pk[:4], realTransportFloodPacket, RouteTransportFlood, recent)
	seedUnmatchedRawAt(t, srv.store, pk[:4], realTransportFloodPacket, RouteTransportFlood, recent)

	got := getScopeAudit(t, router, "")
	if len(got.Repeaters) != 1 {
		t.Fatalf("repeaters = %+v, want 1", got.Repeaters)
	}
	row := got.Repeaters[0]
	if row.RegionEvidence["fm-112"] != 2 {
		t.Errorf("regionEvidence = %v, want fm-112:2", row.RegionEvidence)
	}
	for _, n := range row.NotObserved {
		if n == "fm-112" {
			t.Errorf("notObserved = %v, must not contain fm-112 — two corroborating packets prove it is forwarded", row.NotObserved)
		}
	}
	if len(row.NotObserved) != 1 || row.NotObserved[0] != "behss" {
		t.Errorf("notObserved = %v, want [\"behss\"] — that region has no corroborating traffic here", row.NotObserved)
	}
}

// TestHandleScopeAuditDoesNotVerifyOnOnePacket: a single match is 1-in-65536
// and must leave the region in notObserved, with its count still reported so a
// client can say "one hit, not enough".
func TestHandleScopeAuditDoesNotVerifyOnOnePacket(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	pk := testFullPubkeyA
	insertDeclared(t, srv, pk, time.Now().UTC().Format(time.RFC3339), "fm-112", 0)
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedUnmatchedRawAt(t, srv.store, pk[:4], realTransportFloodPacket, RouteTransportFlood, recent)

	got := getScopeAudit(t, router, "")
	row := got.Repeaters[0]
	if row.RegionEvidence["fm-112"] != 1 {
		t.Errorf("regionEvidence = %v, want fm-112:1", row.RegionEvidence)
	}
	if len(row.NotObserved) != 1 || row.NotObserved[0] != "fm-112" {
		t.Errorf("notObserved = %v, want [\"fm-112\"] — one corroborating packet is not evidence", row.NotObserved)
	}
}

// TestHandleScopeAuditLeavesCleanRowsAlone: a repeater whose declared regions
// are all observed by name, with no unmatched traffic at all, must be untouched
// by verification — no evidence, no change to notObserved, and an empty (not
// null) regionEvidence so a client can iterate it without a guard.
func TestHandleScopeAuditLeavesCleanRowsAlone(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	pk := testFullPubkeyA
	insertDeclared(t, srv, pk, time.Now().UTC().Format(time.RFC3339), "be", 0)
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionRouteAt(t, srv.store, pk[:4], scopeMatched("#be"), RouteFlood, recent)

	got := getScopeAudit(t, router, "")
	row := got.Repeaters[0]
	if len(row.NotObserved) != 0 {
		t.Errorf("notObserved = %v, want empty", row.NotObserved)
	}
	if row.RegionEvidence == nil {
		t.Error("regionEvidence = nil, want an empty object — a client must not need a null guard")
	}
	if len(row.RegionEvidence) != 0 {
		t.Errorf("regionEvidence = %v, want empty — nothing needed verifying here", row.RegionEvidence)
	}
}

// seedUnmatchedRawAt seeds one unmatched transmission carrying a real raw_hex,
// attributed to forwarder. Distinct from seedTransmissionRouteAt, which seeds
// raw_hex 'AA' — fine for tests that never parse it, useless here.
func seedUnmatchedRawAt(t *testing.T, s *PacketStore, forwarder, rawHex string, routeType int, firstSeen string) {
	t.Helper()
	scopeSeedCounter++
	hash := fmt.Sprintf("scoperaw%d", scopeSeedCounter)
	res, err := s.db.conn.Exec(
		`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, code1, code2, scope_name)
		 VALUES (?, ?, ?, ?, 5, '9209', '0000', '')`,
		rawHex, hash, firstSeen, routeType)
	if err != nil {
		t.Fatal(err)
	}
	txID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.conn.Exec(
		`INSERT INTO observations (transmission_id, path_json, timestamp) VALUES (?, ?, 0)`,
		txID, `["`+strings.ToUpper(forwarder)+`"]`); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/server && go test ./... -run 'TestHandleScopeAuditVerifies|TestHandleScopeAuditDoesNotVerify' -v`
Expected: FAIL to compile — `row.RegionEvidence undefined`

- [ ] **Step 3: Add the field**

In `cmd/server/scopes.go`, after `ObservedUnmatchedPackets`:

```go
	// RegionEvidence maps a declared region to how many of this repeater's own
	// unmatched forwarded packets derive to it — see scope_verify.go. A region
	// reaching scopeVerifyMinCorroboration is removed from NotObserved, so this
	// field is not what decides the chip's colour; NotObserved remains the
	// single source of that, and this exists so a client can say HOW a region
	// was established, and can explain a region that got exactly one hit and
	// therefore stayed grey.
	//
	// Absent regions simply had no matching traffic. Never nil in the response
	// — an empty object and a missing key mean the same thing, and an empty map
	// is the cheaper thing for a client to iterate.
	RegionEvidence map[string]int `json:"regionEvidence"`
```

- [ ] **Step 4: Run verification in the handler**

In `cmd/server/routes.go`, in `handleScopeAudit`, immediately after the `forwarding, err = s.store.ScopeAuditForwarding(...)` block:

```go
	// Declared-region verification (M1b): a region this instance holds no key
	// for is unnameable, not absent, and the audit can settle which by deriving
	// the key from the repeater's own declaration and testing it against that
	// repeater's own unnameable traffic. One verifier serves every row so each
	// (region, transmission) pair is derived at most once.
	//
	// A failure here degrades to "no verification" rather than failing the
	// request: the audit was useful before this existed and must stay useful if
	// the extra query errors.
	var verifier *scopeVerifier
	if s.store != nil {
		unmatchedRows, uErr := s.store.unmatchedTransmissionsInWindow(sinceISO)
		if uErr != nil {
			log.Printf("[scope-audit] declared-region verification unavailable: %v", uErr)
		} else {
			verifier = newScopeVerifier(unmatchedRows)
		}
	}
```

Then, in the per-repeater loop, replace the `notObserved` construction:

```go
		notObserved := []string{}
		for _, rgn := range declaredNamed {
			if agg == nil || agg.scopes[rgn] == nil {
				notObserved = append(notObserved, rgn)
			}
		}
```

with:

```go
		// Verify the declared regions this repeater has no named evidence for,
		// against its own unmatched traffic. Regions already observed by name
		// need no verification and are not tested — that keeps the candidate
		// set to exactly the open questions.
		unnamed := []string{}
		for _, rgn := range declaredNamed {
			if agg == nil || agg.scopes[rgn] == nil {
				unnamed = append(unnamed, rgn)
			}
		}
		regionEvidence := map[string]int{}
		verifiedSet := map[string]bool{}
		if verifier != nil && agg != nil && len(unnamed) > 0 {
			regionEvidence = verifier.evidence(agg.unmatchedTxIDs, unnamed)
			for _, rgn := range verifier.verified(regionEvidence) {
				verifiedSet[rgn] = true
			}
		}
		notObserved := []string{}
		for _, rgn := range unnamed {
			if !verifiedSet[rgn] {
				notObserved = append(notObserved, rgn)
			}
		}
```

and add to the `ScopeAuditRow` literal:

```go
			RegionEvidence:           regionEvidence,
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd cmd/server && gofmt -w routes.go scopes.go && go test ./... -run 'TestHandleScopeAudit' -v`
Expected: PASS, including M1's `TestHandleScopeAuditSurfacesUnmatchedPackets` and the sorting tests — a verified region leaving `notObserved` changes a row's rank, so confirm the sort tests still hold rather than assuming.

- [ ] **Step 6: Commit**

```bash
git add cmd/server/scopes.go cmd/server/routes.go cmd/server/scopes_test.go
git commit -m "feat(scope-audit): verify declared regions against a repeater's own unnameable traffic"
```

---

### Task 6: Show how a region was established

**Files:**
- Modify: `public/scope-audit.js` (`mergedScopeChips`, `unmatchedCaveat`)
- Modify: `public/scope-audit.css`
- Test: `test-frontend-helpers.js`

- [ ] **Step 1: Write the failing test**

Append to `test-frontend-helpers.js`, inside the existing `mergedScopeChips` block (before its closing `}`):

```js
  test('a region verified against the repeater own declaration is green, and says so', () => {
    const h = chips({ declaredRegions: ['fm-112'], notObserved: [], regionEvidence: { 'fm-112': 23 } });
    assert.ok(h.includes('sa-chip-observed'), 'still green — it is observed');
    assert.ok(h.includes('sa-chip-verified'), 'but marked as established differently');
    assert.ok(h.includes('23'), 'the tooltip states how much evidence there is');
  });

  test('a region observed by name carries no verified marker', () => {
    const h = chips({ declaredRegions: ['be'], notObserved: [], regionEvidence: {} });
    assert.ok(h.includes('sa-chip-observed'));
    assert.ok(!h.includes('sa-chip-verified'), 'a normally-named region is not a verification');
  });

  test('a single-hit region stays grey and its tooltip explains why', () => {
    const h = chips({ declaredRegions: ['fm-112'], notObserved: ['fm-112'], regionEvidence: { 'fm-112': 1 } });
    assert.ok(h.includes('sa-chip-unobserved'), 'one hit is not enough to turn it green');
    assert.ok(/one match/i.test(h), 'must say why one hit was not accepted');
  });

  test('a missing regionEvidence field renders as before (older server)', () => {
    const h = chips({ declaredRegions: ['be'], notObserved: ['be'] });
    assert.ok(h.includes('sa-chip-unobserved'));
    assert.ok(!h.includes('sa-chip-verified'));
  });
```

- [ ] **Step 2: Run test to verify it fails**

Run: `node test-frontend-helpers.js`
Expected: FAIL — the `sa-chip-verified` assertions, since `mergedScopeChips` ignores `regionEvidence`.

- [ ] **Step 3: Implement**

In `public/scope-audit.js`, replace `mergedScopeChips`'s body:

```js
  function mergedScopeChips(row) {
    var missing = Object.create(null);
    row.notObserved.forEach(function (n) { missing[n] = true; });
    var evidence = row.regionEvidence || {};
    var chips = row.declaredRegions.map(function (n) {
      var observed = !missing[n];
      var hits = evidence[n] || 0;
      // A green chip with evidence was established by verifying the repeater's
      // own declaration against its own unnameable traffic, not by matching a
      // configured region key. Same colour — it is observed either way — with
      // a dotted underline so a reader can tell the two apart without a third
      // colour competing for attention.
      var verified = observed && hits > 0;
      var cls = 'sa-chip ' + (observed ? 'sa-chip-observed' : 'sa-chip-unobserved') + (verified ? ' sa-chip-verified' : '');
      var title;
      if (verified) {
        title = n + ': observed — ' + hits + ' forwarded packet' + (hits === 1 ? '' : 's') +
          ' in this window derive to this region, verified against the repeater’s own declared list. ' +
          'This instance holds no hashRegions key for it, so it could not be named directly.';
      } else if (observed) {
        title = n + ': observed forwarding in this window';
      } else if (hits === 1) {
        title = n + ': declared, and exactly one forwarded packet derives to it — that is one match in 65536 by chance alone, ' +
          'so it is not treated as evidence. Two would be.';
      } else {
        title = n + ': declared, but no forwarding observed in this window';
      }
      return '<span class="' + cls + '" title="' + escapeHtml(title) + '">' + escapeHtml(n) + '</span>';
    });
    if (!chips.length) return '<span class="text-muted">—</span>';
    return chips.join(' ');
  }
```

- [ ] **Step 4: Style the marker**

In `public/scope-audit.css`, after `.sa-chip-unmatched`:

```css
/* Verified-by-declaration: same green as any observed chip, since the region IS
   observed. The dotted underline distinguishes how that was established without
   introducing a third colour into a column that already carries two. */
.sa-chip-verified { text-decoration: underline dotted; text-underline-offset: 2px; }
```

- [ ] **Step 5: Narrow the row caveat**

`unmatchedCaveat` currently fires whenever a row has any unmatched traffic. After verification, most of that traffic is explained, and a caveat that fires when the question has been answered is noise. Replace its guard:

```js
  function unmatchedCaveat(row) {
    var n = row.observedUnmatchedPackets;
    if (!n) return '';
    // Traffic already accounted for by verification is explained, not
    // mysterious. What is left over is the interesting case: this repeater
    // forwards a region it does not declare AND that this instance cannot
    // name. Reporting the full count here would re-raise a question the
    // Scopes column has just answered.
    var explained = 0;
    var evidence = row.regionEvidence || {};
    Object.keys(evidence).forEach(function (k) { explained += evidence[k]; });
    var left = n - explained;
    if (left <= 0) return '';
    var label = escapeHtml(left) + ' forwarded packet' + (left === 1 ? '' : 's');
    return ' <span class="sa-chip sa-chip-unmatched" title="' + label +
      ' in this window carried a region scope this CoreScope instance holds no key for, and match none of this repeater’s declared regions. ' +
      'So this repeater forwards at least one region it does not declare, which this instance also cannot name.">' +
      label + ' unexplained</span>';
  }
```

- [ ] **Step 6: Update the caveat's existing tests**

Three M1 assertions in the `unmatchedCaveat` block assert the old wording. Replace them exactly:

```js
  test('a non-zero count renders a chip carrying the number', () => {
    const h = caveat({ observedUnmatchedPackets: 148 });
    assert.ok(h.includes('sa-chip-unmatched'), 'should carry its own class');
    assert.ok(h.includes('148'), 'should state the count, not just that there is one');
  });

  test('singular and plural are both grammatical', () => {
    assert.ok(caveat({ observedUnmatchedPackets: 1 }).includes('1 forwarded packet '));
    assert.ok(caveat({ observedUnmatchedPackets: 2 }).includes('2 forwarded packets '));
  });

  test('the title names the cause, not just the symptom', () => {
    const h = caveat({ observedUnmatchedPackets: 5 });
    assert.ok(h.includes('hashRegions'), 'must name the config key that fixes it');
  });
```

with:

```js
  test('a non-zero unexplained count renders a chip carrying the number', () => {
    const h = caveat({ observedUnmatchedPackets: 148 });
    assert.ok(h.includes('sa-chip-unmatched'), 'should carry its own class');
    assert.ok(h.includes('148'), 'with no evidence to subtract, the whole count is unexplained');
    assert.ok(h.includes('unexplained'), 'the word changed with the meaning');
  });

  test('singular and plural are both grammatical', () => {
    assert.ok(caveat({ observedUnmatchedPackets: 1 }).includes('1 forwarded packet '));
    assert.ok(caveat({ observedUnmatchedPackets: 2 }).includes('2 forwarded packets '));
  });

  test('the title says what unexplained traffic implies', () => {
    // The cause is no longer only a hashRegions gap: after verification, what
    // is left over is traffic for a region the repeater does not declare.
    const h = caveat({ observedUnmatchedPackets: 5 });
    assert.ok(/does not declare/i.test(h), 'must state the sharper conclusion');
  });
```

Then add:

```js
  test('traffic fully explained by verification raises no caveat', () => {
    assert.strictEqual(caveat({ observedUnmatchedPackets: 23, regionEvidence: { 'fm-112': 23 } }), '');
  });

  test('only the unexplained remainder is reported', () => {
    const h = caveat({ observedUnmatchedPackets: 30, regionEvidence: { 'fm-112': 23 } });
    assert.ok(h.includes('7 forwarded packets '), 'want the remainder, not the total');
  });
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `node test-frontend-helpers.js`
Expected: PASS, all assertions.

- [ ] **Step 8: Commit**

```bash
git add public/scope-audit.js public/scope-audit.css test-frontend-helpers.js
git commit -m "feat(scope-audit): mark verified regions and narrow the caveat to what stays unexplained"
```

---

### Task 7: Document it

**Files:**
- Modify: `docs/api-spec.md`

- [ ] **Step 1: Add the field to the payload block**

In the `GET /api/scope-audit` response block, after the `observedUnmatchedPackets` line (add a comma to it):

```
      "observedUnmatchedPackets": number,                // forwarded packets whose scope this instance holds no key for — see note below
      "regionEvidence":           { "<region>": number } // declared regions corroborated by this repeater's own unnameable traffic — see note below
```

- [ ] **Step 2: Add the note**

After the `observedUnmatchedPackets` bullet:

```
- `regionEvidence` maps a declared region to how many of this repeater's own unmatched
  forwarded packets derive to it. The server tests each declared region this repeater has
  no *named* evidence for by deriving `SHA256("#region")[:16]` and HMAC-ing that
  repeater's own unmatched packets with it — the same computation the ingestor performs
  at ingest, with the candidate set narrowed to this repeater's declarations. A region
  reaching **2** corroborating packets is removed from `notObserved`: `code1` is two
  bytes, so one match happens by chance with probability 1/65536, while two on the same
  region is (1/65536)². A region with exactly one hit therefore stays in `notObserved`
  **and** appears here with the value 1, so a client can explain why it is still grey.
  `notObserved` remains the single source of truth for whether a region was observed;
  this field says only *how* that was established. The object is always present and may
  be empty.
```

- [ ] **Step 3: Amend the `observedUnmatchedPackets` note**

That note predates verification. Append to it:

```
  Since M1b, part of this count is explained: packets counted in `regionEvidence` are
  attributable to a declared region after all. A client showing this as a caveat should
  subtract them and report only the remainder, which carries a sharper meaning — traffic
  this repeater forwards for a region it does **not** declare and this instance cannot
  name.
```

- [ ] **Step 4: Commit**

```bash
git add docs/api-spec.md
git commit -m "docs(api): document regionEvidence on GET /api/scope-audit"
```

---

### Task 8: Benchmark and verify

- [ ] **Step 1: Write the benchmark**

Append to `cmd/server/scope_verify_test.go`:

```go
// BenchmarkScopeVerifierAudit models a full audit refresh: every declared name
// against every unmatched packet, once, through the memo. The naive shape would
// be targets x names x packets; this asserts the memo keeps it at names x
// packets, which is what makes the feature affordable (AGENTS.md rule 0).
func BenchmarkScopeVerifierAudit(b *testing.B) {
	const packets, names, targets = 400, 124, 205
	rows := make([]unmatchedTransmissionRow, 0, packets)
	for i := 0; i < packets; i++ {
		rows = append(rows, unmatchedTransmissionRow{txID: int64(i + 1), rawHex: realTransportFloodPacket})
	}
	txIDs := make([]int64, 0, packets)
	for i := 0; i < packets; i++ {
		txIDs = append(txIDs, int64(i+1))
	}
	declared := make([]string, 0, names)
	for i := 0; i < names; i++ {
		declared = append(declared, fmt.Sprintf("r%04d", i))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v := newScopeVerifier(rows)
		for t := 0; t < targets; t++ {
			v.evidence(txIDs, declared)
		}
	}
}
```

Add `"fmt"` to the test file's imports.

- [ ] **Step 2: Run it and record the number**

Run: `cd cmd/server && go test -bench BenchmarkScopeVerifierAudit -benchtime=5x -run '^$' ./...`
Expected: one figure. Paste the real output into the commit message.

The budget: the audit is cached for 30s, so anything under ~1s per refresh is comfortable and under ~100ms is invisible. If the measured figure exceeds 1s, stop — either the memo is not working (check `hmacCount` in a debugger) or `scopeVerifyMaxPacketsPerTarget` needs lowering. Do not ship a number you have not looked at.

- [ ] **Step 3: Full suites**

Run: `cd cmd/server && go test ./...` then `cd ../ingestor && go test ./...` then, from the repo root, `node test-packet-filter.js && node test-aging.js && node test-frontend-helpers.js`
Expected: PASS everywhere. The ingestor is untouched by this plan; run it to prove that.

- [ ] **Step 4: Commit**

```bash
git add cmd/server/scope_verify_test.go
git commit -m "test(scope-audit): benchmark the verification pass at audit scale"
```

- [ ] **Step 5: Confirm against the real case after deploy**

Deferred, like M1's, and for the same reason: `test-fixtures/e2e-fixture.db` predates the feature and has neither `node_declared_regions` nor `scope_name`.

On staging or live, the row for `e3d3f4d7edd02aced3442b4ca77acb0824d9fcf1dc53cc42dca1ee0abe1cc0b1` must show **`fm-112` and `behss` green with dotted underlines**, and its `regionEvidence` must report counts in the same proportion measured by hand on 2026-09-07: 23 for `fm-112`, 3 for `behss` out of 36 unmatched rows in a 2000-packet sample. Exact numbers will differ — that sample was one node's packets, not a window — but both regions must clear the threshold of 2. If either stays grey, the feature has not worked whatever the unit tests say.

Then re-run the caveat measurement (`caveat-check.py`) and compare against the pre-deploy baseline of 119/205 rows carrying a finding. The expected direction is fewer findings and a sharply smaller unexplained-caveat count.

---

## Notes for the implementer

- **`notObserved` stays the single source of chip colour.** `regionEvidence` explains, it does not decide. Two fields that can disagree about the same fact is how this column got confusing in the first place.
- **Do not lower `scopeVerifyMinCorroboration` to 1.** One match in 65536 is not a rounding error at this scale: ~400 packets × ~124 names produces roughly one false positive per refresh, and a false green is worse than the grey this milestone replaces.
- **Do not widen `scopeAuditForwarderScanQuery` to carry `raw_hex`.** It returns one row per hop per flood packet — 19,049 on a 2,000-packet sample. The second narrow query exists precisely to avoid that.
- **Do not fold verification into the ingestor.** It would then write `scope_name`, and a wrong answer would persist until someone ran `scope-repair`. Read-time means a wrong answer expires with the window. That difference is the reason this is M1b and not part of M2.
- **Case matters.** `regionCode` must not fold case: the key is a hash over the raw bytes of `#name`.
