# Scope Audit — Unmatched-Traffic Caveat (M1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop the Scope Audit from presenting "this instance cannot name that region" as "this repeater is not forwarding that region", by counting the unmatched forwarded packets the endpoint currently discards and surfacing them as a caveat.

**Architecture:** `ScopeAuditForwarding` (`cmd/server/scopes.go`) drops rows whose `scope_name` is the empty string with a bare `continue`. That empty string is the ingestor's "transport-scoped, but no configured region key matched" state. Counting those per target, exposing the count on `ScopeAuditRow`, and rendering a caveat chip gives the reader the missing half of the story. Server-side and frontend only — no ingestor change, no schema change, no config coupling.

**Tech Stack:** Go 1.x (`cmd/server`, stdlib `testing`), vanilla JS frontend (`public/`), Node's `assert` via `test-frontend-helpers.js`.

**Spec:** `docs/specs/2026-09-07-auto-region-keys-design.md`, section "4. Scope-audit honesty".

**Depends on M0** (`### M0 — Forwarder attribution`, added to the spec in `b610d461`). Both queries currently credit a transmission to `path[last]` only, but on a flood route every forwarder appends its hash to the END of the path (`internal/packetpath/route.go:20`), so `path[last]` means "heard directly by an uplinked observer", not "forwarded it". Measured live: 133 of 205 repeaters have zero attributable evidence. The `unmatchedPackets` counter below counts unmatched rows **among the rows attribution admits**, so before M0 it reads zero for that same 65% — inviting exactly the wrong conclusion in a brand-new field. Do not start Tasks 1, 2 or 5 until M0 has landed.

**Status: code complete, verification partly deferred.** M0 landed as `d93b4463`, which unblocked the rest.

| Task | Commit | State |
|---|---|---|
| 1 — counter in `ScopeAuditForwarding` | `70e6bcd5` | done |
| 2 — `ObservedUnmatchedPackets` on the API row | `ca464b59` | done |
| 3 — caveat chip | `79f38ef1` | done (landed first; inert until the field existed) |
| 4 — `docs/api-spec.md` | `93a0c385` | done |
| 5 — verification | — | automated part done; steps 3–5 **deferred to deploy** |

Automated verification passed on all four suites: `cmd/server` full (ok, 94.8s), `test-frontend-helpers.js` (686), `test-packet-filter.js` (99), `test-aging.js` (18).

Steps 3, 4 and 5 of Task 5 cannot run in this working copy: `test-fixtures/e2e-fixture.db` predates the whole feature — it has no `node_declared_regions` table and no `scope_name` column — and the live instance runs the pre-M0 code. They run against staging or live after deploy, per the operator's decision. Do not mark this plan complete until they have.

---

## Why this is a real defect, not a nicety

Measured on the live instance 2026-09-07: of 613 `notObserved` entries across 205 repeaters, 260 (42%) name a region that never appeared under any name in the whole 7-day window. Two of them (`behss`, `fm-112`) are hash-verified as genuinely forwarded traffic that the instance simply cannot name. The page's headline claim — "which repeaters declare a region they are not actually forwarding" — is false for those rows.

---

## File Structure

| File | Responsibility | Change |
|---|---|---|
| `cmd/server/scopes.go` | `scopeAuditTargetAgg`, `ScopeAuditForwarding`, `ScopeAuditRow` | Modify |
| `cmd/server/routes.go` | `handleScopeAudit` row assembly | Modify |
| `cmd/server/scopes_test.go` | Store- and handler-level tests | Modify |
| `public/scope-audit.js` | `unmatchedCaveat`, wired into `rowHtml`, exported for tests | Modify |
| `public/scope-audit.css` | `.sa-chip-unmatched` | Modify |
| `test-frontend-helpers.js` | Caveat rendering assertions | Modify |
| `docs/api-spec.md` | `observedUnmatchedPackets` field + note | Modify |

---

### Task 1: Count unmatched forwarded packets per target

**Files:**
- Modify: `cmd/server/scopes.go` (`scopeAuditTargetAgg` ~line 388, `ScopeAuditForwarding` ~line 530)
- Test: `cmd/server/scopes_test.go`

- [x] **Step 1: Write the failing test**

Append to `cmd/server/scopes_test.go`, after `TestScopeAuditForwardingAmbiguousHopCreditsNeitherTarget`:

```go
// TestScopeAuditForwardingCountsUnmatchedPackets: a transport-scoped packet
// whose code1 matched no configured region key is stored with scope_name = ""
// (scopeNameForDB's "transport-scoped but unnameable" state). It is not a
// named scope, so it must not enter agg.scopes, and it is not an unscoped
// plain flood either, so it must not enter unscopedPackets. It is its own
// fact: this instance saw the target forward traffic it holds no key for.
// Without this counter the audit reports the declared region as "not
// observed", which reads as a finding about the repeater rather than about
// this instance's configuration.
func TestScopeAuditForwardingCountsUnmatchedPackets(t *testing.T) {
	s := newScopeTestStore(t)
	hop := testFullPubkeyA[:4]
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionRouteAt(t, s, hop, scopeUnmatched(), RouteFlood, recent)

	got, err := s.ScopeAuditForwarding("2026-01-01T00:00:00Z", []string{testFullPubkeyA})
	if err != nil {
		t.Fatal(err)
	}
	agg := got[testFullPubkeyA]
	if agg == nil {
		t.Fatalf("want an agg for the target, got none (result = %+v)", got)
	}
	if agg.unmatchedPackets != 1 {
		t.Errorf("unmatchedPackets = %d, want 1", agg.unmatchedPackets)
	}
	if len(agg.scopes) != 0 {
		t.Errorf("scopes = %+v, want empty — an unmatched packet names no region", agg.scopes)
	}
	if agg.unscopedPackets != 0 {
		t.Errorf("unscopedPackets = %d, want 0 — unmatched is not the same as unscoped", agg.unscopedPackets)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `cd cmd/server && go test ./... -run TestScopeAuditForwardingCountsUnmatchedPackets -v`
Expected: FAIL to compile — `agg.unmatchedPackets undefined (type *scopeAuditTargetAgg has no field or method unmatchedPackets)`

- [x] **Step 3: Add the field**

In `cmd/server/scopes.go`, in `scopeAuditTargetAgg`, add after `unscopedPackets`:

```go
	// unmatchedPackets counts packets this target was observed forwarding
	// that carried a transport scope no configured region key matched
	// (transmissions.scope_name = ""). It is deliberately NOT folded into
	// unscopedPackets: '*' governs plain unscoped floods, and an unmatched
	// packet is the opposite — it IS scoped, this instance just holds no key
	// for that region. A non-zero value means any notObserved entry on this
	// row may be unnameable rather than unforwarded.
	unmatchedPackets int64
```

- [x] **Step 4: Count it**

In `ScopeAuditForwarding`, replace the bare skip:

```go
			if scopeName.String == "" {
				continue // unmatched — not part of the declared/observed comparison
			}
```

with:

```go
			if scopeName.String == "" {
				// Unmatched: transport-scoped, but no configured region key
				// matched code1. Still not part of the declared/observed
				// comparison — it names no region — but it is the evidence
				// that a notObserved finding on this row may be a gap in this
				// instance's hashRegions rather than in the repeater.
				agg.unmatchedPackets++
				continue
			}
```

- [x] **Step 5: Run tests to verify they pass**

Run: `cd cmd/server && go test ./...`
Expected: PASS, including the pre-existing `TestScopeAuditForwardingAttributesUnambiguousHop` and `TestScopeAuditForwardingAmbiguousHopCreditsNeitherTarget`.

- [x] **Step 6: Commit**

```bash
git add cmd/server/scopes.go cmd/server/scopes_test.go
git commit -m "feat(scope-audit): count the unmatched packets ScopeAuditForwarding discards"
```

---

### Task 2: Surface the count on the API row

**Files:**
- Modify: `cmd/server/scopes.go` (`ScopeAuditRow`, after `AmbiguousHops`)
- Modify: `cmd/server/routes.go` (`handleScopeAudit`, the `unscopedPackets, ambiguousHops` block and the `ScopeAuditRow` literal)
- Test: `cmd/server/scopes_test.go`

- [x] **Step 1: Write the failing test**

Append to `cmd/server/scopes_test.go`, after `TestHandleScopeAuditSurfacesAmbiguousHops`:

```go
// TestHandleScopeAuditSurfacesUnmatchedPackets: a repeater declares "behss",
// and this instance sees it forward transport-scoped traffic it cannot name.
// The row must still list "behss" as notObserved — an unmatched packet names
// no region, so it cannot satisfy the declaration — but it must also carry
// observedUnmatchedPackets, so a client can say the finding might be a
// missing region key rather than a silent repeater.
func TestHandleScopeAuditSurfacesUnmatchedPackets(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	pk := testFullPubkeyA
	insertDeclared(t, srv, pk, time.Now().UTC().Format(time.RFC3339), "behss", 0)
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionRouteAt(t, srv.store, pk[:4], scopeUnmatched(), RouteFlood, recent)

	got := getScopeAudit(t, router, "")
	if len(got.Repeaters) != 1 {
		t.Fatalf("repeaters = %+v, want 1", got.Repeaters)
	}
	row := got.Repeaters[0]
	if row.ObservedUnmatchedPackets != 1 {
		t.Errorf("observedUnmatchedPackets = %d, want 1", row.ObservedUnmatchedPackets)
	}
	if len(row.NotObserved) != 1 || row.NotObserved[0] != "behss" {
		t.Errorf("notObserved = %v, want [\"behss\"] — an unmatched packet names no region and cannot satisfy a declaration", row.NotObserved)
	}
	if row.WildcardContradiction {
		t.Error("wildcardContradiction = true, want false — unmatched traffic is scoped, so it says nothing about '*'")
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `cd cmd/server && go test ./... -run TestHandleScopeAuditSurfacesUnmatchedPackets -v`
Expected: FAIL to compile — `row.ObservedUnmatchedPackets undefined`

- [x] **Step 3: Add the field to `ScopeAuditRow`**

In `cmd/server/scopes.go`, add after the `AmbiguousHops` field:

```go
	// ObservedUnmatchedPackets counts packets this repeater was observed
	// forwarding whose transport scope matched no region key this instance
	// holds. Like AmbiguousHops it is a caveat, not a finding: a non-zero
	// value means a NotObserved entry here may be a gap in this instance's
	// hashRegions rather than in the repeater's forwarding. It says nothing
	// about DeclaredWildcard — unmatched traffic IS scoped, so it is not
	// evidence for or against '*'.
	ObservedUnmatchedPackets int64 `json:"observedUnmatchedPackets"`
```

- [x] **Step 4: Populate it in the handler**

In `cmd/server/routes.go`, in `handleScopeAudit`, change:

```go
		var unscopedPackets, ambiguousHops int64
		if agg != nil {
			unscopedPackets = agg.unscopedPackets
			ambiguousHops = agg.ambiguousHops
		}
```

to:

```go
		var unscopedPackets, ambiguousHops, unmatchedPackets int64
		if agg != nil {
			unscopedPackets = agg.unscopedPackets
			ambiguousHops = agg.ambiguousHops
			unmatchedPackets = agg.unmatchedPackets
		}
```

and add to the `ScopeAuditRow` literal, after `AmbiguousHops: ambiguousHops,`:

```go
			ObservedUnmatchedPackets: unmatchedPackets,
```

- [x] **Step 5: Run tests to verify they pass**

Run: `cd cmd/server && go test ./...`
Expected: PASS

- [x] **Step 6: Commit**

```bash
git add cmd/server/scopes.go cmd/server/routes.go cmd/server/scopes_test.go
git commit -m "feat(scope-audit): expose observedUnmatchedPackets on the API row"
```

---

### Task 3: Render the caveat — DONE (`79f38ef1`)

Landed ahead of the rest: the chip is inert until the server sends the field, so it
could not break anything, and `public/scope-audit.js` was the one file M0 was not
holding. Steps kept below as the record of what was built; do not redo them.

**Files:**
- Modify: `public/scope-audit.js` (new `unmatchedCaveat`, called in `rowHtml`, added to `window.__meshcoreScopeAuditInternals`)
- Modify: `public/scope-audit.css`
- Test: `test-frontend-helpers.js`

- [x] **Step 1: Write the failing test**

Append to `test-frontend-helpers.js`, after the `mergedScopeChips` block:

```js
// ===== scope-audit.js: unmatchedCaveat =====
// A declared region this instance holds no hashRegions key for can never turn
// green, however much traffic the repeater forwards. On live data that
// explains up to 42% of all notObserved entries, so the column must be able
// to say so instead of presenting every grey chip as a confirmed gap.
console.log('\n=== scope-audit.js: unmatchedCaveat ===');
{
  const ctx = makeSandbox();
  ctx.registerPage = () => {};
  loadInCtx(ctx, 'public/app.js');
  loadInCtx(ctx, 'public/scope-audit.js');
  const caveat = ctx.__meshcoreScopeAuditInternals.unmatchedCaveat;

  test('zero unmatched packets renders nothing at all', () => {
    assert.strictEqual(caveat({ observedUnmatchedPackets: 0 }), '');
  });

  test('a missing field renders nothing (older server, field absent)', () => {
    assert.strictEqual(caveat({}), '');
  });

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
    // The operator fix is a hashRegions edit; a caveat that does not say so
    // sends them looking at the repeater instead of at their own config.
    const h = caveat({ observedUnmatchedPackets: 5 });
    assert.ok(h.includes('hashRegions'), 'must name the config key that fixes it');
  });
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `node test-frontend-helpers.js`
Expected: FAIL — `TypeError: caveat is not a function`

- [x] **Step 3: Implement `unmatchedCaveat`**

In `public/scope-audit.js`, add immediately after `ambiguousCaveat`:

```js
  // unmatchedCaveat flags rows where this instance saw the repeater forward
  // transport-scoped traffic it holds no region key for. Those packets can
  // never satisfy a declared region — the server has no name to match against
  // — so any "not observed" entry on such a row may be a gap in this
  // instance's hashRegions rather than in the repeater's forwarding. Distinct
  // from ambiguousCaveat: that one is a prefix collision between two nodes,
  // this one is a missing key on our side.
  function unmatchedCaveat(row) {
    var n = row.observedUnmatchedPackets;
    if (!n) return '';
    return ' <span class="sa-chip sa-chip-unmatched" title="' + n + ' forwarded packet' + (n === 1 ? '' : 's') +
      ' in this window carried a region scope this CoreScope instance holds no key for, so it could not be named. ' +
      'Any “not observed” entry on this row may be a missing entry in this instance’s hashRegions config rather than a repeater that is not forwarding.">' +
      n + ' forwarded packet' + (n === 1 ? '' : 's') + ' unnameable</span>';
  }
```

Then call it in `rowHtml`, in the Scopes `<td>`, immediately after `ambiguousCaveat(row)`:

```js
      '<td data-value="' + row.notObserved.length + '">' + mergedScopeChips(row) + (row.declaredWildcard ? ' <span class="sa-chip sa-chip-wildcard" title="Declares the \'*\' wildcard — allows plain unscoped floods.">*</span>' : '') + ambiguousCaveat(row) + unmatchedCaveat(row) + '</td>' +
```

And export it, extending the existing internals object:

```js
    window.__meshcoreScopeAuditInternals = { mergedScopeChips: mergedScopeChips, emptyStateHtml: emptyStateHtml, sourcesLineHtml: sourcesLineHtml, unmatchedCaveat: unmatchedCaveat };
```

- [x] **Step 4: Style the chip**

In `public/scope-audit.css`, add after the `.sa-chip-ambiguous` rule:

```css
.sa-chip-unmatched { background: var(--section-bg, var(--card-bg)); color: var(--text-muted); border: 1px dashed var(--border); font-family: inherit; font-style: italic; }
```

- [x] **Step 5: Run tests to verify they pass**

Run: `node test-frontend-helpers.js`
Expected: PASS, all assertions including the pre-existing `mergedScopeChips` block.

- [x] **Step 6: Commit**

```bash
git add public/scope-audit.js public/scope-audit.css test-frontend-helpers.js
git commit -m "feat(scope-audit): say when a not-observed region is one we cannot name"
```

---

### Task 4: Document the field

**Files:**
- Modify: `docs/api-spec.md` (payload block ~line 1890, Notes list ~line 1921)

- [x] **Step 1: Add the field to the payload block**

In the `GET /api/scope-audit` response block, after the `ambiguousHops` line, add a comma to that line and append:

```
      "ambiguousHops":            number,                // forwarder hops this window that could not be attributed — see note below
      "observedUnmatchedPackets": number                  // forwarded packets this window whose scope this instance holds no key for — see note below
```

- [x] **Step 2: Add the note**

In the same section's **Notes:** list, directly after the `ambiguousHops` bullet:

```
- `observedUnmatchedPackets` counts packets this repeater was observed forwarding whose
  transport scope matched no region key this instance has configured (`hashRegions`), so
  the ingestor stored them with an empty `scope_name`. Those packets name no region and
  therefore cannot satisfy a declared one, which means a repeater forwarding a region this
  instance cannot name appears in `notObserved` exactly like one forwarding nothing. A
  non-zero value is a caveat on this row's `notObserved`, in the same spirit as
  `ambiguousHops`, but with a different cause and a different fix: `ambiguousHops` is a
  prefix collision between two repeaters, `observedUnmatchedPackets` is a missing entry in
  this instance's own configuration. It is **not** evidence for or against
  `declaredWildcard` — unmatched traffic is scoped, so it never affects
  `wildcardContradiction`.
```

- [x] **Step 3: Verify no other doc contradicts it**

Run: `grep -rn "not part of the declared/observed comparison" docs/ cmd/`
Expected: only the updated comment in `cmd/server/scopes.go`; no stale doc claiming unmatched rows are discarded.

- [x] **Step 4: Commit**

```bash
git add docs/api-spec.md
git commit -m "docs(api): document observedUnmatchedPackets on GET /api/scope-audit"
```

---

### Task 5: Verify end to end

- [x] **Step 1: Full Go suite**

Run: `cd cmd/server && go test ./...` then `cd ../ingestor && go test ./...`
Expected: PASS in both. The ingestor is untouched by this plan; run it to prove that.

- [x] **Step 2: Full frontend suite**

Run: `node test-packet-filter.js && node test-aging.js && node test-frontend-helpers.js`
Expected: PASS

- [ ] **Step 3: Browser validation** (AGENTS.md rule 2)

Start the server against a database with declared-regions data, open `#/scope-audit`, and confirm a row with unmatched traffic shows the new chip next to its grey chips, with the title text readable on hover. Take a screenshot. If no local database has such a row, say so rather than claiming the check passed.

- [ ] **Step 4: Confirm the fix against the real case**

With the live database or the fixture, the row for `e3d3f4d7edd02aced3442b4ca77acb0824d9fcf1dc53cc42dca1ee0abe1cc0b1` must show a non-zero unnameable count alongside its `behss` and `fm-112` chips. That is the case this plan exists for; if it does not show, the plan has not worked regardless of what the unit tests say.

- [ ] **Step 5: Check the two caveats are not both permanently on**

M0 widens attribution from 222 distinct last-hops to every hop in every flood path (964 distinct prefixes on a 2000-packet sample), so `ambiguousHops` — currently 0 on all 205 rows precisely because so few hops were considered — will start firing. If both this chip and `possibly ambiguous` end up lit on most rows, neither tells the reader anything and the column is worse than before M0. Count how many rows carry each after M0 and report the numbers; if both exceed roughly half the rows, raise it rather than shipping two permanent caveats.

---

## Notes for the implementer

- **Do not** make the server read `hashRegions`. It deliberately does not, and adding that coupling would mislabel every region on a deployment where server and ingestor do not share a `config.json`. This plan needs no config.
- **Do not** fold `unmatchedPackets` into `unscopedPackets`. They are opposites: unscoped means the packet carried no scope at all (`scope_name` SQL NULL), unmatched means it carried one this instance cannot name (`scope_name` empty string). `scopeNameForDB` in `cmd/ingestor/db.go` is the source of truth for that encoding.
- The chip is a caveat, not an alarm. It reuses the muted, dashed-border treatment of `.sa-chip-ambiguous` on purpose — it must not compete visually with the red/green chips that carry the actual finding.
