# Nodes

The Nodes page lists every node your mesh has seen — repeaters, companions, rooms, and sensors.

[Screenshot: nodes list with status indicators]

## What you see

Each row shows:

- **Name** — the node's advertised name (or public key if unnamed)
- **Role** — Repeater, Companion, Room, or Sensor
- **Status** — color-coded health indicator
- **Last seen** — when the node was last heard
- **Advert count** — how many advertisements this node has sent

## Status indicators

| Indicator | Meaning |
|-----------|---------|
| 🟢 Active | Heard recently (within threshold for its role) |
| 🟡 Degraded | Not heard for a while but not yet silent |
| 🔴 Silent | Not heard for an extended period |

Thresholds differ by role. Infrastructure nodes (repeaters, rooms) have longer grace periods than companions. See [Configuration](configuration.md) for `healthThresholds`.

## Filtering

### Role tabs

Click **All**, **Repeaters**, **Rooms**, **Companions**, or **Sensors** to filter by role.

### Search

Type in the search box to filter by name or public key. The filter applies instantly.

### Status filter

Filter to show only active, degraded, or silent nodes.

### Area filter

If [areas are configured](area-filter.md), an area pill bar appears above the list. Selecting an area shows only nodes whose GPS position falls within that area.

### Last heard filter

Filter nodes by how recently they were heard (e.g., last hour, last 24h).

## Sorting

Click any column header to sort. Click again to reverse the order. Your sort preference is saved across sessions.

## Node detail

Click a node row to open the **detail pane** on the right. It shows:

- Full public key
- Role and status explanation
- Location (if known)
- Recent packets involving this node
- Neighbor nodes
- Signal statistics

Click the node name in the detail pane to open the **full node page** with complete history, analytics, and health data.

## Neighbours card

The neighbours card lists the nodes this node exchanges traffic with. Most columns come from packet paths (score, observations, last seen). The **Measured** column comes from the neighbours report the node sends through an observer (see [Personal MQTT logins](../observer-mqtt-logins.md#neighbour-reports-how-observers-send-them)).

| Part | Meaning |
|---|---|
| `in 5.5 dB` | SNR with which this node hears the neighbour |
| `out -2.0 dB` | SNR with which the neighbour hears this node |
| `not measured` | The report has no SNR for that direction |
| age | How long ago the report was received |
| **verified** | The reporter is an observer with a confirmed personal login, and the report came through an MQTT source with `verifiesObservers` (see [Verified neighbour reports](../observer-mqtt-logins.md#verified-neighbour-reports)). A link is verified only when every measured direction is |
| **not verified** | Any other report. The numbers are what the node claimed |

The column sorts by the weaker of the two SNRs and is coloured good (5 dB or more), fair (-5 dB or more) or poor.

A row named `<prefix>… (only known from neighbour reports)` has no packet evidence at all and no known node with that prefix. If the tooltip of a Measured cell reads "does not answer scope queries", the report's status for that neighbour is a timeout: the neighbour did not answer the scope query.

The line above the table reads for example `3 of 5 neighbours queried, reported 4m ago`: how many of the node's neighbours it queried, out of how many it has. `list cut short` means the report was truncated; a neighbour missing from it is not treated as gone.

In the node details, **Floods under** shows the region the node floods under when no scope is chosen, taken from its neighbours report. `*` (shown as "unscoped") means it floods without a region.

## Favorites

Nodes you've claimed on the Home page appear as favorites. You can also star nodes directly from the Nodes page.

## Tips

- Use the search box for quick lookups — it matches partial names and keys
- Sort by "Last seen" descending to find the most active nodes
- The status explanation tells you exactly why a node is marked degraded or silent
