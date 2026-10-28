package main

// Verified neighbour reports (#1865,
// docs/superpowers/specs/2026-10-28-measured-neighbour-links-design.md, Part 1).
// A report is verified when its topic pubkey has an observer binding whose
// owner's own login confirmed it and whose owner still exists. The set is read
// from users.db read-only (users_keyset.go); users.db stores the pubkeys
// uppercase, Has compares lowercase.

// confirmedObserversQuery reads internal/users schema v8 (observer_bindings
// with confirmed_at). A binding whose owner was deleted (user_id NULL) is not
// verified, matching the broker hook.
const confirmedObserversQuery = `SELECT pubkey FROM observer_bindings WHERE confirmed_at IS NOT NULL AND user_id IS NOT NULL`

// newConfirmedObserverFilter returns the confirmed-observer set read from
// cfg's users.db, or nil when no MQTT source has verifiesObservers set: no
// report can be verified then, so users.db is not read for it.
func newConfirmedObserverFilter(cfg *Config) *usersKeySet {
	for _, src := range cfg.MQTTSources {
		if src.VerifiesObservers {
			return newConfirmedObserverSet(cfg.UsersDBPath())
		}
	}
	return nil
}

// newConfirmedObserverSet returns the set of confirmed observers. Until the
// first successful read nothing is verified: a missing users.db, a server
// without user management or one older than observer bindings means every
// report is unverified.
func newConfirmedObserverSet(usersDBPath string) *usersKeySet {
	s := newUsersKeySet(usersDBPath, confirmedObserversQuery, "neighbors", false)
	s.label = "confirmed observers"
	s.noTableMsg = "has no observer_bindings table (a server older than observer logins); neighbour reports are unverified until it appears"
	return s
}
