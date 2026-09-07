package main

import (
	"sort"
	"strings"
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
