package main

import (
	"crypto/aes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

func uplinkCount(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// uplinkMsg is a /uplink payload as CoreDrive RX v1.21.0 sends it
// (src/publisher.js buildUplinkPayload), an hour in the past so
// resolveRxTimeCore keeps the timestamp.
func uplinkMsg() map[string]interface{} {
	return map[string]interface{}{
		"type": "UPLINK_PING", "timestamp": time.Now().UTC().Add(-time.Hour).Format(rxTimeMillisLayout),
		"tag": "78277c0e", "channel": "#coredrive", "scope": "#be", "pkt_hash": "b76c6a2777f9b0e9",
		"echoes": []interface{}{
			map[string]interface{}{"first_hop": "1403", "last_hop": "1403", "hops": float64(1), "snr": 12.25, "rssi": float64(-100)},
			map[string]interface{}{"first_hop": "c0cd", "last_hop": "c0cd", "hops": float64(1), "snr": -2.25, "rssi": float64(-120)},
			map[string]interface{}{"first_hop": "1403", "last_hop": "e470", "hops": float64(2), "snr": -8.25, "rssi": float64(-126)},
		},
		"gps": map[string]interface{}{"lat": 50.998, "lon": 5.384, "acc_m": 6.0},
	}
}

func TestHandleClientUplinkWritesRow(t *testing.T) {
	s := newTestStore(t)
	handleClientUplink(s, "test", testCompanionPK, uplinkMsg())
	var tag, hash, firstHops, echoes string
	var count int
	var lat float64
	err := s.db.QueryRow(`SELECT tag, pkt_hash, echo_count, first_hops, echoes_json, lat FROM client_uplink_pings`).Scan(&tag, &hash, &count, &firstHops, &echoes, &lat)
	if err != nil {
		t.Fatal(err)
	}
	if tag != "78277c0e" || hash != "b76c6a2777f9b0e9" || count != 3 || lat != 50.998 {
		t.Fatalf("row = %s %s %d %v", tag, hash, count, lat)
	}
	if firstHops != "1403,c0cd" {
		t.Errorf("first_hops = %q, want the distinct first hops 1403,c0cd", firstHops)
	}
}

func TestHandleClientUplinkNoEchoNoHash(t *testing.T) {
	s := newTestStore(t)
	m := uplinkMsg()
	delete(m, "pkt_hash")
	m["echoes"] = []interface{}{}
	handleClientUplink(s, "test", testCompanionPK, m)
	var hash *string
	var count int
	if err := s.db.QueryRow(`SELECT pkt_hash, echo_count FROM client_uplink_pings`).Scan(&hash, &count); err != nil {
		t.Fatal(err)
	}
	if hash != nil || count != 0 {
		t.Fatalf("pkt_hash = %v, echo_count = %d; want NULL and 0", hash, count)
	}
}

func TestHandleClientUplinkRejectsAndSanitises(t *testing.T) {
	s := newTestStore(t)
	bad := uplinkMsg()
	bad["tag"] = "not-a-tag"
	handleClientUplink(s, "test", testCompanionPK, bad)
	handleClientUplink(s, "test", "NOT-HEX!", uplinkMsg())
	if n := uplinkCount(t, s, "client_uplink_pings"); n != 0 {
		t.Fatalf("rows = %d, want 0", n)
	}
	m := uplinkMsg()
	m["pkt_hash"] = "xyz"
	m["echoes"] = []interface{}{map[string]interface{}{"first_hop": "zz", "last_hop": "1403"}, "junk",
		map[string]interface{}{"first_hop": "1403", "last_hop": "1403", "hops": float64(1)}}
	handleClientUplink(s, "test", testCompanionPK, m)
	var hash *string
	var count int
	if err := s.db.QueryRow(`SELECT pkt_hash, echo_count FROM client_uplink_pings`).Scan(&hash, &count); err != nil {
		t.Fatal(err)
	}
	if hash != nil || count != 1 {
		t.Fatalf("pkt_hash = %v, echo_count = %d; want a dropped bad hash and only the valid echo", hash, count)
	}
}

func TestHandleClientUplinkIdempotent(t *testing.T) {
	s := newTestStore(t)
	m := uplinkMsg()
	handleClientUplink(s, "test", testCompanionPK, m)
	handleClientUplink(s, "test", testCompanionPK, m)
	if n := uplinkCount(t, s, "client_uplink_pings"); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

// grpTxtPayload encrypts text the way the firmware does (Mesh.cpp:540-557,
// Utils.cpp:127-145) and returns it decoded as the ingestor would see it
// without the key.
func grpTxtPayload(t *testing.T, channel, text string) *DecodedPacket {
	t.Helper()
	ch := newUplinkChannel(channel)
	key, _ := hex.DecodeString(ch.keyHex)
	plain := append([]byte{1, 2, 3, 4, 0}, []byte(text)...)
	for len(plain)%16 != 0 {
		plain = append(plain, 0)
	}
	block, _ := aes.NewCipher(key)
	ct := make([]byte, len(plain))
	for i := 0; i < len(plain); i += 16 {
		block.Encrypt(ct[i:i+16], plain[i:i+16])
	}
	secret := make([]byte, 32)
	copy(secret, key)
	h := hmac.New(sha256.New, secret)
	h.Write(ct)
	mac := h.Sum(nil)[:2]
	buf := append([]byte{byte(ch.hashByte)}, mac...)
	buf = append(buf, ct...)
	pl := decodeGrpTxt(buf, nil)
	return &DecodedPacket{Payload: pl}
}

func TestNewUplinkChannelMatchesFirmware(t *testing.T) {
	// #coredrive hashes to channel byte 0x76, as seen on air (CoreScope live,
	// transmission b76c6a2777f9b0e9).
	if ch := newUplinkChannel("#coredrive"); ch.hashByte != 0x76 {
		t.Fatalf("hash byte = %#x, want 0x76", ch.hashByte)
	}
	if newUplinkChannel("  ") != nil {
		t.Fatal("empty name must give no channel")
	}
}

func TestNoteUplinkTransmissionLinksTagToHash(t *testing.T) {
	s := newTestStore(t)
	ch := newUplinkChannel("#coredrive")
	noteUplinkTransmission(s, ch, grpTxtPayload(t, "#coredrive", "BE-BRE-ON8AR | Erwin: cdrx 78277c0e"), "b76c6a2777f9b0e9")
	var hash, sender string
	if err := s.db.QueryRow(`SELECT pkt_hash, sender FROM client_uplink_tx WHERE tag = '78277c0e'`).Scan(&hash, &sender); err != nil {
		t.Fatal(err)
	}
	if hash != "b76c6a2777f9b0e9" || sender != "BE-BRE-ON8AR | Erwin" {
		t.Fatalf("hash %s sender %q", hash, sender)
	}
	// Other text on the channel, another channel's packet, or no channel set: nothing.
	noteUplinkTransmission(s, ch, grpTxtPayload(t, "#coredrive", "someone: hello"), "1111111111111111")
	noteUplinkTransmission(s, ch, grpTxtPayload(t, "#elsewhere", "x: cdrx aaaaaaaa"), "2222222222222222")
	noteUplinkTransmission(s, nil, grpTxtPayload(t, "#coredrive", "x: cdrx bbbbbbbb"), "3333333333333333")
	if n := uplinkCount(t, s, "client_uplink_tx"); n != 1 {
		t.Fatalf("client_uplink_tx rows = %d, want 1", n)
	}
}

// The live ingestor holds #coredrive among its own channel keys, so the decoder
// hands over a decrypted CHAN payload instead of GRP_TXT.
func TestNoteUplinkTransmissionAlreadyDecrypted(t *testing.T) {
	s := newTestStore(t)
	ch := newUplinkChannel("#coredrive")
	chanPayload := func(channel, text string) *DecodedPacket {
		return &DecodedPacket{Payload: Payload{Type: "CHAN", Channel: channel, ChannelHash: ch.hashByte,
			DecryptionStatus: "decrypted", Sender: "BE-ON8AR-Mobile", Text: text}}
	}
	noteUplinkTransmission(s, ch, chanPayload("#CoreDrive", "BE-ON8AR-Mobile: cdrx 71c131bd"), "19602811bbca85e5")
	// Same hash byte, another channel: not a ping, whatever the text says.
	noteUplinkTransmission(s, ch, chanPayload("#other", "x: cdrx aaaaaaaa"), "1111111111111111")
	var hash, sender string
	if err := s.db.QueryRow(`SELECT pkt_hash, sender FROM client_uplink_tx WHERE tag = '71c131bd'`).Scan(&hash, &sender); err != nil {
		t.Fatal(err)
	}
	if hash != "19602811bbca85e5" || sender != "BE-ON8AR-Mobile" {
		t.Fatalf("hash %s sender %q", hash, sender)
	}
	if n := uplinkCount(t, s, "client_uplink_tx"); n != 1 {
		t.Fatalf("client_uplink_tx rows = %d, want 1", n)
	}
	// Through the real decoder with the key loaded: also CHAN, also linked.
	key := ch.keyHex
	keyed := grpTxtPayload(t, "#coredrive", "BE-ON8AR-Mobile: cdrx 036ec636")
	raw, _ := hex.DecodeString(fmt.Sprintf("%02x", ch.hashByte) + keyed.Payload.MAC + keyed.Payload.EncryptedData)
	dec := &DecodedPacket{Payload: decodeGrpTxt(raw, map[string]string{"#coredrive": key})}
	if dec.Payload.Type != "CHAN" {
		t.Fatalf("decoder type %s, want CHAN", dec.Payload.Type)
	}
	noteUplinkTransmission(s, ch, dec, "2222333344445555")
	if n := uplinkCount(t, s, "client_uplink_tx"); n != 2 {
		t.Fatalf("client_uplink_tx rows = %d, want 2", n)
	}
}

func TestClientUplinkConfig(t *testing.T) {
	if (&Config{}).ClientUplinkEnabled() || (&Config{}).UplinkChannel() != nil {
		t.Fatal("absent config must be off")
	}
	c := &Config{ClientUplink: &ClientUplinkConfig{Enabled: true}}
	if ch := c.UplinkChannel(); ch == nil || ch.hashByte != 0x76 {
		t.Fatalf("default channel should be #coredrive, got %+v", ch)
	}
}

func TestClientUplinkGate(t *testing.T) {
	payload := []byte(`{"type":"UPLINK_PING","timestamp":"` + time.Now().UTC().Add(-time.Hour).Format(rxTimeMillisLayout) + `","tag":"0a1b2c3d","echoes":[]}`)
	msg := &mockMessage{topic: "meshcore/client/" + testCompanionPK + "/uplink", payload: payload}
	off := newTestStore(t)
	handleMessage(off, "test", MQTTSource{Name: "test"}, msg, nil, nil, &Config{})
	if n := uplinkCount(t, off, "client_uplink_pings"); n != 0 {
		t.Fatalf("feature off: rows = %d", n)
	}
	on := newTestStore(t)
	handleMessage(on, "test", MQTTSource{Name: "test"}, msg, nil, nil, &Config{ClientUplink: &ClientUplinkConfig{Enabled: true}})
	if n := uplinkCount(t, on, "client_uplink_pings"); n != 1 {
		t.Fatalf("feature on: rows = %d, want 1", n)
	}
}

func TestPruneOldClientUplink(t *testing.T) {
	s := newTestStore(t)
	handleClientUplink(s, "test", testCompanionPK, uplinkMsg())
	if _, err := s.db.Exec(`INSERT INTO client_uplink_tx (tag, pkt_hash, seen_at) VALUES ('aaaaaaaa','x','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	n, err := s.pruneOldClientUplinkAt(time.Now().UTC().Add(-24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || uplinkCount(t, s, "client_uplink_pings") != 1 || uplinkCount(t, s, "client_uplink_tx") != 0 {
		t.Fatalf("pruned %d; the recent ping must stay, the old tag link go", n)
	}
}
