package main

import (
	"log"
	"strings"
	"time"
)

// handleClientPacket processes a packet from the mobile client RX topic
// (meshcore/client/{PUBLIC_KEY}/packets). Unlike observer packets, a roaming
// companion reports WHERE it directly heard a node, so we write a
// client_receptions row and never touch the observers/observations tables.
// rxPubkey is the companion pubkey from the topic (ACL-bound by the broker).
func handleClientPacket(store *Store, tag, rxPubkey string, msg map[string]interface{}, channelKeys map[string]string) {
	rawHex, _ := msg["raw"].(string)
	if rawHex == "" {
		return
	}
	gps, ok := msg["gps"].(map[string]interface{})
	if !ok {
		return // a client packet without a GPS fix is not coverage; drop
	}
	lat, latOK := toFloat64(gps["lat"])
	lon, lonOK := toFloat64(gps["lon"])
	if !latOK || !lonOK {
		return
	}
	var accPtr *float64
	if acc, ok := toFloat64(gps["acc_m"]); ok {
		accPtr = &acc
	}

	decoded, err := DecodePacket(rawHex, channelKeys, false)
	if err != nil {
		log.Printf("MQTT [%s] client decode error: %v", tag, err)
		return
	}

	direction := ""
	if v, ok := msg["direction"].(string); ok {
		direction = v
	} else if v, ok := msg["Direction"].(string); ok {
		direction = v
	}

	var snrPtr *float64
	if f, ok := toFloat64(firstPresent(msg, "SNR", "snr")); ok {
		snrPtr = &f
	}
	var rssiPtr *int
	if f, ok := toFloat64(firstPresent(msg, "RSSI", "rssi")); ok {
		v := int(f)
		rssiPtr = &v
	}

	rxAt, _ := resolveRxTime(msg, tag)
	isAdvert := decoded.Header.PayloadTypeName == "ADVERT"

	rec, ok := buildClientReception(
		firstNonEmpty(rxPubkey, stringField(msg, "origin_id")),
		direction, decoded.Path.Hops, decoded.Payload.PubKey, isAdvert,
		snrPtr, rssiPtr, lat, lon, accPtr, rxAt, time.Now().UTC().Format(time.RFC3339),
	)
	if !ok {
		return
	}
	if _, err := store.InsertClientReception(rec); err != nil {
		log.Printf("MQTT [%s] client_reception insert: %v", tag, err)
	}
}

// firstPresent returns the first present value among the given keys.
func firstPresent(msg map[string]interface{}, keys ...string) interface{} {
	for _, k := range keys {
		if v, ok := msg[k]; ok {
			return v
		}
	}
	return nil
}

// stringField returns msg[key] as a string, or "" if absent/not a string.
func stringField(msg map[string]interface{}, key string) string {
	if v, ok := msg[key].(string); ok {
		return v
	}
	return ""
}

// ClientReception is one mobile RX coverage point: a companion (RxPubkey)
// directly heard a node (HeardKey) at a GPS position. Hex binning is done
// server-side from Lat/Lon at query time, so no cell id is stored here.
type ClientReception struct {
	RxPubkey    string
	HeardKey    string
	HeardKeyLen int
	RSSI        *int
	SNR         *float64
	Lat         float64
	Lon         float64
	PosAccM     *float64
	RxAt        string
	IngestedAt  string
	Src         string
}

// deriveHeardKey applies the RX capture HARD RULE: record only what the
// companion heard itself and directly.
//   - direction must be "rx".
//   - hops present → the directly-heard node is the LAST hop (path[len-1]);
//     1-byte (2 hex char) prefixes are collision-prone and rejected.
//   - hops empty + isAdvert → the 0-hop advertiser, by its full pubkey.
//   - otherwise → not attributable (ok=false).
// Returns (heardKey lowercased, keylenBytes, src, ok).
func deriveHeardKey(direction string, hops []string, advertPubkey string, isAdvert bool) (string, int, string, bool) {
	if !strings.EqualFold(direction, "rx") {
		return "", 0, "", false
	}
	if len(hops) > 0 {
		last := strings.ToLower(strings.TrimSpace(hops[len(hops)-1]))
		keylen := len(last) / 2
		if keylen < 2 { // exclude 1-byte (collision-prone), matching Reach
			return "", 0, "", false
		}
		return last, keylen, "rxlog", true
	}
	if isAdvert && advertPubkey != "" {
		pk := strings.ToLower(strings.TrimSpace(advertPubkey))
		return pk, len(pk) / 2, "advert", true
	}
	return "", 0, "", false
}

// buildClientReception validates inputs and assembles a ClientReception, or
// returns ok=false when the packet is not attributable / out of range.
func buildClientReception(
	rxPubkey, direction string, hops []string, advertPubkey string, isAdvert bool,
	snr *float64, rssi *int, lat, lon float64, posAccM *float64, rxAt, ingestedAt string,
) (*ClientReception, bool) {
	if rxPubkey == "" || rxAt == "" {
		return nil, false
	}
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return nil, false
	}
	heardKey, keylen, src, ok := deriveHeardKey(direction, hops, advertPubkey, isAdvert)
	if !ok {
		return nil, false
	}
	return &ClientReception{
		RxPubkey: strings.ToLower(rxPubkey), HeardKey: heardKey, HeardKeyLen: keylen,
		RSSI: rssi, SNR: snr, Lat: lat, Lon: lon, PosAccM: posAccM,
		RxAt: rxAt, IngestedAt: ingestedAt, Src: src,
	}, true
}

// InsertClientReception writes one coverage row. Idempotent via the
// UNIQUE(rx_pubkey, heard_key, rx_at) constraint; returns ins=false when the
// row already existed. All writes live in the ingestor (read/write invariant #1283).
func (s *Store) InsertClientReception(r *ClientReception) (bool, error) {
	res, err := s.db.Exec(`
		INSERT INTO client_receptions
			(rx_pubkey, heard_key, heard_keylen, rssi, snr, lat, lon, pos_acc_m, rx_at, ingested_at, src)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(rx_pubkey, heard_key, rx_at) DO NOTHING`,
		r.RxPubkey, r.HeardKey, r.HeardKeyLen, r.RSSI, r.SNR, r.Lat, r.Lon, r.PosAccM, r.RxAt, r.IngestedAt, r.Src)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
