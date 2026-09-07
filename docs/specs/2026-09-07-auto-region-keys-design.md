# Auto-Derived Region Keys & Scope-Audit Honesty — Design Spec

**Date:** 2026-09-07
**Status:** Approved (design); **amended 2026-09-07** — a second, independent cause
of `notObserved` was measured after approval (see *Second cause* below); it adds M0
and re-orders the milestones. Implementation not started.

---

## Problem

The Scope Audit reports "declared but not observed" for regions this instance is
structurally incapable of observing, presenting a configuration gap as a finding
about someone else's repeater.

There are **two independent reasons** an instance can be structurally incapable of
observing a region, and they were found in that order rather than together: it may
hold no key that can name the region, or it may discard the evidence before
nameability is ever consulted. Both are below; the second one gates the first.

### First cause — a region with no key cannot be named

A transport-scoped packet's region is identified by HMAC-ing the payload with
`SHA256("#name")[:16]` for every configured region and comparing the derived
2-byte code to the packet's `code1` (`matchingRegions`, `cmd/ingestor/main.go:1700`).
A region absent from `hashRegions` therefore cannot be named: the ingestor stores
`scope_name = ""` (the "transport-scoped but unnameable" state per
`scopeNameForDB`, `cmd/ingestor/db.go:2038`). `ScopeAuditForwarding` skips those
rows outright (`cmd/server/scopes.go:530`), so the region never reaches
`agg.scopes` and `handleScopeAudit` lists it under `notObserved`
(`cmd/server/routes.go:3611`).

### Evidence — first cause

Verified against the live instance (analyzer.on8ar.eu) on 2026-09-07.

Repeater `e3d3f4d7…c0b1` (BE-HSS-JessaZH.VIR) declares nine regions and shows
`behss` and `fm-112` as not observed. Packet `0a065d41d51f1f77` decodes to
`route_type=0` (TRANSPORT_FLOOD), `payload_type=5`, `code1=9209`, path `["E3D3"]`
— the repeater is the last hop, so it is attributable. Re-deriving the code for
each candidate region name over that packet's own payload:

| Region | Derived code1 | |
|---|---|---|
| `#fm-112` | `9209` | matches |
| `#behss` | `AAA8` | |
| `#be` | `9CC7` | |

The packet *is* `fm-112`, and is stored as `scope_name = ""`.

Across a 2000-packet sample touching that repeater, 36 rows hold `scope_name = ""`.
Re-derived: 23 are `fm-112`, 3 are `behss`, 5 are `be` (genuine #1609 ambiguity —
`be` is configured, but a second key also matched), and 5 belong to a region
outside the candidate set.

Network-wide over 7 days: repeaters declare **124 distinct region names** (123
after dropping the literal `null`, which is a client serialisation artefact —
note that the automatic path in this design does *not* filter on name values, so
it would keep it; see the candidate filter below); this instance can name **16**.
`unknownScope` is 1.72% of all transport-scoped traffic.
Of 613 `notObserved` entries across 205 repeaters, **260 (42%)** name a region
that never appeared under any name in the whole window. That 42% is an upper
bound — a configured but genuinely idle region counts toward it — but `behss` and
`fm-112` are hash-verified, not inferred.

It is **also not a lower bound**, which was not visible when this was written: an
entry counted there can be unattributable as well as unnameable. See *Which cause
dominates* below, where the same measurement is re-taken with that confound
separated out.

### Second cause — forwarding is attributed to the last path hop only

`scopeAuditForwarderScanQuery` credits a transmission to exactly one node, the
last hop of its path (`cmd/server/scopes.go:447`), as does the per-node
`scopeConformanceQuery` (`cmd/server/scopes.go:113`). On a flood-family route
every forwarder APPENDS its own hash to the END of the path
(`internal/packetpath/route.go:20`), so `path[last]` does not mean "forwarded
this packet" — it means "was the transmission an uplinked observer heard
directly". Every earlier hop forwarded the same packet and is thrown away.

The last-hop rule is genuinely required for DIRECT routes (2, 3), which consume
the next hop from the FRONT, leaving `path[last]` as the route's far end rather
than the transmitter. But both queries already restrict to `route_type IN (0, 1)`
via `scopeConformanceForwarderRouteTypesSQL`, where that hazard cannot arise — so
inside these two queries the restriction discards evidence and buys nothing.

Consequence: a repeater is invisible to the audit unless an uplinked observer sits
within direct RF range of it. Regions it forwards read `notObserved` no matter how
many keys this instance holds, so the first cause's fix cannot reach them.

### Evidence — second cause

Verified against the live instance on 2026-09-07, 24h window, via
`/api/scope-audit`, `/api/scope-stats`, `/api/nodes`, `/api/nodes/{pk}/scopes` and
`/api/packets`.

Found on repeater `cf7903ce…7e12` (BE-HHE-LAAK-EDG-01). `/api/nodes/{pk}/scopes`
returns `observed: []` with **the entire route mix zero** for 1h, 24h *and* 7d —
not just no named scope, but no attributed transmission of any kind — while the
same page's node header reports `transported_scopes:
["#be","#be-vli","#eu","#fm-112"]` and `relay_count_24h: 306`. Two panels, one
page, one database, opposite answers.

Its own traffic over 14 days (373 rows, `/api/packets?node=`):

| | |
|---|---|
| flood-family packets carrying `CF79` in the path | 155 |
| of those, `CF79` as `path[last]` | **0** |
| `CF79` at path position 0 | 103 |
| scope names on those packets | 126 × `#be`, 7 × `#fm-112`, 1 × `#eu`, 1 unmatched |
| rows where `CF79` *is* `path[last]` | 52 — all `route_type = 2` (DIRECT), correctly excluded |

It is an edge node: its strongest neighbour BE-ZOD-MOSKEE-DIS hears it 1397 times
against 1 the other way, so its relays are forwarded onward at least once before
any uplinked observer logs them. `transported_scopes` sees all of it because
`byPathHop` indexes *every* hop (`cmd/server/repeater_enrich_bulk.go:166`); the
two scope queries see none of it. The 52 DIRECT rows are the useful control — they
confirm the last-hop rule is still load-bearing for route types 2 and 3.

Not one node, the network:

- Flood-family sample, 1000 packets over 1.5h: mean path length **7.08** hops
  (counting only hops ≥ `minForwarderHopHexLen`), so the last-hop rule keeps
  **394 of 2789** hop observations — **14%**. **85%** of the nodes seen forwarding
  in that window never appear as a last hop at all.
- `/api/scope-audit`, 24h, 205 repeaters: **133 (65%)** have zero attributable
  evidence of any kind — no named scope, no undeclared scope, no unscoped packets
  — so every region they declare reads "declared, not observed". Of those 133,
  **110** have `relay_count_24h > 0` and **55** already carry a non-empty
  `transported_scopes`, attributed by full 4-hex key rather than by the
  collision-prone 1-byte prefix bucket. The evidence is in the same database.
- `ambiguousHops` is **0** on all 205 rows. The prefix-collision machinery
  `ScopeAuditForwarding` documents at length has never been fed enough hops to
  fire once.

### Which cause dominates

The same 642 `notObserved` entries (24h), split by whether the named region
appeared under that name anywhere in the window — `/api/scope-stats` named 17
distinct regions in it:

| | entries | |
|---|---|---|
| names a region this instance never named in the window | 287 | 45% — first cause, M2's target |
| names a region that *is* named in this window | 355 | 55% — nameability is not the blocker |
| sits on one of the 133 zero-evidence repeaters | 389 | 61% |
| …of those, naming a region that *is* nameable | 238 | 37% of all entries — second cause alone |

The two causes overlap and neither subsumes the other; the 42%/7d figure above
reproduces as 45%/24h here.

This is what orders the milestones, and the ordering is not about size. With the
last-hop rule in place M2 cannot be **measured**: deriving a key for `#behka`
would name the traffic in `transmissions.scope_name`, and the declaring repeater's
audit row would still say `notObserved`, because its hops were discarded before
nameability was consulted. For 65% of repeaters M2's effect on the audit would be
exactly zero, indistinguishable from M2 not working.

### Why collisions are benign

`code1` is an HMAC over the packet payload, so a collision between two region
names is re-rolled per packet rather than fixed per name pair. Two consequences
shape this design:

- A region is never systematically lost to a collision, only a random fraction of
  its packets.
- Targeted poisoning is not possible: an attacker cannot choose a name that
  reliably collides with `#be`, because they do not control the payloads.

The residual risk is therefore purely a rise in the random ambiguity rate,
proportional to the key-set size — which is what the cap below bounds. The rate
is `(N-1)/65536` per scoped packet: ~0.09% at 58 keys, ~0.27% at 180, and ~0.48%
at the 314 that `maxDerived: 256` permits on top of a 58-key explicit set. Only
the first of those is today's baseline; the others are what the cap buys.

---

## Scope

M0, M1, M1b and M2 below. M3 is explicitly gated on measurements taken during M2.
M4 is tracked, not built.

M0 was added by the 2026-09-07 amendment and comes first: it is a `cmd/server/`
change of two SQL predicates, and until it lands neither M1's counter nor M2's
effect can be observed on the audit at all.

M1b was added by a second 2026-09-07 amendment, and it changes what M2 is for.
M1 marks a declared region this instance cannot name with a row-level caveat, but
leaves its chip grey — and grey reads as "declared but not forwarding", a claim
the data does not support either. M1b resolves that question directly instead of
footnoting it. Once it lands, **M2 no longer fixes the audit**; it fixes the rest
of the product (the packets page, scope-stats, `default_scope`, the per-node
observed list), which is real but a different and less urgent kind of value than
this document originally claimed for it.

Out of scope: regions in use but never declared over RF. Neither the config fix
nor this design can name those — both lean on the declared side.

---

## Architecture

### 0. Forwarder attribution on flood routes

Drop the `je.key = json_array_length(o.path_json) - 1` join condition from both
`scopeConformanceQuery` (`cmd/server/scopes.go:113`) and
`scopeAuditForwarderScanQuery` (`cmd/server/scopes.go:447`). Everything else in
both queries stays exactly as it is: `route_type IN (0, 1)`, the
`LENGTH(je.value) >= minForwarderHopHexLen` floor, the explicit `json_valid` guard
against one malformed row erroring the whole query, and the prefix match against
the caller's pubkey.

"Forwarded" then means what `transported_scopes` has always meant — appeared as a
path hop on a flood-family transmission — and the node page stops contradicting
itself. De-duplication is unaffected: `ScopeConformance` counts each transmission
once via `EXISTS`, and `ScopeAuditForwarding` already de-dupes on
`<target>|<txID>`, which now additionally absorbs the same target appearing twice
in one path (a routing loop, or a prefix collision within a single path).

Three consequences to carry deliberately rather than discover later:

- **Evidence quality stops being uniform.** `path[last]` is corroborated by an
  observer's own RF reception; a middle hop is attested only by the path field of a
  packet somebody else forwarded onward. This is already the standard `byPathHop`
  applies for `transported_scopes` and `relay_count_24h`, so no new class of trust
  is introduced — but the UI should be able to say which kind of evidence a row
  rests on instead of blending them silently. Cheapest honest form: carry a
  `directHops` count beside the total per (target, scope) and render it as a
  qualifier on the existing row, not as a second table.
- **`ambiguousHops` will start firing.** It is zero everywhere today; at ~7× the
  hops it will resolve real collisions, which is exactly what that machinery is
  for, but the "possibly ambiguous" chip will appear on rows that currently look
  clean. That is more honest, not less — and it is also the measurement M3 was
  gated on, so M3's gate becomes answerable for the first time.
- **Per-hop collision exposure is unchanged** — same 4-hex floor, same prefix
  match; only the number of matched hops grows. `observations.resolved_path`
  carries full pubkeys for ~90% of hops on the flood rows that have it, so a later
  refinement can attribute those exactly and fall back to the prefix match for the
  rest. Deliberately out of scope for M0: a strictly better attribution layered on
  top of a correct one, not part of making it correct.

Cost: the SQL keeps its shape. `json_each` already expands the entire array, and
the removed predicate was a filter on that expansion rather than an index lookup.
The audit's Go attribution loop grows with hop count (mean 7.08 on this network)
against the in-memory prefix index, which is what `scopeAuditPrefixIndex` exists
for. `ScopeConformance`'s `EXISTS` can only get cheaper — it may short-circuit on
the first matching hop instead of computing an array length per observation.

### 1. `regionKeySet` — the key registry

New file `cmd/ingestor/region_keys.go`. `loadRegionKeys` currently returns a flat
`map[string][]byte` built once at `main.go:111` and threaded through six call
sites. It becomes a two-tier, refreshable type:

```go
type regionKeys struct {                 // immutable snapshot
    explicit map[string][]byte           // from hashRegions — always trusted
    derived  map[string][]byte           // from node_declared_regions
}

type regionKeySet struct {               // live, refreshable
    cur atomic.Pointer[regionKeys]
}
```

The ingest hot path reads via `set.snapshot()` — one atomic load, no lock. A
refresh builds the replacement map off to the side and swaps the pointer. This is
AGENTS.md rule 0: no expensive work under a lock in the ingest path.

**Refresh** runs at startup and on a ticker. The query is one
`ROW_NUMBER() OVER (PARTITION BY target ORDER BY observed_at DESC)` over
`node_declared_regions`, covered by `idx_ndr_target`. At 205 targets it is
negligible. The ingestor already has the per-target `CurrentDeclaredRegions`
(`client_reception.go:552`); this adds the bulk variant beside it, mirroring
`AllCurrentDeclaredRegions` in `cmd/server/scopes.go`.

**Configuration** — a new top-level block, default **off**:

```json
"autoRegionKeys": { "enabled": false, "maxDerived": 256, "refreshMinutes": 15 }
```

Opt-in matches the existing `clientRxObservations` / `clientRfSamples` /
`clientRegions` flags. Note the codebase-wide gotcha those flags document: config
loading is plain `json.Unmarshal` with no `DisallowUnknownFields`, so a
mis-nested key is silently ignored. `autoRegionKeys` is **top-level**, a sibling
of `hashRegions`, not nested inside it.

With `enabled: false` the derived tier stays empty and behaviour is byte-for-byte
what it is today.

**Candidate filter.** A declared name is rejected when it is empty or longer than
32 characters, contains a non-printable character, a comma (the `regions_csv`
delimiter), or a `#` (the firmware strips it, so its presence signals a malformed
entry). Names already in `explicit` are skipped rather than duplicated.

There is deliberately **no blocklist on name values**. The declared set contains
entries that look like junk (`null`, `bierhuis`, `sol3`), but a rule filtering on
string content is unmaintainable, and the cost of one is a single slot out of 256
plus a 1-in-65536 collision chance.

**Ranking when over `maxDerived`:** by number of distinct repeaters declaring the
name (descending), then most recent `observed_at`, then name. The long tail of
one-off names is dropped first; `#be` (declared by 127 repeaters) never is. Fully
deterministic, therefore testable.

Every refresh logs the per-tier totals, names added, and names dropped.

### 2. Matching with evidence

`matchScope` returns a bare string today, discarding the difference between "no
key matched" and "several matched". It becomes:

```go
type scopeMatch struct {
    Name       string      // "" = unresolved
    Reason     scopeReason // unique | explicitOverDerived | pathDeclared | ambiguous | none
    Candidates []string    // populated once more than one key matches
}
```

Resolution tiers:

1. Exactly one match → `unique`.
2. More than one, exactly one of them in `explicit` → that one,
   `explicitOverDerived`. Operator intent beats RF hearsay, and this covers the
   majority of the ambiguity this change itself introduces.
3. *(M3, gated)* Still tied, and exactly one candidate is declared by a node in
   the packet's path → that one, `pathDeclared`.
4. Otherwise → `""`, `ambiguous` — the current behaviour.

`transmissions.scope_name` keeps its existing three-state encoding. **No schema
migration and no new column:** `Reason` goes to logs and an in-process counter.
Naming a packet wrongly is worse than not naming it, and tier 4 preserves that.

### 3. `scope-repair` must use the same key set

`runScopeRepair` (`cmd/ingestor/scope_repair.go:274`) builds its keys with
`loadRegionKeys(cfg)` — explicit only. Left alone, a repair run would re-derive
every automatically-named row as unmatched and write `""` back over it. It must
build the same two-tier `regionKeySet`, including a derived-tier refresh, before
scanning. This is a data-loss bug if missed, not a detail.

### 3b. Declared-region verification (independent of 1–3)

M1's caveat says "some of this row's grey chips may be unnameable rather than
unforwarded". That is honest but weak: the question it declines to answer is
answerable, per chip, from data already in the database.

For a repeater R declaring region X, with unmatched packets it was observed
forwarding: derive `SHA256("#X")[:16]`, HMAC each of those packets' payloads with
it, and compare to the packet's stored `code1` — exactly the computation
`matchingRegions` performs, but with the candidate set narrowed to R's own
declarations rather than every key this instance holds.

Three properties make this a better instrument for *this* question than the
global derivation in section 1:

  - **Better signal-to-noise.** Section 1 tests ~180 hypotheses against every
    packet, of which at most one is related to it; that is why it needs a cap, a
    ranking and a tie-break to contain the noise it creates. Here the ~9
    hypotheses per repeater are each independently supported before the test runs:
    the repeater says it forwards these regions.
  - **Corroboration is available, and it is not available to section 1.** The
    ingest-time path names each packet in isolation: one packet, one decision, a
    1-in-65536 chance of a coincidental match. Verification looks at a set. If
    two or more of R's unmatched packets derive to X, the odds of coincidence are
    (1/65536)² or better. **A chip turns green on ≥2 corroborating packets;
    exactly one leaves it grey with its own tooltip**, because a single match is
    not evidence.
  - **Nothing is written.** This is a read-time inference in `cmd/server/`, so a
    wrong answer expires with the window rather than persisting in
    `transmissions.scope_name` until someone runs `scope-repair`. It also keeps
    the read/write invariant intact without argument.

**Query shape matters here.** `scopeAuditForwarderScanQuery` returns one row per
hop per flood packet — 19,049 rows in a 2,000-packet sample after M0 — so widening
it to carry `raw_hex` would load the hot scan for nothing. Verification takes a
**second, narrow query** over only the unmatched transmissions in the window
(~400 over 7 days), fetching `id` and `raw_hex`, decoding each once and reusing the
payload across every candidate name. The main scan is untouched.

What survives of M1's chip: after M1b the row-level caveat fires only for
unmatched traffic matching **none** of the repeater's declared names. That is
rarer and more interesting than what it reports today — a repeater forwarding a
region it does not declare *and* that this instance cannot name.

### 4. Scope-audit honesty (independent of 1–3)

Regions stay unnameable even with derivation enabled: above the cap, or never
declared anywhere. The audit must be able to say so.

`ScopeAuditForwarding` currently discards unmatched rows with a bare `continue`
(`cmd/server/scopes.go:530`). That becomes an `unmatchedPackets` counter on
`scopeAuditTargetAgg`, surfaced as a field on `ScopeAuditRow`, rendered as a
caveat chip in `public/scope-audit.js` in the same idiom as the existing
`possibly ambiguous` chip (`ambiguousCaveat`).

No config coupling and no ingestor change: the server does not read
`hashRegions`, and adding that coupling would mislabel every region on any
deployment where the two binaries do not share a `config.json`.

---

## Milestones

### M0 — Forwarder attribution (gates M1's counter and M2's measurement)

`cmd/server/` plus one frontend tooltip. The smallest change in this document and
the one with the largest effect on what the audit reports.

- remove the last-hop join condition from both queries (Architecture 0)
- update the doc comments that currently present the last-hop rule as deliberate:
  `RouteTypeMix` (`cmd/server/scopes.go:35`), `scopeConformanceQuery`,
  `scopeAuditForwarderScanQuery`. `RouteTypeMix`'s "direct/transportDirect are
  always zero by construction" stays **true** (the route filter is untouched), but
  the reason it gives is stated in terms of `path[last]` and must be restated in
  terms of the route filter
- same restatement for `routesHtml`'s tooltip (`public/node-scopes.js:132`), which
  repeats the `path[last]` reasoning to the reader
- Tests (`cmd/server/scopes_test.go`): a flood transmission whose path carries the
  target in the **middle** is now attributed; a DIRECT transmission whose
  `path[last]` **is** the target is still not attributed — the existing guarantee,
  and after M0 the only thing standing between the audit and misattribution, so it
  gets an explicit test rather than relying on the route filter being obvious; a
  2-hex hop is still ignored; a target appearing twice in one path counts once
- afterwards, **re-measure** the first-cause share and record the new number here.
  M2's sizing depends on what remains once attribution is fixed, not on the 45%
  measured through the last-hop rule

#### What widening attribution costs, measured on staging 2026-09-07

Reading every hop instead of `path[last]` multiplies the rows the scan returns.
On a live-shaped database (206 declared repeaters, 965k transmissions) the 7d
window returns **3,470,188 hop rows** from 1,368,761 observations carrying a
path, and `GET /api/scope-audit?window=7d` took **16.7s** cold, against 4.0s for
24h and 0.15s for 1h. SQLite accounts for 2.7s of that; the rest was the Go side
reading rows.

Three SQL-side reductions were measured on that database and all were rejected,
because each costs more than it saves:

| approach | rows returned | time in SQLite |
|---|---|---|
| the query as written | 3,470,188 | 2.7s |
| pre-filter on the declared targets' first 4 hex chars | 1,971,126 | 20.9s |
| `GROUP BY t.id, hop` | 965,025 | 38.0s |
| `SELECT DISTINCT t.id, path_json` | 1,229,966 | 17.7s |

The query plan is already index-driven (`idx_transmissions_first_seen`, then
`idx_observations_tx_ts`), so there is no missing index behind this: the rows are
inherent to the data. Note for anyone attempting a hop comparison in SQL:
**80% of stored hops are uppercase** (1,026,814 of 1,284,897 in a 24h window)
because `packetpath.DecodePathFromRawHex` writes them that way, while targets are
lowercase. A case-sensitive comparison silently drops most attributable hops.

What did work was taking the per-transmission columns out of the per-hop rows
(`942761c4`) and not recomputing the same window concurrently or every 30s
(`b7515cec`). Measured after both, warm process: **24h 2.79-2.89s across six
samples** (from 4.04s) and **7d 11.6s** (from 16.7s), with repeat requests inside
the TTL served in ~1ms.

### M1 — Scope-audit honesty

`cmd/server/` and `public/` only. Ships value on its own and reviews independently.
Sequenced after M0: the `unmatchedPackets` counter below counts unmatched rows
*among the rows attribution admits*, so before M0 it would read zero for the same
65% of repeaters and invite the same wrong conclusion in a new field.

- `scopeAuditTargetAgg.unmatchedPackets`, counted where the `continue` is today
- `ScopeAuditRow.ObservedUnmatchedPackets`, documented in `docs/api-spec.md`
- caveat chip in `scope-audit.js`, exposed through
  `window.__meshcoreScopeAuditInternals` so it can be asserted
- Tests: `cmd/server/scopes_test.go` (counter and field), `test-frontend-helpers.js`
  (chip renders only when non-zero)

### M1b — Declared-region verification

`cmd/server/` and `public/` only, like M1. No config, no schema change, nothing
written. See Architecture 3b.

- second query over the window's unmatched transmissions (`id`, `raw_hex`),
  separate from the main hop scan so that scan stays as it is
- per repeater, derive a key for each of its declared regions and test it against
  its own unmatched packets, decoding each packet once
- a declared region with **≥2** corroborating packets is observed; with exactly
  one it stays grey and its tooltip says why one match is not evidence
- the chip carries how it was established, so a reader can tell a region named
  from a configured key apart from one verified against the repeater's own
  declaration
- narrow M1's row caveat to unmatched traffic matching none of the declared names
- Tests: two corroborating packets turn a chip green; one does not; a packet
  matching no declared name still feeds the narrowed caveat; a repeater with no
  unmatched traffic is unaffected
- Perf: bound the work at (unmatched transmissions in window × declared names per
  repeater), deduplicated per transmission, and benchmark it — AGENTS.md rule 0.
  The 30s audit cache already absorbs the cost, but the bound is what stops a
  future window widening turning it into a hot path

### M2 — Auto-derived region keys

**Re-scoped by the M1b amendment.** This no longer fixes the audit — M1b does.
What is left is the rest of the product still not seeing these regions:
`/api/packets` shows an empty scope on a packet that has one, `/api/scope-stats`
omits whole regions from `byRegion`, `nodes.default_scope` is never set for them,
and the per-node observed list is short. Worth doing, lower urgency, and it should
be sized against what M1b leaves rather than against the original 42%.

- `region_keys.go`: `regionKeySet`, `regionKeys`, atomic snapshot, two tiers
- bulk declared-regions read in the ingestor
- refresh at startup plus ticker; cap, filter, ranking, logging
- `autoRegionKeys` config block, default off; `config.example.json` entry with the
  `_comment_autoRegionKeys` explainer the file's convention expects
- `matchScope` → `scopeMatch`, tiers 1/2/4; six call sites updated
- `scope-repair` builds the same key set
- `docs/api-spec.md` and `docs/client-regions.md` updated
- Tests: filter and ranking determinism, cap enforcement, tier 1/2/4 resolution,
  disabled-by-default equivalence with today's behaviour, `scope-repair` no longer
  unnames derived rows
- **Benchmark** for the N-HMAC path. AGENTS.md rule 0 requires proof for perf
  claims, and this grows the key set by up to `maxDerived` (256 by default).
  The instance's current explicit key count is not recorded here — it is
  operator config — so the benchmark must sweep key-set size rather than assert
  a single before/after. Note that the work is inherently
  O(keys) per transport-scoped packet and **cannot be indexed** — the code depends
  on the payload, so the "pre-indexed lookup table" suggested in `matchScope`'s
  comment is not achievable. Measured against current traffic (~0.04 transport
  packets/s) the cost is negligible, but it is linear, which is the reason the cap
  exists.

### M3 — Path-evidence tie-break (tier 3) — gated

At ~180 keys (today's explicit set plus the 123 names currently declared) the
ambiguous share is ~0.27% of scoped packets, roughly 60 packets a week on this
network, and tier 2 absorbs most of it because the majority of
collisions will be explicit-against-derived. What is left for tier 3 may be ten
packets a week, against the cost of a prefix index over every declared target plus
plumbing path evidence into the decode path.

**Build only if M2's `ambiguous` logging shows the volume justifies it.** The
design remains the tiered one; the last tier is built on measurement rather than
expectation.

#### What the first run on staging measured (2026-09-07)

The gate is **still open, and the first tally points at closing it**. After 15
minutes on staging:

```
[regions] scope matches: unique=1306 explicit-over-derived=0 ambiguous=0 none=0
```

Zero ambiguous in 1306 scoped packets. That is one ticker interval, not the day the
gate asks for, and it is not identically zero either: the same container logged nine
`ambiguous collision between [#lu #be]` lines in the seconds after an MQTT reconnect,
so the rate is low rather than absent. The full reading has to come from a container
left running, because each deploy replaces it and takes `docker logs` along.

What the same run did settle is the size of the derived tier on this network, and it
is not what the estimate above assumes:

```
[regions] derived-key refresh: 124 name(s) declared, 124 kept after filter+cap(256), 160 total key(s) in force
[regions] derived keys now active: [#null]
```

159 explicit keys plus **one** derived. The 123 other declared names had already been
merged into the live `hashRegions` by hand that morning (58 keys to 159), so the
derived tier had nothing left to add but the one name no operator would type.
`scope-repair --dry-run` with the feature on: 0 rows newly named, 2 corrected.

`#null` is a repeater that declares a region literally named `null`
(`95f8e61c…`). `regionNameAcceptable` accepts it deliberately — the rules are
structural, and its own comment names `null` as an example of a junk-looking name
that costs one slot out of `maxDerived` and one 1-in-65536 collision chance. The
measurement confirms the rule behaves as designed; it is not a filter gap.

The consequence for sizing: at ~160 keys rather than the ~180 assumed above, and with
tier 2 absorbing explicit-against-derived collisions, the ambiguous share this network
can produce is smaller than the estimate that gates M3. That makes the reading more
likely to close the gate than to open it, which is a reason to take it rather than
skip it.

If built: candidate X wins when at least one resolvable path hop declares X and no
resolvable hop declares a competing candidate. Hops shorter than
`minForwarderHopHexLen` (4 hex chars) are ignored, matching the server's floor; a
hop whose truncated prefix resolves to several targets contributes the union of
their declared sets, so an inconclusive union abstains rather than guesses.

### M4 — Customizer exposure

AGENTS.md rule 8: `maxDerived` and `refreshMinutes` belong in the customizer.
Tracked, not built here.

---

## Operational note

Independent of this work, the immediate remedy for the live instance is to merge
the declared region names into `hashRegions`, restart the ingestor, and run
`ingestor scope-repair` (dry run first). Note what that remedy does **not** do:
it names traffic, and the audit still attributes none of it to the 133 repeaters
it cannot see, so the `notObserved` list will shrink far less than the key count
suggests until M0 lands. `scope-repair` applies only
`"" → name` and `name → ""` where several keys now match; any other transition is
reported as `UNEXPECTED` and left unwritten. `-apply` requires stopping the
ingestor first: every UPDATE runs in one transaction and `busy_timeout` is 5s, so
a live ingestor would hit `SQLITE_BUSY`.
