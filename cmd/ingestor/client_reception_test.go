package main

import "testing"

func TestClientReceptionsTableExists(t *testing.T) {
	s := newTestStore(t)
	cols := map[string]bool{}
	rows, err := s.db.Query(`PRAGMA table_info(client_receptions)`)
	if err != nil {
		t.Fatalf("PRAGMA failed: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols[name] = true
	}
	for _, want := range []string{"id", "rx_pubkey", "heard_key", "heard_keylen", "rssi", "snr", "lat", "lon", "pos_acc_m", "h3", "rx_at", "ingested_at", "src"} {
		if !cols[want] {
			t.Errorf("missing column %q in client_receptions", want)
		}
	}
}
