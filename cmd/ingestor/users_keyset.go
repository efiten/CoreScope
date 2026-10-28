package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// usersKeySet is a set of pubkeys read from users.db, read-only, with raw SQL
// (AGENTS.md: the ingestor never imports internal/users and never writes
// users.db). It backs the linked-only ingest filter (linked_companions.go)
// and the verified flag of neighbour reports (confirmed_observers.go).

const (
	// usersKeySetRefresh is the periodic re-read; it picks up removals.
	usersKeySetRefresh = time.Minute
	// usersKeySetMissGap caps the re-reads a miss may trigger.
	usersKeySetMissGap = 5 * time.Second
)

// usersKeySet hands each caller the current set of pubkeys. Snapshots are
// never mutated after they are stored, so a hit is lock-free. Reads (refresh,
// or a miss after the gap) are serialized by mu.
type usersKeySet struct {
	path  string
	query string // a query returning one pubkey column
	tag   string // log prefix
	// passWhenUnloaded makes Has answer true until the first successful read.
	passWhenUnloaded bool
	// label names the set in log lines; noTableMsg is logged once when the
	// queried table is missing.
	label      string
	noTableMsg string
	now        func() time.Time
	cur        atomic.Pointer[map[string]struct{}]

	mu       sync.Mutex  // guards everything below
	db       *sql.DB     // opened lazily, read-only
	lastRead time.Time   // last read attempt, for the miss gap
	reads    int         // read attempts (tests)
	failing  bool        // the last read failed; logged once per failure streak
	loaded   atomic.Bool // a read has succeeded at least once
	noTable  bool        // warned that the table is missing
}

// newUsersKeySet starts unloaded and empty; nothing is read until refresh or
// the first miss.
func newUsersKeySet(path, query, tag string, passWhenUnloaded bool) *usersKeySet {
	s := &usersKeySet{
		path:             path,
		query:            query,
		tag:              tag,
		passWhenUnloaded: passWhenUnloaded,
		label:            tag,
		now:              time.Now,
	}
	empty := map[string]struct{}{}
	s.cur.Store(&empty)
	return s
}

// Has reports whether pubkey (any case) is in the set. A miss re-reads
// users.db when the last read is at least usersKeySetMissGap old, so a key
// added a moment ago is seen within seconds while a flood of unknown pubkeys
// costs one read per gap. Before the first successful read the answer is
// passWhenUnloaded.
func (s *usersKeySet) Has(pubkey string) bool {
	pk := strings.ToLower(strings.TrimSpace(pubkey))
	if _, ok := (*s.cur.Load())[pk]; ok {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := (*s.cur.Load())[pk]; ok { // another miss re-read meanwhile
		return true
	}
	if s.now().Sub(s.lastRead) < usersKeySetMissGap {
		return !s.loaded.Load() && s.passWhenUnloaded
	}
	s.readLocked()
	if !s.loaded.Load() {
		return s.passWhenUnloaded
	}
	_, ok := (*s.cur.Load())[pk]
	return ok
}

// refresh re-reads the set: at startup and from refreshLoop.
func (s *usersKeySet) refresh() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readLocked()
}

func (s *usersKeySet) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// readLocked swaps in a fresh snapshot and marks the set loaded. A missing
// table (a server older than the feature) is warned once and leaves the set
// unloaded. Any other failure keeps the current snapshot (or the unloaded
// answer, before the first success) and is logged once per streak.
func (s *usersKeySet) readLocked() {
	s.lastRead = s.now()
	s.reads++
	keys, err := s.read()
	if err != nil && strings.Contains(err.Error(), "no such table") {
		if !s.noTable {
			log.Printf("[%s] %s %s", s.tag, s.path, s.noTableMsg)
			s.noTable = true
		}
		return
	}
	if err != nil {
		if !s.failing {
			log.Printf("[%s] %s unavailable (%v); keeping the %d in force", s.tag, s.label, err, len(*s.cur.Load()))
			s.failing = true
		}
		return
	}
	if s.failing {
		log.Printf("[%s] %s readable again from %s", s.tag, s.label, s.path)
		s.failing = false
	}
	next := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		next[strings.ToLower(strings.TrimSpace(k))] = struct{}{}
	}
	s.cur.Store(&next)
	s.loaded.Store(true)
}

func (s *usersKeySet) read() ([]string, error) {
	if s.db == nil {
		if _, err := os.Stat(s.path); err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("%w at %s", errUsersDBMissing, s.path)
			}
			return nil, err
		}
		db, err := sql.Open("sqlite3", usersDBReadOnlyDSN(s.path))
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(1)
		s.db = db
	}
	rows, err := s.db.Query(s.query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var pk string
		if err := rows.Scan(&pk); err != nil {
			return nil, err
		}
		out = append(out, pk)
	}
	return out, rows.Err()
}

// Close closes the read-only handle, if any.
func (s *usersKeySet) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		s.db.Close()
		s.db = nil
	}
}

// refreshLoop re-reads the set every usersKeySetRefresh for the life of the
// process, which picks up removals.
func (s *usersKeySet) refreshLoop() {
	t := time.NewTicker(usersKeySetRefresh)
	defer t.Stop()
	for range t.C {
		s.refresh()
	}
}
