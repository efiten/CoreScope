package main

import (
	"sync"
	"time"
)

// Limits on the unauthenticated /neighbors report (#1865). Any MQTT publisher
// can send one, so the values it carries are bounded before they reach the
// nodes table.
const (
	// maxConfiguredScopeLen caps the normalised comma-separated scope list.
	// Real repeaters carry a handful of short region names.
	maxConfiguredScopeLen = 256
	// maxReportFuture is how far ahead of our clock a report may be stamped
	// and still be accepted. Allows ordinary clock skew, rejects a timestamp
	// chosen to win last-write-wins forever.
	maxReportFuture = 5 * time.Minute
)

// reportNow is swapped in tests.
var reportNow = time.Now

// reportTooFarInFuture reports whether a canonical RFC3339 timestamp (the
// output of normalizeReportTS) is more than maxReportFuture ahead of now.
// Empty or unparseable input is not "in the future".
func reportTooFarInFuture(canonical string) bool {
	if canonical == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, canonical)
	if err != nil {
		return false
	}
	return t.After(reportNow().Add(maxReportFuture))
}

const (
	// maxNeighborReportPayload caps the raw report stored in
	// neighbor_reports.payload. The firmware sends at most 10 kB (#1865); a
	// larger payload is processed but not journalled.
	maxNeighborReportPayload = 64 << 10
	// maxHeardSecsAgo clamps neighbors[].heard_secs_ago: older than the
	// measured_links retention default is not a current link anyway.
	maxHeardSecsAgo = 7 * 24 * 3600
	// maxNeighborStatusLen caps neighbors[].status; longer values are stored
	// as "unknown". Real values are short words such as "responded".
	maxNeighborStatusLen = 32
)

const (
	// reporterLogLimiterCap bounds the reporters remembered for log
	// throttling; reporterLogInterval is how long one is remembered.
	reporterLogLimiterCap = 1024
	reporterLogInterval   = time.Hour
)

// reporterLogLimiter allows one log line per reporter per
// reporterLogInterval, so a publisher repeating a bad report cannot fill the
// log. The zero value is ready to use.
type reporterLogLimiter struct {
	mu   sync.Mutex
	now  func() time.Time // nil: time.Now
	seen map[string]time.Time
}

// allow reports whether a line for reporter may be logged now. Entries older
// than the interval are evicted when the map is full; when it is still full,
// nothing is logged (the caller still counts the event).
func (l *reporterLogLimiter) allow(reporter string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.now != nil {
		now = l.now()
	}
	if l.seen == nil {
		l.seen = make(map[string]time.Time)
	}
	if at, ok := l.seen[reporter]; ok && now.Sub(at) < reporterLogInterval {
		return false
	}
	if _, ok := l.seen[reporter]; !ok && len(l.seen) >= reporterLogLimiterCap {
		for k, at := range l.seen {
			if now.Sub(at) >= reporterLogInterval {
				delete(l.seen, k)
			}
		}
		if len(l.seen) >= reporterLogLimiterCap {
			return false
		}
	}
	l.seen[reporter] = now
	return true
}
