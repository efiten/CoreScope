package main

import (
	"crypto/sha256"
	"sort"
	"strings"
	"sync/atomic"
)

// declaredRegionStat is one region name as reported over RF, with the two
// facts the cap ranks on: how many distinct repeaters declare it, and how
// recently any of them last answered.
type declaredRegionStat struct {
	Name      string
	Declarers int
	LastSeen  string // ISO, greatest observed_at across declarers
}

// maxRegionNameLen bounds a derived region name. Firmware region names are
// short labels; anything longer is a malformed or hostile entry, and each
// accepted name costs an HMAC on every transport-scoped packet.
const maxRegionNameLen = 32

// regionNameAcceptable reports whether a declared name may become a derived
// region key.
//
// The rules are structural, never about the name's meaning. The declared set
// contains entries that look like junk ("null", "bierhuis", "sol3"), but a
// blocklist on string values is unmaintainable and the cost of one bad name is
// a single slot out of maxDerived plus a 1-in-65536 collision chance. What IS
// rejected is anything that could not have come from the firmware intact:
//
//   - a comma would split the name on the next regions_csv round-trip
//   - a '#' cannot appear (the firmware strips it), so its presence means the
//     value was mangled somewhere upstream
//   - non-ASCII or whitespace would make the key SHA256 over bytes nobody
//     intended, silently mismatching the sender
//   - a NUL is the block-cipher padding a stale client failed to trim
func regionNameAcceptable(name string) bool {
	if name == "" || len(name) > maxRegionNameLen {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c <= ' ' || c >= 0x7F || c == ',' || c == '#' {
			return false
		}
	}
	return true
}

// rankDeclaredRegions filters stats through regionNameAcceptable and returns at
// most max names, most-worth-keeping first: by declarer count descending, then
// by recency, then by name. The name tie-break is what makes the result
// deterministic — without it the derived tier would churn between refreshes on
// equally-ranked names and the add/drop logging would be noise.
func rankDeclaredRegions(stats []declaredRegionStat, max int) []string {
	kept := make([]declaredRegionStat, 0, len(stats))
	for _, s := range stats {
		if regionNameAcceptable(s.Name) {
			kept = append(kept, s)
		}
	}
	sort.Slice(kept, func(i, j int) bool {
		if kept[i].Declarers != kept[j].Declarers {
			return kept[i].Declarers > kept[j].Declarers
		}
		if kept[i].LastSeen != kept[j].LastSeen {
			return kept[i].LastSeen > kept[j].LastSeen
		}
		return kept[i].Name < kept[j].Name
	})
	if max > 0 && len(kept) > max {
		kept = kept[:max]
	}
	names := make([]string, 0, len(kept))
	for _, s := range kept {
		names = append(names, s.Name)
	}
	return names
}

// splitDeclaredRegionsCSV parses a regions_csv value into its entries. The
// ingestor writes this column with strings.Join(regions, ","), so this is its
// exact inverse. Mirrors splitRegionsCSV in cmd/server/scopes.go.
func splitDeclaredRegionsCSV(csv string) []string {
	out := []string{}
	if csv == "" {
		return out
	}
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// regionKeySnapshot is an immutable view of the region keys in force for one
// packet. `all` is the single map matching iterates - merging at build time
// rather than per packet keeps the hot path free of allocation. `explicit`
// carries membership only, and exists so the ambiguity tie-break can tell an
// operator-configured region from one derived off the air.
type regionKeySnapshot struct {
	all      map[string][]byte
	explicit map[string]bool
}

func (s *regionKeySnapshot) isExplicit(name string) bool { return s.explicit[name] }

// regionKeySet holds the live snapshot. Readers take one atomic load; a
// refresh builds the replacement off to the side and swaps the pointer, so the
// ingest hot path never blocks on a rebuild (AGENTS.md rule 0).
type regionKeySet struct {
	cur     atomic.Pointer[regionKeySnapshot]
	enabled bool
	max     int
}

// newRegionKeySet builds the explicit tier from hashRegions. The derived tier
// starts empty; refreshDerived fills it, and does nothing at all when
// autoRegionKeys is off.
func newRegionKeySet(cfg *Config) *regionKeySet {
	explicitKeys := loadRegionKeys(cfg)
	explicitNames := make(map[string]bool, len(explicitKeys))
	all := make(map[string][]byte, len(explicitKeys))
	for name, key := range explicitKeys {
		explicitNames[name] = true
		all[name] = key
	}
	s := &regionKeySet{
		enabled: cfg.AutoRegionKeysEnabled(),
		max:     cfg.AutoRegionKeysMaxDerived(),
	}
	s.cur.Store(&regionKeySnapshot{all: all, explicit: explicitNames})
	return s
}

func (s *regionKeySet) snapshot() *regionKeySnapshot { return s.cur.Load() }

// refreshDerived rebuilds the derived tier from names (already ranked and
// capped by the caller) and swaps in a new snapshot. It REPLACES the derived
// tier rather than merging into it, so a region that stops being declared
// leaves the key set and the cap keeps meaning something.
//
// A name that duplicates an explicit key is skipped, not re-added: the
// explicit tier must stay authoritative for the tie-break, and demoting a
// configured region because a repeater also declares it would invert the whole
// rule.
//
// Returns the names actually added, for the caller to log.
func (s *regionKeySet) refreshDerived(names []string) []string {
	if !s.enabled {
		return nil
	}
	old := s.cur.Load()
	all := make(map[string][]byte, len(old.explicit)+len(names))
	for name := range old.explicit {
		all[name] = old.all[name]
	}
	added := make([]string, 0, len(names))
	for _, raw := range names {
		if !regionNameAcceptable(raw) {
			continue
		}
		name := "#" + raw
		if old.explicit[name] {
			continue
		}
		if _, exists := all[name]; exists {
			continue
		}
		h := sha256.Sum256([]byte(name))
		all[name] = h[:16]
		added = append(added, name)
	}
	s.cur.Store(&regionKeySnapshot{all: all, explicit: old.explicit})
	return added
}

// scopeReason records how a scope match was decided, so the outcome is
// auditable in logs without a schema change. It is deliberately not stored:
// transmissions.scope_name keeps its existing three-state encoding.
type scopeReason string

const (
	scopeReasonNone                scopeReason = "none"                  // no key matched
	scopeReasonUnique              scopeReason = "unique"                // exactly one key matched
	scopeReasonExplicitOverDerived scopeReason = "explicit-over-derived" // several matched, one was operator config
	scopeReasonAmbiguous           scopeReason = "ambiguous"             // several matched, no principled winner
)

// scopeMatch is the result of naming one packet's region scope.
type scopeMatch struct {
	Name       string // empty when unresolved - the caller stores that as the unmatched state
	Reason     scopeReason
	Candidates []string // every matching name, populated only when more than one matched
}

// match names the region scope of a transport-scoped packet, resolving a
// multi-key collision by evidence rather than by map order.
//
// Tiers, in order:
//
//  1. Exactly one key matched - name it.
//  2. Several matched but exactly one came from hashRegions - name that one.
//     The operator's own configuration outranks a name picked up off the air,
//     and this covers the bulk of the ambiguity auto-derivation introduces.
//  3. Otherwise abstain, returning an empty name. Two equally-sourced
//     candidates offer no principled winner, and naming a packet wrongly is
//     worse than leaving it unnamed - the rule #1609 established, unchanged.
//
// (The spec's tier-3 path-evidence tie-break sits between 2 and 3 and is
// deliberately not built here; see
// docs/specs/2026-09-07-auto-region-keys-design.md. The scopeReasonAmbiguous
// counter is what measures whether it is worth building.)
func (s *regionKeySnapshot) match(payloadType byte, payloadRaw []byte, code1 string) scopeMatch {
	matched := matchingRegions(s.all, payloadType, payloadRaw, code1)
	switch len(matched) {
	case 0:
		return scopeMatch{Reason: scopeReasonNone}
	case 1:
		return scopeMatch{Name: matched[0], Reason: scopeReasonUnique}
	}

	var explicitMatches []string
	for _, name := range matched {
		if s.explicit[name] {
			explicitMatches = append(explicitMatches, name)
		}
	}
	if len(explicitMatches) == 1 {
		return scopeMatch{Name: explicitMatches[0], Reason: scopeReasonExplicitOverDerived, Candidates: matched}
	}
	return scopeMatch{Reason: scopeReasonAmbiguous, Candidates: matched}
}
