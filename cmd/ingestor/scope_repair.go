package main

import (
	"database/sql"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
)

// scope-repair is a maintenance subcommand (#1609) that re-derives
// transmissions.scope_name for every transport-scoped row using the current
// matchScope. It corrects rows that were mis-derived by the old first-match
// logic, and names rows that were stored unmatched ("") because their region
// was added to hashRegions after they were ingested. It never runs as part
// of normal ingestor startup — it
// only executes when explicitly invoked as `ingestor scope-repair ...` (see
// the dispatch in main()), the same pattern used for --version.
//
// Usage:
//
//	ingestor scope-repair -config=config.json [-db=path/to.db] [-apply]
//
// Without -apply it is a dry run: it reports what it would change and
// writes nothing.

// scopeState mirrors the three states transmissions.scope_name can hold
// (see scopeNameForDB): Valid=false is SQL NULL ("not transport-scoped"),
// Valid=true with Name=="" is "transport-scoped but unnameable", and
// Valid=true with a non-empty Name is a matched region.
type scopeState struct {
	Valid bool
	Name  string
}

func (s scopeState) String() string {
	if !s.Valid {
		return "NULL"
	}
	if s.Name == "" {
		return `""`
	}
	return s.Name
}

// scopeDerivation is the re-derived state of one row plus enough detail to
// tell *why* it came out unnamed: MatchCount is the number of configured
// region keys whose derived code equals the packet's code1, meaningful only
// when State.Valid. MatchCount>1 is the #1609 ambiguity this tool repairs;
// MatchCount==0 means no configured key matches at all, which — for a row
// that was previously stored under a real region name — means the set of
// region keys has changed since ingest, not the historical bug. The two
// cases both produce State.Name=="" and must not be treated the same way.
type scopeDerivation struct {
	State      scopeState
	MatchCount int
}

// rederiveScope runs the same decode + match path handleMessage uses at
// ingest (BuildPacketData): DecodePacket for the header/transport codes and
// payload bytes, then matchingRegions against payloadType+payloadRaw+code1
// — the same helper matchScope itself uses. channelKeys is nil and
// validateSignatures is false because region matching depends on neither —
// only on the undecrypted payload bytes.
func rederiveScope(rawHex string, regionKeys map[string][]byte) (scopeDerivation, error) {
	decoded, err := DecodePacket(rawHex, nil, false)
	if err != nil {
		return scopeDerivation{}, err
	}
	if decoded.TransportCodes == nil || decoded.TransportCodes.Code1 == "0000" {
		return scopeDerivation{State: scopeState{Valid: false}}, nil
	}
	matched := matchingRegions(regionKeys, byte(decoded.Header.PayloadType), decoded.payloadRaw, decoded.TransportCodes.Code1)
	name := ""
	if len(matched) == 1 {
		name = matched[0]
	}
	return scopeDerivation{State: scopeState{Valid: true, Name: name}, MatchCount: len(matched)}, nil
}

// scopeRepairUnexpected is a row whose re-derived state differs from the
// stored one in a way the #1609 fix does not explain. It is reported but
// never applied.
type scopeRepairUnexpected struct {
	ID  int64
	Old scopeState
	New scopeState
}

// scopeRepairReport is the outcome of a repairScopeNames run, dry or applied.
type scopeRepairReport struct {
	Applied bool

	ScannedNotNull int
	DecodeFailed   int
	Unchanged      int
	CorrectedTotal int

	NamedBefore   int
	UnnamedBefore int

	// CorrectedByOldName counts, per previously-stored region name, how many
	// rows moved from that name to "" because the current config's region
	// keys ambiguously match the row (MatchCount>1). This is the only
	// transition repairScopeNames ever applies.
	CorrectedByOldName map[string]int

	// NamedTotal counts rows that moved from "" to a region name because
	// exactly one currently-configured key matches them, and
	// NamedByNewName breaks that down per newly-assigned name.
	NamedTotal     int
	NamedByNewName map[string]int

	Unexpected []scopeRepairUnexpected
}

// repairScopeNames re-derives scope_name for every row currently marked
// transport-scoped (scope_name IS NOT NULL) and, when apply is true,
// writes exactly two transitions:
//
//   - a stored non-empty region that re-derives to "" because two or more
//     configured keys now match it (an ambiguous multi-key match under the
//     pre-#1609 first-match bug);
//   - a stored "" that re-derives to exactly one region, which happens when
//     that region name was added to hashRegions after the row was ingested.
//
// Every other disagreement between stored and re-derived state is reported
// as unexpected and left untouched — it cannot be explained by either case,
// and guessing would repeat the original mistake. In particular a stored
// name that re-derives to zero matches (the region was REMOVED from the
// config) is reported, never rewritten.
//
// Rows whose raw_hex fails to decode are counted and skipped, never
// touched. Rows already correct (re-derived == stored) are left alone.
// Running repairScopeNames(apply=true) twice in a row writes nothing the
// second time: every row it just wrote now re-derives to the state it
// holds, which is the Unchanged case.
func repairScopeNames(db *sql.DB, regionKeys map[string][]byte, apply bool) (*scopeRepairReport, error) {
	rows, err := db.Query(`SELECT id, raw_hex, scope_name FROM transmissions WHERE scope_name IS NOT NULL ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("query transport-scoped rows: %w", err)
	}
	defer rows.Close()

	report := &scopeRepairReport{Applied: apply, CorrectedByOldName: map[string]int{}, NamedByNewName: map[string]int{}}
	type fix struct {
		id     int64
		newVal string
	}
	var fixes []fix

	for rows.Next() {
		var id int64
		var rawHex, storedName string
		if err := rows.Scan(&id, &rawHex, &storedName); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan row: %w", err)
		}
		report.ScannedNotNull++
		if storedName == "" {
			report.UnnamedBefore++
		} else {
			report.NamedBefore++
		}

		d, err := rederiveScope(rawHex, regionKeys)
		if err != nil {
			report.DecodeFailed++
			continue
		}
		oldState := scopeState{Valid: true, Name: storedName}

		switch {
		case oldState == d.State:
			report.Unchanged++
		case oldState.Name != "" && d.State.Valid && d.State.Name == "" && d.MatchCount > 1:
			report.CorrectedTotal++
			report.CorrectedByOldName[oldState.Name]++
			fixes = append(fixes, fix{id, ""})
		case oldState.Name == "" && d.State.Valid && d.State.Name != "":
			report.NamedTotal++
			report.NamedByNewName[d.State.Name]++
			fixes = append(fixes, fix{id, d.State.Name})
		default:
			report.Unexpected = append(report.Unexpected, scopeRepairUnexpected{ID: id, Old: oldState, New: d.State})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}

	if apply && len(fixes) > 0 {
		tx, err := db.Begin()
		if err != nil {
			return nil, fmt.Errorf("begin tx: %w", err)
		}
		stmt, err := tx.Prepare(`UPDATE transmissions SET scope_name = ? WHERE id = ?`)
		if err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("prepare update: %w", err)
		}
		for _, f := range fixes {
			if _, err := stmt.Exec(f.newVal, f.id); err != nil {
				stmt.Close()
				tx.Rollback()
				return nil, fmt.Errorf("update id=%d: %w", f.id, err)
			}
		}
		stmt.Close()
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit tx: %w", err)
		}
	}

	return report, nil
}

// writeScopeRepairReport renders a scopeRepairReport for the operator.
func writeScopeRepairReport(w io.Writer, r *scopeRepairReport) {
	mode := "DRY RUN (no writes)"
	if r.Applied {
		mode = "APPLIED"
	}
	fmt.Fprintf(w, "scope-repair: %s\n", mode)
	fmt.Fprintf(w, "  transport-scoped rows scanned (scope_name IS NOT NULL): %d\n", r.ScannedNotNull)
	fmt.Fprintf(w, "  raw_hex decode failures (skipped, untouched):           %d\n", r.DecodeFailed)
	fmt.Fprintf(w, "  already correct (unchanged):                           %d\n", r.Unchanged)
	fmt.Fprintf(w, "  corrected (ambiguous match -> unmatched \"\"):            %d\n", r.CorrectedTotal)
	fmt.Fprintf(w, "  newly named (unmatched \"\" -> region):                   %d\n", r.NamedTotal)
	fmt.Fprintf(w, "  named before: %d, unnamed (\"\") before: %d\n", r.NamedBefore, r.UnnamedBefore)
	namedAfter := r.NamedBefore - r.CorrectedTotal + r.NamedTotal
	unnamedAfter := r.UnnamedBefore + r.CorrectedTotal - r.NamedTotal
	if r.Applied {
		fmt.Fprintf(w, "  named after:  %d, unnamed (\"\") after:  %d\n", namedAfter, unnamedAfter)
	} else {
		fmt.Fprintf(w, "  named after (if applied):  %d, unnamed (\"\") after (if applied):  %d\n", namedAfter, unnamedAfter)
	}

	if len(r.CorrectedByOldName) > 0 {
		names := make([]string, 0, len(r.CorrectedByOldName))
		for name := range r.CorrectedByOldName {
			names = append(names, name)
		}
		sort.Strings(names)
		fmt.Fprintf(w, "  corrections by previously-stored region name:\n")
		for _, name := range names {
			fmt.Fprintf(w, "    %s -> \"\": %d row(s)\n", name, r.CorrectedByOldName[name])
		}
	}

	if len(r.NamedByNewName) > 0 {
		names := make([]string, 0, len(r.NamedByNewName))
		for name := range r.NamedByNewName {
			names = append(names, name)
		}
		sort.Strings(names)
		fmt.Fprintf(w, "  newly named by region:\n")
		for _, name := range names {
			fmt.Fprintf(w, "    %s: %d row(s)\n", name, r.NamedByNewName[name])
		}
	}

	if len(r.Unexpected) > 0 {
		fmt.Fprintf(w, "  UNEXPECTED transitions (not applied, needs manual review): %d\n", len(r.Unexpected))
		for _, u := range r.Unexpected {
			fmt.Fprintf(w, "    id=%d: %s -> %s\n", u.ID, u.Old, u.New)
		}
	}
}

// runScopeRepair is the `ingestor scope-repair` subcommand entry point.
func runScopeRepair(args []string) int {
	fs := flag.NewFlagSet("scope-repair", flag.ExitOnError)
	configPath := fs.String("config", "config.json", "path to config file")
	dbPathOverride := fs.String("db", "", "path to sqlite db (default: the config file's dbPath)")
	apply := fs.Bool("apply", false, "write corrections (default: dry run, report only)")
	fs.Parse(args)

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("scope-repair: config: %v", err)
	}

	dbPath := cfg.DBPath
	if *dbPathOverride != "" {
		dbPath = *dbPathOverride
	}
	regionKeys := loadRegionKeys(cfg)

	store, err := OpenStore(dbPath)
	if err != nil {
		log.Fatalf("scope-repair: db: %v", err)
	}
	defer store.Close()

	report, err := repairScopeNames(store.db, regionKeys, *apply)
	if err != nil {
		log.Fatalf("scope-repair: %v", err)
	}
	writeScopeRepairReport(os.Stdout, report)
	return 0
}
