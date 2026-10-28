package main

import (
	"log"
	"path/filepath"
)

// Linked-only ingest (docs/specs/2026-10-08-companion-linking-design.md,
// "Linked-only ingest"). With clientRxCoverage.requireLinkedCompanion and
// userManagement.enabled, every meshcore/client/<pubkey>/... message whose
// pubkey no user linked is dropped before any client handler runs. The set
// comes from users.db, read-only, with raw SQL (AGENTS.md: the ingestor never
// imports internal/users and never writes users.db). It is a filter for
// honest clients, not a security boundary: all RX clients share one broker
// account.

const (
	// linkedCompanionsRefresh is the periodic re-read; it picks up unlinks.
	linkedCompanionsRefresh = usersKeySetRefresh
	// linkedCompanionsMissGap caps the re-reads a miss may trigger.
	linkedCompanionsMissGap = usersKeySetMissGap
)

// linkedCompanionsQuery reads internal/users schema v6 (companion_links);
// TestLinkedCompanionsTableIsInUsersSchema checks the table still exists there.
const linkedCompanionsQuery = `SELECT pubkey FROM companion_links`

// linkedCompanionSet hands each client message the current set of linked
// pubkeys (users_keyset.go).
type linkedCompanionSet struct {
	*usersKeySet
}

// newLinkedCompanionSet starts unloaded: until the first successful read of
// companion_links, every pubkey passes. A users.db that is missing, unreadable
// or older than companion linking must not cost the data of linked companions.
func newLinkedCompanionSet(usersDBPath string) *linkedCompanionSet {
	s := newUsersKeySet(usersDBPath, linkedCompanionsQuery, "companions", true)
	s.label = "linked companions"
	s.noTableMsg = "has no companion_links table (a server older than companion linking); passing client data until it appears"
	return &linkedCompanionSet{s}
}

// Allow reports whether pubkey (the topic segment, any case) is linked. A
// miss re-reads users.db at most once per linkedCompanionsMissGap, so a
// companion linked a moment ago is accepted within seconds.
func (s *linkedCompanionSet) Allow(pubkey string) bool {
	return s.Has(pubkey)
}

// allowClientPubkey applies the linked-only filter to a client topic's
// pubkey segment. A drop is counted, never logged: a flood would otherwise
// fill the log.
func (s *Store) allowClientPubkey(pubkey string) bool {
	if s.linkedCompanions == nil || s.linkedCompanions.Allow(pubkey) {
		return true
	}
	s.Stats.ClientUnlinkedDropped.Add(1)
	return false
}

// newLinkedCompanionFilter returns the linked-only filter, already read
// once, when clientRxCoverage.requireLinkedCompanion is in effect, else nil.
// The setting without userManagement.enabled is ignored with a startup
// warning, as on the server.
func newLinkedCompanionFilter(cfg *Config) *linkedCompanionSet {
	if !cfg.RequireLinkedCompanionSet() {
		return nil
	}
	if !cfg.RequireLinkedCompanion() {
		log.Printf("[companions] WARNING: clientRxCoverage.requireLinkedCompanion is ignored: it needs userManagement.enabled")
		return nil
	}
	shown := cfg.UsersDBPath()
	if abs, err := filepath.Abs(shown); err == nil {
		shown = abs
	}
	log.Printf("[companions] only linked companions may publish client data; reading companion_links from %s", shown)
	s := newLinkedCompanionSet(cfg.UsersDBPath())
	s.refresh()
	return s
}
