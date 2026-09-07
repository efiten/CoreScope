# Handover — Scope Audit M0–M2

**Written:** 2026-09-07
**Branch:** `feat/auto-region-keys` (pushed to `origin`, 33 commits ahead of `master` @ `8b115332`)
**Draft PR:** https://github.com/efiten/CoreScope/pull/11 (to `efiten/CoreScope` master — deliberately NOT upstream yet)

> **If you are a fresh Claude session picking this up:** the code is complete and travels
> fine in git. What does not travel is why it is shaped this way, what is deliberately
> unfinished, and which measurements decide what happens next. That is what this document
> is for. Read it before touching anything; then read
> `docs/specs/2026-09-07-auto-region-keys-design.md` for the design, and the three plans in
> `docs/plans/2026-09-07-*.md` for task-level detail and tick state.

---

## The problem this fixes

Repeater `e3d3f4d7edd02aced3442b4ca77acb0824d9fcf1dc53cc42dca1ee0abe1cc0b1`
(BE-HSS-JessaZH.VIR) declares nine regions. Two of them, `behss` and `fm-112`, showed as
"not observed" in the Scope Audit — a page whose headline claim is "which repeaters declare
a region they are not actually forwarding".

That claim was false for those rows. Packet `0a065d41d51f1f77` decodes to `code1=9209`,
which is exactly the code `#fm-112` derives by HMAC over that packet's own payload. Over a
2000-packet sample touching that repeater, 36 rows held `scope_name = ''`; re-derived, 23
are `fm-112` and 3 are `behss`. The repeater was forwarding both.

**Two independent causes**, found in that order:

1. **A region with no configured key cannot be named.** `matchingRegions`
   (`cmd/ingestor/main.go`) HMACs the payload with each key in `hashRegions` and compares to
   `code1`. A region absent from that list is stored `scope_name = ''` — the
   "transport-scoped but unnameable" state — and every declared-vs-observed comparison reads
   that as absent.
2. **Forwarding was attributed to `path[last]` only.** On flood routes every forwarder
   appends its hash to the END of the path (`internal/packetpath/route.go:20`), so
   `path[last]` means "the transmission an uplinked observer heard directly", not "forwarded
   it". The last-hop rule is genuinely needed for DIRECT routes, but both queries already
   filtered `route_type IN (0,1)`, where that hazard cannot arise — so the restriction only
   discarded evidence.

Measured live on 2026-09-07: cause 2 left **133 of 205 repeaters with zero attributable
evidence**, of which 110 had relayed traffic in the window.

---

## What is built

| Milestone | What it does | Where |
|---|---|---|
| **M0** | Attribute flood forwarding to every path hop, not just the last | `cmd/server/scopes.go` |
| **M1** | Count and surface traffic this instance cannot name, as a caveat | `cmd/server/`, `public/` |
| **M1b** | Verify a repeater's declared regions against its own unnameable traffic | `cmd/server/scope_verify.go`, `public/` |
| **M2** | Derive region keys from `node_declared_regions` — **opt-in, default off** | `cmd/ingestor/` |
| M3 | Path-evidence tie-break | **not built**, gated on a measurement below |

Automated verification at handover: `cmd/server` full suite ok (119.9s), `cmd/ingestor` ok
apart from one pre-existing Windows failure (below), frontend 692/99/18, `gofmt -l` and
`go vet` clean.

---

## Decisions you should not quietly reverse

**M1b's threshold is 2 corroborating packets, and the arithmetic is the argument.** `code1`
is two bytes, so an unrelated region name matches a given packet with probability 1/65536.
Across ~400 unmatched packets and ~124 declared names, chance alone produces roughly one
false match per audit refresh. Two matches on the same region for the same repeater is
(1/65536)². Lowering it to 1 does not make the feature noisy — it makes it unsound. See
`scopeVerifyMinCorroboration` in `cmd/server/scope_verify.go`.

**M1b is read-time and writes nothing.** A wrong answer expires with the window instead of
sitting in `transmissions.scope_name` until someone runs `scope-repair`. That, plus the
read/write separation invariant in AGENTS.md, is why it is a server-side inference and not
part of M2.

**`notObserved` remains the single source of chip colour.** `regionEvidence` says only
*how* a region was established, and explains a region that got exactly one hit and
therefore stayed grey. Two fields that can disagree about the same fact is how this column
became confusing in the first place.

**M2 is default off** and an absent config block leaves behaviour byte-for-byte unchanged —
asserted by `TestAutoRegionKeysDefaultsOff` and `TestRefreshFromStoreIsNoOpWhenDisabled`,
not assumed.

**M2 was re-scoped after M1b landed.** It no longer fixes the audit; M1b does. What M2
fixes is the rest of the product still not seeing these regions: `/api/packets` shows an
empty scope on a packet that has one, `/api/scope-stats` omits whole regions from
`byRegion`, `nodes.default_scope` is never set for them, and the per-node observed list is
short. Real value, lower urgency. If you find yourself justifying M2 by the audit, re-read
the spec's "Which cause dominates".

**`scope-repair` had to be pulled onto the same key set.** Left on `loadRegionKeys` alone it
would re-derive every automatically-named row as unmatched and write `''` over the name —
a maintenance tool erasing exactly what M2 produces. Guarded by
`TestScopeRepairKeepsDerivedNames`.

---

## Traps found the hard way

**gofmt rewrites `''` inside doc comments.** It applies the old godoc typographic
substitution and turns a two-single-quote digraph into a closing curly quote. A comment
explaining that a query keys on an EMPTY `scope_name` rendered as a quotation mark, and came
back on every gofmt run. Written out in words in `cmd/server/scope_verify.go` with a note;
do not put the literal back.

**Caching the expensive operation is not the same as removing the expensive loop.** M1b's
plan claimed ~50ms because caching per `(region, transmission)` cuts HMACs from ~10M to
~50k. Measured: **501ms**. The HMACs had become a rounding error while the *iteration* was
still cubic — 10.2M map lookups at ~49ns. Re-keyed per region, holding the set of matching
transmissions: **36ms**, a 14× improvement. `hmacCount` guards the first mistake; only the
benchmark caught the second.

**M2 has no such loop to hoist.** One HMAC per key per packet is irreducible: `code1` is an
HMAC over the payload, so nothing is payload-independent to index on. The old `matchScope`
comment suggesting a "pre-indexed lookup table" was wrong and a note where it stood says so.
Benchmarked linear at ~0.65µs/key: at the 314-key ceiling, 217µs/packet — 0.0008% of one
core at this network's 0.037 transport-scoped packets/s.

---

## Outstanding — and where each piece has to happen

### On the build/publish laptop (server access)

1. **Deploy the branch to staging, then live.** Nothing here is running anywhere. The
   original complaint is still visible on analyzer.on8ar.eu exactly as it was.

2. **Browser validation** (AGENTS.md rule 2), never done for any milestone. It cannot be
   done from a dev checkout: `test-fixtures/e2e-fixture.db` predates this whole feature —
   no `node_declared_regions` table, no `scope_name` column. On staging or live, the
   `e3d3f4d7…` row must show **`fm-112` and `behss` green with dotted underlines**, and
   `regionEvidence` must report counts roughly in the hand-measured proportion (23 : 3 out
   of 36 in a 2000-packet sample — exact numbers will differ, both must clear 2).

3. **The config fix, which is independent of all of this and works today.**
   `hashRegions` is missing 123 region names that repeaters in this network declare. A
   ready-to-run script was produced this session but lives only in a scratchpad — regenerate
   it from `/api/scope-audit`'s `declaredRegions` if you need it. Procedure: back up
   `config.json`, merge (do not replace) with `jq '.hashRegions = ((.hashRegions // []) + $new | unique)'`,
   restart only `corescope-ingestor`, run `ingestor scope-repair` as a dry run, read the
   report, then `-apply` **with the ingestor stopped** — it does every UPDATE in one
   transaction and `busy_timeout` is 5s, so a live ingestor hits `SQLITE_BUSY`.
   Names are case-sensitive: the key is `SHA256("#name")[:16]`.

4. **Four measurements**, three of which are decisions rather than tick-boxes:
   - M2 default-off proof: startup log must read `autoRegionKeys disabled — only the N configured hashRegions key(s) are in force`, with no `[regions] derived` line.
   - M2 feature-on proof: the refresh log reports declared/kept/total, and `scope-repair --dry-run` lists the newly named regions.
   - **The ambiguity rate.** After a day with M2 on, read `[regions] scope matches: unique=… explicit-over-derived=… ambiguous=… none=…`. **That number decides whether M3 is built at all** — the spec estimates ~10 ambiguous packets a week after tier 2 absorbs the rest, which would not justify the machinery. Record it in the spec's M3 section, replacing the estimate.
   - **The double-caveat check.** M0 widened attribution from 222 distinct last-hops to ~964 distinct hop prefixes, so `ambiguousHops` — currently 0 on all 205 rows precisely because so few hops were considered — will start firing. If both that chip and M1's unexplained-traffic chip end up lit on most rows, neither tells the reader anything. Pre-deploy baseline: **119 of 205 rows carry any finding**, both caveats at 0. Fetch `/api/scope-audit?window=24h` and count rows with `ambiguousHops > 0`, `observedUnmatchedPackets > 0`, and both.

### Anywhere

5. **Code review.** 36 changed files across four milestones, reviewed by nobody. In a
   feature where two cost models and one test case turned out wrong, this is not a
   formality.

6. **`go test -race` on the ingestor.** Could not run on the Windows machine (needs cgo,
   no gcc). **CI will not cover it either**: `deploy.yml:134` runs `-race` on the server
   only, added for PR #1208's atomic.Pointer migration; line 143 tests the ingestor without
   it. M2 introduces an `atomic.Pointer` in the ingestor. Either add `-race` to that line or
   run it locally on Linux. `atomic.Pointer` is race-free by construction and no published
   snapshot is ever mutated — but that is an argument, not a measurement.

7. **Upstream PR**, once the above is done. PR #11 is a draft against `efiten/CoreScope`
   master on purpose; upstream is `Kpa-clawbot/CoreScope`, currently 19 commits ahead of
   this fork's master, which will need reconciling first.

---

## Known gaps, honestly stated

**M1b and M2 have never been tested together.** Each is covered on its own. With M2 enabled,
packets that were unmatched get named at ingest, so M1b's verifier has fewer candidates and
the chip goes green by name rather than by verification. That is coherent — the region lands
in `agg.scopes` and leaves `notObserved` by the normal route — but no test pins it. A gap in
coverage, not a known bug.

**One ingestor test fails on Windows and always did.**
`TestWriteStatsAtomic_SymlinkAtDestIsReplaced` — `os.Symlink` needs
`SeCreateSymbolicLinkPrivilege`. Proven unrelated: `git diff 8b115332..HEAD -- cmd/ingestor/`
was empty before M2 started. It passes on Linux, so CI is the place to confirm.
`TestMQTTStallWatchdog_DisconnectedEscalationThrottled_1749` is load-flaky — it failed in a
full suite run under load and passed in isolation.

**Regions in use but never declared over RF stay invisible.** Neither the config fix nor M2
can name those; both lean on the declared side. This is stated in the spec's Scope section
and is not a defect to go looking for.
