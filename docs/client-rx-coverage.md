# Client RX Coverage

Crowdsourced RF coverage from mobile clients: a phone connects over BLE to a MeshCore
*companion* radio, captures which nodes the companion hears (with SNR/RSSI), tags each reception
with the phone's GPS position, and publishes it to MQTT. CoreScope ingests these into
`client_receptions` and renders per-node H3-style hex coverage on the Reach page.

This is the contract the mobile capture app (separate repo, "Plan 2") implements.

## MQTT topic & payload

Topic: `meshcore/client/{PUBLIC_KEY}/packets` — `{PUBLIC_KEY}` is the companion's pubkey. The
broker (EMQX) should ACL-restrict each client to publish only under its own pubkey, which is how
"a connected companion may only inject under the keys that apply" is enforced.

Payload — meshcoretomqtt-compatible packet, plus a `gps` object:

```json
{
  "origin": "<companion name>",
  "origin_id": "<companion pubkey hex>",
  "timestamp": "2026-06-09T12:00:00Z",
  "type": "PACKET",
  "direction": "rx",
  "raw": "<packet hex>",
  "SNR": -7,
  "RSSI": -92,
  "gps": { "lat": 51.05, "lon": 3.72, "acc_m": 8 }
}
```

- The discriminator is the `gps` object. A packet without `gps` is dropped (coverage needs a position).
- `raw` is decoded server-side to derive the directly-heard node and the path; `hash`/`path` fields
  are not required.
- Subscription: the ingestor's default subscription (`meshcore/#`) already covers this topic. Sources
  configured with an explicit topic list must add `meshcore/client/+/packets`.

## Capture HARD RULE — only what was heard directly

The app and ingestor record **only the node the companion physically received**, never upstream
relayers:

- Packet **with a path** (≥1 hop) → record `path[len-1]` (the last forwarder = the immediate RF
  transmitter). Confirmed against firmware `Mesh.cpp` (a forwarder appends its own hash to the end
  of the path) and CoreScope's `neighbor_builder.go:226-228`.
- Packet **with no path** (0 hops) **and** an advert → record the advertiser's full pubkey.
- `direction` must be `rx`. 1-byte (2 hex char) prefixes are excluded (collision-prone, like Reach).
- The RSSI/SNR belong to the directly-received transmission, so they attach to the recorded node.
- The rest of the path is discarded for coverage.

## Storage — `client_receptions` (ingestor-owned)

A roaming companion is a mobile observer with a moving position, so it gets its own table (not
`observations`, which assumes a fixed observer location). Per the #1283 read/write invariant, the
table and all writes live in `cmd/ingestor/`.

```
client_receptions(
  id, rx_pubkey, heard_key, heard_keylen, rssi, snr,
  lat, lon, pos_acc_m, rx_at, ingested_at, src,
  UNIQUE(rx_pubkey, heard_key, rx_at))   -- idempotent re-ingest
```

`heard_keylen` is 32 for a full pubkey (0-hop advert) or 2/3 for a multibyte prefix. `src` is
`advert` or `rxlog`. No hex cell is stored — binning is computed server-side from lat/lon.

## Read API — coverage GeoJSON

`GET /api/nodes/{pubkey}/rx-coverage?bbox={minLat,minLon,maxLat,maxLon}&z={zoom}`

Returns a GeoJSON `FeatureCollection` of hexagons covering where clients heard the node, aggregated
server-side (read-only). Each feature:

```json
{ "type": "Feature",
  "geometry": { "type": "Polygon", "coordinates": [[[lon,lat], ...]] },
  "properties": { "cell": "9:123:-45", "count": 7, "best_snr": -6, "has_sig": true } }
```

- Hex binning is a pure-Go pointy-top grid over Web Mercator (`cmd/server/hexgrid.go`). We do **not**
  use `uber/h3-go` because it is CGO and the project builds with `CGO_ENABLED=0`.
- `z` (Leaflet zoom) selects the hex resolution (zoom-adaptive). Raw points never leave the server
  (privacy: contributors' tracks are not exposed).
- `best_snr` / `has_sig` drive the colour: green→orange by best SNR, grey when no signal metric.

## Frontend

Shown only in the Reach view (`#/nodes/{pubkey}/reach`), as a toggleable hex layer drawn on the
existing Leaflet map (`public/node-reach-coverage.js`), deep-linked via `?coverage=1`. No new
frontend dependencies. Colours come from CSS variables in `public/node-reach.css`
(`--nq-cov-strong|mid|weak|grey`).

## Trust

Identity = the companion pubkey (`rx_pubkey`). The broker ACL binds each client to its own
`{PUBLIC_KEY}` topic, so a client can only contribute under the key it physically holds. Optional
future hardening: have the companion sign a broker-issued token (the firmware exposes on-device
signing) — not required for the MVP.

## Configurable values (future customizer)

Hardcoded initially, tracked for the customizer per AGENTS.md rule 8: hex resolution per zoom
(`zoomToHexRes`), colour SNR thresholds (`coverageColorVar`), and any `rx_at` max-age validation.
