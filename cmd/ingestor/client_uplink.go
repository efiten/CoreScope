package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"regexp"
	"strings"
	"time"
)

// Uplink pings (CoreDrive RX v1.21.0+, docs/client-rx-coverage.md): a mobile
// companion sends a channel message on a dedicated hashtag channel carrying
// only "cdrx <tag>", listens for repeaters re-broadcasting it, and reports the
// ping on meshcore/client/{PUBLIC_KEY}/uplink. Two facts come out of it:
//   - echo: the companion heard a repeater re-broadcast the ping, so a repeater
//     heard the companion (reported in the payload)
//   - arrival: an observer received the ping, found through its packet hash
//
// The hash comes from the payload when there was an echo. Without one, the
// ingestor learns it by decrypting the ping itself when an observer reports
// it (noteUplinkTransmission), keyed by the tag in the text.

var (
	uplinkTagRe     = regexp.MustCompile(`^[0-9a-f]{8}$`)
	uplinkHashRe    = regexp.MustCompile(`^[0-9a-f]{16}$`)
	uplinkHopRe     = regexp.MustCompile(`^[0-9a-f]{2,8}$`)
	uplinkTextTagRe = regexp.MustCompile(`(?:^|: )cdrx ([0-9a-f]{8})$`)
)

// maxUplinkEchoes bounds the stored echo list: one ping is re-broadcast by a
// handful of repeaters in a 10 s window, never by hundreds.
const maxUplinkEchoes = 32

// UplinkEcho is one re-broadcast of the ping the companion heard.
type UplinkEcho struct {
	FirstHop string   `json:"first_hop"`
	LastHop  string   `json:"last_hop"`
	Hops     int      `json:"hops"`
	SNR      *float64 `json:"snr,omitempty"`
	RSSI     *float64 `json:"rssi,omitempty"`
}

// ClientUplinkPing is one stored ping.
type ClientUplinkPing struct {
	RxPubkey   string
	Tag        string
	SentAt     string
	IngestedAt string
	Channel    string
	Scope      string
	PktHash    string // empty when no echo was heard
	Echoes     []UplinkEcho
	Lat, Lon   *float64
	PosAccM    *float64
}

// firstHops returns the distinct first hops of the echoes, in order heard.
func (p *ClientUplinkPing) firstHops() string {
	seen := map[string]bool{}
	var out []string
	for _, e := range p.Echoes {
		if !seen[e.FirstHop] {
			seen[e.FirstHop] = true
			out = append(out, e.FirstHop)
		}
	}
	return strings.Join(out, ",")
}

func handleClientUplink(store *Store, tag, rxPubkey string, msg map[string]interface{}) {
	rxPubkey = strings.ToLower(strings.TrimSpace(rxPubkey))
	if !clientPubkeyRe.MatchString(rxPubkey) {
		log.Printf("MQTT [%s] uplink: invalid pubkey %.8q, dropping", tag, rxPubkey)
		return
	}
	pingTag, _ := msg["tag"].(string)
	pingTag = strings.ToLower(strings.TrimSpace(pingTag))
	if !uplinkTagRe.MatchString(pingTag) {
		log.Printf("MQTT [%s] uplink: invalid tag %.12q, dropping", tag, pingTag)
		return
	}
	p := &ClientUplinkPing{RxPubkey: rxPubkey, Tag: pingTag, IngestedAt: time.Now().UTC().Format(time.RFC3339)}
	rxTime, _ := resolveRxTimeCore(msg, tag)
	p.SentAt = rxTime.Format(rxTimeMillisLayout)
	p.Channel = clampString(msg["channel"], 32)
	p.Scope = clampString(msg["scope"], 32)
	if h, _ := msg["pkt_hash"].(string); uplinkHashRe.MatchString(strings.ToLower(h)) {
		p.PktHash = strings.ToLower(h)
	}
	if raw, ok := msg["echoes"].([]interface{}); ok {
		for _, r := range raw {
			if len(p.Echoes) >= maxUplinkEchoes {
				break
			}
			m, ok := r.(map[string]interface{})
			if !ok {
				continue
			}
			first, _ := m["first_hop"].(string)
			last, _ := m["last_hop"].(string)
			first, last = strings.ToLower(first), strings.ToLower(last)
			if !uplinkHopRe.MatchString(first) || !uplinkHopRe.MatchString(last) {
				continue
			}
			e := UplinkEcho{FirstHop: first, LastHop: last}
			if h, ok := toFloat64(m["hops"]); ok && h >= 1 && h <= 64 {
				e.Hops = int(h)
			}
			if v, ok := toFloat64(m["snr"]); ok {
				e.SNR = &v
			}
			if v, ok := toFloat64(m["rssi"]); ok {
				e.RSSI = &v
			}
			p.Echoes = append(p.Echoes, e)
		}
	}
	if gps, ok := msg["gps"].(map[string]interface{}); ok {
		lat, latOK := toFloat64(gps["lat"])
		lon, lonOK := toFloat64(gps["lon"])
		if latOK && lonOK && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180 {
			p.Lat, p.Lon = &lat, &lon
			if acc, ok := toFloat64(gps["acc_m"]); ok {
				p.PosAccM = &acc
			}
		}
	}
	if _, err := store.InsertClientUplinkPing(p); err != nil {
		log.Printf("MQTT [%s] client_uplink_pings insert: %v", tag, err)
	}
}

func clampString(v interface{}, max int) string {
	s, _ := v.(string)
	s = strings.TrimSpace(s)
	if len(s) > max {
		s = s[:max]
	}
	return s
}

// InsertClientUplinkPing stores one ping. Idempotent on (rx_pubkey, tag): a
// redelivered message is stored once.
func (s *Store) InsertClientUplinkPing(p *ClientUplinkPing) (bool, error) {
	echoes, err := json.Marshal(p.Echoes)
	if err != nil {
		return false, err
	}
	if p.Echoes == nil {
		echoes = []byte("[]")
	}
	var hash interface{}
	if p.PktHash != "" {
		hash = p.PktHash
	}
	res, err := s.db.Exec(`
		INSERT INTO client_uplink_pings
			(rx_pubkey, tag, sent_at, ingested_at, channel, scope, pkt_hash, echo_count, first_hops, echoes_json, lat, lon, pos_acc_m)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(rx_pubkey, tag) DO NOTHING`,
		p.RxPubkey, p.Tag, p.SentAt, p.IngestedAt, p.Channel, p.Scope, hash, len(p.Echoes), p.firstHops(), string(echoes), p.Lat, p.Lon, p.PosAccM)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// uplinkChannel is the ping channel the ingestor decrypts to learn tag → hash.
type uplinkChannel struct {
	keyHex   string
	hashByte int
}

// newUplinkChannel derives the hashtag channel's key and hash byte the way the
// firmware does: key = SHA-256("#name")[:16], hash = SHA-256(key)[0]
// (helpers/BaseChatMesh.cpp:908). nil for an empty name.
func newUplinkChannel(name string) *uplinkChannel {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	k := sha256.Sum256([]byte(name))
	key := k[:16]
	h := sha256.Sum256(key)
	return &uplinkChannel{keyHex: hex.EncodeToString(key), hashByte: int(h[0])}
}

// noteUplinkTransmission links a ping's tag to the packet hash an observer
// reported. Only GRP_TXT the ingestor could not decrypt with its own channel
// keys reaches here, on the ping channel's hash byte; that byte is shared with
// unrelated channels, so the MAC check in decryptChannelMessage is what tells
// a ping from them.
func noteUplinkTransmission(store *Store, ch *uplinkChannel, decoded *DecodedPacket, pktHash string) {
	if ch == nil || decoded == nil || pktHash == "" {
		return
	}
	pl := decoded.Payload
	if pl.Type != "GRP_TXT" || pl.ChannelHash != ch.hashByte || pl.EncryptedData == "" || pl.MAC == "" {
		return
	}
	res, err := decryptChannelMessage(pl.EncryptedData, pl.MAC, ch.keyHex)
	if err != nil {
		return
	}
	text := res.Message
	if res.Sender != "" {
		text = res.Sender + ": " + res.Message
	}
	m := uplinkTextTagRe.FindStringSubmatch(text)
	if m == nil {
		return
	}
	if _, err := store.db.Exec(`
		INSERT INTO client_uplink_tx (tag, pkt_hash, sender, seen_at) VALUES (?,?,?,?)
		ON CONFLICT(tag) DO NOTHING`, m[1], pktHash, res.Sender, time.Now().UTC().Format(time.RFC3339)); err != nil {
		log.Printf("[uplink] client_uplink_tx insert: %v", err)
	}
}
