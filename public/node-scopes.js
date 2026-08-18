/* === CoreScope — node-scopes.js ===
   Per-node "Scopes" section: fetches /api/nodes/{pubkey}/scopes once and
   renders observed-vs-declared region-scope conformance into the node
   detail view (an embedded card, not a routed page — this is one section
   on an existing page). Mirrors node-reach.js's fetch/render/empty-state
   shape and its window-button control. Repeaters only; the caller (nodes.js)
   only mounts the container for role === 'repeater'. */
'use strict';
(function () {
  var loadGen = 0; // bumped per load; guards against in-flight races (mirrors node-reach.js)
  var DEFAULT_WINDOW = '24h'; // matches the server default
  var WINDOWS = [
    { key: '1h', label: '1h' },
    { key: '24h', label: '24h' },
    { key: '7d', label: '7d' }
  ];

  function windowBtn(key, cur, label) {
    var on = key === cur;
    return '<button data-window="' + key + '" aria-pressed="' + (on ? 'true' : 'false') + '"' +
      (on ? ' class="active"' : '') + '>' + label + '</button>';
  }

  function statCard(label, value, descShort, descFull) {
    return '<div class="analytics-stat-card" title="' + escapeHtml(descFull) + '">' +
      '<div class="analytics-stat-label">' + escapeHtml(label) + '</div>' +
      '<div class="analytics-stat-value">' + escapeHtml(String(value)) + '</div>' +
      '<div class="analytics-stat-desc">' + escapeHtml(descShort) + '</div></div>';
  }

  // buildRows merges observed[] with declared.regions[] into one row per
  // distinct scope name, so a scope that is declared but never observed
  // still gets a row (the row Step 2b exists to surface) instead of being
  // silently absent because it has no packets to iterate.
  function buildRows(observed, declared) {
    var byName = Object.create(null);
    observed.forEach(function (o) {
      byName[o.scope] = { scope: o.scope, packets: o.packets, lastSeen: o.lastSeen, observed: true };
    });
    if (declared) {
      declared.regions.forEach(function (r) {
        if (!byName[r]) byName[r] = { scope: r, packets: 0, lastSeen: null, observed: false };
      });
    }
    var rows = Object.keys(byName).map(function (k) { return byName[k]; });
    // Observed scopes ordered by packet count (brief requirement); declared-only
    // rows (0 packets) sort after, alphabetically among themselves.
    rows.sort(function (a, b) {
      if (b.packets !== a.packets) return b.packets - a.packets;
      return a.scope.localeCompare(b.scope);
    });
    return rows;
  }

  // declaredBadge renders exactly one of the four states this screen must
  // keep visually distinct: never-asked (declared === null, silence carries
  // no meaning), declared+observed (agreement), declared-but-not-observed
  // (the row this body of work exists to surface), observed-but-not-declared
  // (forwarding something it doesn't admit to, or a stale declared list).
  function declaredBadge(row, declared) {
    if (!declared) {
      return '<span class="ns-decl ns-decl-unknown" title="This repeater has never successfully answered a declared-regions request — out of direct RF range, firmware below v13, or the request was silently ignored (only DIRECT-routed requests get answered). Silence carries no meaning; this is not the same as declaring nothing.">not asked</span>';
    }
    var declaredHere = declared.regions.indexOf(row.scope) !== -1;
    if (row.observed && declaredHere) {
      return '<span class="ns-decl ns-decl-yes" title="Declared flood-allowed and observed forwarding it — agreement.">declared</span>';
    }
    if (declaredHere && !row.observed) {
      return '<span class="ns-decl ns-decl-quiet" title="Declared flood-allowed but not observed forwarding it this window — could be a quiet region, or a repeater that silently stopped.">declared, not observed</span>';
    }
    return '<span class="ns-decl ns-decl-warn" title="Observed forwarding this scope but it is absent from the declared list — either forwarding something it does not admit to, or the declared list was captured before a config change.">observed, not declared</span>';
  }

  function scopeRow(row, declared) {
    return '<tr>' +
      '<td class="ns-scope-name">' + escapeHtml(row.scope) + '</td>' +
      '<td class="ns-n">' + (row.observed ? row.packets : '—') + '</td>' +
      '<td class="ns-n">' + (row.observed ? timeAgo(row.lastSeen) : '—') + '</td>' +
      '<td>' + declaredBadge(row, declared) + '</td>' +
      '</tr>';
  }

  function declaredMetaHtml(declared) {
    if (!declared) {
      return '<div class="ns-declared-meta"><span class="ns-never-asked">Declared regions: never successfully asked (out of direct RF range, old firmware, or the RF request was silently ignored).</span></div>';
    }
    var age = timeAgo(declared.observedAt);
    var truncated = declared.truncated
      ? ' <span class="ns-truncated">list truncated — entries may have been silently dropped; a missing region here is not necessarily a real absence.</span>'
      : '';
    var declaresNothing = declared.regions.length === 0
      ? ' — it declares no regions flood-allowed.'
      : '';
    return '<div class="ns-declared-meta">Declared regions answer captured ' + escapeHtml(age) + '.' + truncated + declaresNothing + '</div>';
  }

  function routesHtml(routes) {
    return '<div class="ns-routes" title="Route-type breakdown of packets this node was observed FORWARDING (last hop of a FLOOD-family route). direct/transportDirect are always 0 by construction — a DIRECT route\'s last path hop is the route\'s far end, never the transmitter, so this node can never be attributed as the forwarder of one.">' +
      'Route mix (forwarded): transportFlood <b>' + routes.transportFlood + '</b> &middot; flood <b>' + routes.flood + '</b> &middot; direct <b>' + routes.direct + '</b> &middot; transportDirect <b>' + routes.transportDirect + '</b>' +
      '</div>';
  }

  async function load(container, pubkey, win) {
    var myGen = ++loadGen;
    container.innerHTML = headHtml(win) + '<div class="text-muted" style="padding:8px"><span class="spinner"></span> Loading scopes…</div>';
    var d;
    try {
      d = await api('/nodes/' + encodeURIComponent(pubkey) + '/scopes?window=' + win, { ttl: 30000 });
    } catch (e) {
      if (myGen !== loadGen) return;
      container.innerHTML = headHtml(win) + '<div class="ns-empty">Failed to load scopes: ' + escapeHtml(e.message) + '</div>';
      wireWindowButtons(container, pubkey);
      return;
    }
    if (myGen !== loadGen) return;

    var rows = buildRows(d.observed, d.declared);

    var statsHtml = '<div class="analytics-stats">' +
      statCard('Unmatched', d.unmatched, 'Scoped, no key held',
        'Packets that carried a transport scope but matched no configured region key this CoreScope instance holds — a neighbouring region may exist with no key configured here.') +
      statCard('Unscoped', d.unscoped, 'No scope at all',
        'Packets that carried no scope at all (Code1 = 0000, or a non-transport route).') +
      statCard('Declared regions', d.declared ? d.declared.regions.length : '—', d.declared ? 'Flood-allowed, last answer' : 'Never successfully asked',
        d.declared ? 'Number of regions this repeater declared flood-allowed in its most recent answer.' : 'This repeater has never successfully answered a declared-regions request.') +
      '</div>';

    // configIssue must be judged from observed alone, not from rows (the
    // observed/declared union) — a repeater with declared-only rows and
    // unmatched>0 still has a config problem (this deployment holds no key
    // that could ever name its traffic), and that note must render even
    // though rows is non-empty. See FIX 1.
    var configIssue = d.observed.length === 0 && d.unmatched > 0;
    var noteHtml = configIssue
      ? '<div class="ns-empty ns-empty-config">No scope could be named for this repeater\'s traffic in the last ' + win + ' — ' + d.unmatched + ' scoped packet' + (d.unmatched === 1 ? '' : 's') + ' matched no configured region key. Scope tracking may not be configured on this deployment; check the region-keys configuration.</div>'
      : '';
    var bodyHtml;
    if (rows.length === 0) {
      bodyHtml = configIssue
        ? ''
        : '<div class="ns-empty">No scope data — this repeater has forwarded nothing we observed carrying a region scope in the last ' + win + '.</div>';
    } else {
      bodyHtml = '<table class="ns-table"><thead><tr><th>Scope</th><th>Packets</th><th>Last seen</th><th>Declared</th></tr></thead><tbody>' +
        rows.map(function (r) { return scopeRow(r, d.declared); }).join('') +
        '</tbody></table>';
    }

    container.innerHTML = headHtml(win) + statsHtml + declaredMetaHtml(d.declared) +
      routesHtml(d.routes) + noteHtml + bodyHtml +
      '<div class="ns-links"><a href="#/analytics?tab=hashsizes">Hash-size analytics &rarr;</a></div>';
    wireWindowButtons(container, pubkey);
  }

  function headHtml(win) {
    return '<div class="ns-head">' +
      '<h4>Scopes</h4>' +
      '<div class="analytics-time-range ns-window" id="nsWindow">' +
      WINDOWS.map(function (w) { return windowBtn(w.key, win, w.label); }).join('') +
      '</div></div>';
  }

  function wireWindowButtons(container, pubkey) {
    var bar = container.querySelector('#nsWindow');
    if (!bar) return;
    bar.addEventListener('click', function (e) {
      var b = e.target.closest('button[data-window]');
      if (b) load(container, pubkey, b.getAttribute('data-window'));
    });
  }

  function render(container, pubkey) {
    load(container, pubkey, DEFAULT_WINDOW);
  }

  window.NodeScopes = { render: render };
})();
