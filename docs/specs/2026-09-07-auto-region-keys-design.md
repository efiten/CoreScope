# Auto-Derived Region Keys & Scope-Audit Honesty — Design Spec

**Date:** 2026-09-07
**Status:** Approved (design); implementation not started

---

## Problem

The Scope Audit reports "declared but not observed" for regions this instance is
structurally incapable of observing, presenting a configuration gap as a finding
about someone else's repeater.

A transport-scoped packet's region is identified by HMAC-ing the payload with
`SHA256("#name")[:16]` for every configured region and comparing the derived
2-byte code to the packet's `code1` (`matchingRegions`, `cmd/ingestor/main.go:1700`).
A region absent from `hashRegions` therefore cannot be named: the ingestor stores
`scope_name = ""` (the "transport-scoped but unnameable" state per
`scopeNameForDB`, `cmd/ingestor/db.go:2038`). `ScopeAuditForwarding` skips those
rows outright (`cmd/server/scopes.go:530`), so the region never reaches
`agg.scopes` and `handleScopeAudit` lists it under `notObserved`
(`cmd/server/routes.go:3611`).

### Evidence

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

M1 and M2 below. M3 is explicitly gated on measurements taken during M2. M4 is
tracked, not built.

Out of scope: regions in use but never declared over RF. Neither the config fix
nor this design can name those — both lean on the declared side.

---

## Architecture

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

### M1 — Scope-audit honesty

`cmd/server/` and `public/` only. Ships value on its own and reviews independently.

- `scopeAuditTargetAgg.unmatchedPackets`, counted where the `continue` is today
- `ScopeAuditRow.ObservedUnmatchedPackets`, documented in `docs/api-spec.md`
- caveat chip in `scope-audit.js`, exposed through
  `window.__meshcoreScopeAuditInternals` so it can be asserted
- Tests: `cmd/server/scopes_test.go` (counter and field), `test-frontend-helpers.js`
  (chip renders only when non-zero)

### M2 — Auto-derived region keys

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
`ingestor scope-repair` (dry run first). `scope-repair` applies only
`"" → name` and `name → ""` where several keys now match; any other transition is
reported as `UNEXPECTED` and left unwritten. `-apply` requires stopping the
ingestor first: every UPDATE runs in one transaction and `busy_timeout` is 5s, so
a live ingestor would hit `SQLITE_BUSY`.
