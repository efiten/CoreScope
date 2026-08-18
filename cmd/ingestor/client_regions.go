package main

import (
	"log"
	"strings"
	"time"
)

// handleClientRegions processes one region-discovery answer from
// meshcore/client/{PUBLIC_KEY}/regions: a mobile companion asked a repeater
// (target) which regions it declares flood-allowed, and relays the answer.
// The topic pubkey (rxPubkey) is the reporting companion's identity
// (ACL-bound by the broker) and is never taken from the payload. `target` is
// the repeater that was asked; it arrives as payload data and is validated
// with the same hex rule as the topic segment before being trusted. An empty
// `regions` list is a valid, deliberate answer ("nothing flood-allowed") and
// is stored; a missing or malformed `regions` field is not an answer at all
// and the whole message is dropped rather than half-stored as an empty list.
// GPS is optional here, unlike client coverage: a declared-regions answer is
// meaningful without a position, so a missing/invalid fix stores NULL
// lat/lon instead of dropping the row.
func handleClientRegions(store *Store, cfg *Config, tag, rxPubkey string, msg map[string]interface{}) {
	rxPubkey = strings.ToLower(strings.TrimSpace(rxPubkey))
	if !clientPubkeyRe.MatchString(rxPubkey) {
		log.Printf("MQTT [%s] regions: invalid pubkey %.8q, dropping", tag, rxPubkey)
		return
	}
	target, _ := msg["target"].(string)
	target = strings.ToLower(strings.TrimSpace(target))
	if !clientPubkeyRe.MatchString(target) {
		log.Printf("MQTT [%s] regions: invalid target %.8q, dropping", tag, target)
		return
	}
	regionsRaw, ok := msg["regions"].([]interface{})
	if !ok {
		log.Printf("MQTT [%s] regions: missing/malformed regions field, dropping", tag)
		return
	}
	regions := make([]string, 0, len(regionsRaw))
	for _, r := range regionsRaw {
		s, ok := r.(string)
		if !ok {
			log.Printf("MQTT [%s] regions: non-string region entry, dropping", tag)
			return
		}
		regions = append(regions, s)
	}
	truncated, _ := msg["truncated"].(bool)

	rxTime, _ := resolveRxTimeCore(msg, tag)
	observedAt := rxTime.Format(rxTimeMillisLayout)

	var latPtr, lonPtr *float64
	if gps, ok := msg["gps"].(map[string]interface{}); ok {
		lat, latOK := toFloat64(gps["lat"])
		lon, lonOK := toFloat64(gps["lon"])
		if latOK && lonOK && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180 {
			latPtr, lonPtr = &lat, &lon
		}
	}

	o := &ClientDeclaredRegions{
		Target:     target,
		RxPubkey:   rxPubkey,
		ObservedAt: observedAt,
		IngestedAt: time.Now().UTC().Format(time.RFC3339),
		RegionsCSV: strings.Join(regions, ","),
		Truncated:  truncated,
		Lat:        latPtr,
		Lon:        lonPtr,
	}
	if _, err := store.InsertClientDeclaredRegions(o); err != nil {
		log.Printf("MQTT [%s] node_declared_regions insert: %v", tag, err)
	}
}
