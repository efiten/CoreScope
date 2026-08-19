/* === CoreScope — scope-audit.js ===
   Network-wide scope audit (route #/scope-audit): the whole-network answer
   to "which repeaters declare a region they are not actually forwarding".
   Fetches GET /api/scope-audit once per window and renders one row per
   declared repeater, sorted server-side so the interesting rows (missing a
   declared region, or contradicting its own wildcard) sit at the top.
   Reuses node-scopes.js's vocabulary: scope names are shown without the
   leading '#' (the two backends spell them differently — see normScope
   there), and '*' is never listed as a scope. */
'use strict';
(function () {
  var loadGen = 0; // bumped per load; guards against in-flight races (mirrors node-reach.js)
  var DEFAULT_WINDOW = '24h';
  var WINDOWS = [
    { key: '1h', label: '1h' },
    { key: '24h', label: '24h' },
    { key: '7d', label: '7d' }
  ];
  var win = DEFAULT_WINDOW;

  function windowBtn(key, cur, label) {
    var on = key === cur;
    return '<button data-window="' + key + '" aria-pressed="' + (on ? 'true' : 'false') + '"' +
      (on ? ' class="active"' : '') + '>' + label + '</button>';
  }

  // windowHonestyNote spells out, in the window's own words, why "declared
  // but not observed" is weak evidence over a short window (a quiet region
  // simply has no traffic) and strong evidence over a long one — the reader
  // must never mistake a 1h result for a 7d one.
  function windowHonestyNote(w) {
    var strength = w === '7d' ? 'a strong signal — a full week with zero matching traffic is hard to explain as bad luck'
      : (w === '24h' ? 'a moderate signal — worth checking, but a quiet region can still look this way for a day'
        : 'weak evidence — an hour with no traffic proves very little; treat this as a hint to re-check on 24h or 7d');
    return '<div class="sa-window-note">Rows below are ranked by declared regions with <strong>zero observed forwarding in the last ' +
      escapeHtml(w) + '</strong>. At this window, that absence is ' + strength + '.</div>';
  }

  function pageHtml() {
    return '<div class="sa-page">' +
      '<div class="sa-head">' +
      '<h2>Scope audit</h2>' +
      '<div class="analytics-time-range" id="saWindow">' +
      WINDOWS.map(function (w) { return windowBtn(w.key, win, w.label); }).join('') +
      '</div></div>' +
      '<div class="sa-intro">Network-wide comparison of declared vs. observed region-scope forwarding, across every repeater that has declared a region list over RF. <a href="#/nodes">Per-node detail lives on each node\'s page</a>.</div>' +
      '<div id="saBody"><div class="text-muted" style="padding:8px"><span class="spinner"></span> Loading scope audit…</div></div>' +
      '</div>';
  }

  function ageHtml(row) {
    var age = timeAgo(row.declaredAt);
    return '<span title="Declared regions answer captured ' + escapeHtml(row.declaredAt) + '">' + escapeHtml(age) + '</span>';
  }

  function scopeChips(names, cls) {
    if (!names.length) return '<span class="text-muted">—</span>';
    return names.map(function (n) { return '<span class="sa-chip ' + cls + '">' + escapeHtml(n) + '</span>'; }).join(' ');
  }

  function undeclaredChips(rows) {
    if (!rows.length) return '<span class="text-muted">—</span>';
    return rows.map(function (o) {
      return '<span class="sa-chip sa-chip-undeclared" title="' + o.packets + ' packet' + (o.packets === 1 ? '' : 's') +
        ', last seen ' + escapeHtml(timeAgo(o.lastSeen)) + '">' + escapeHtml(o.scope) + '</span>';
    }).join(' ');
  }

  // nameHtml renders the repeater identity cell. row.name == null means this
  // instance holds NO nodes row for the target at all (a declared-regions
  // answer can name a repeater the network has never recorded) — distinct
  // from row.name === "" (a known node that simply has no name). A
  // truthiness check collapses those two into the same pubkey-stub
  // rendering, which defeats the point of the API making the field
  // nullable. The unknown case also gets no link (FIX 4): #/nodes/<pubkey>
  // cannot resolve a target we hold no row for.
  function nameHtml(row) {
    if (row.name == null) {
      return '<span class="sa-name-unknown" title="No nodes row held for this target — a declared-regions answer can name a repeater this instance has never recorded.">' +
        escapeHtml(row.publicKey.slice(0, 10)) + '… (unknown node)</span>';
    }
    var name = row.name ? escapeHtml(row.name) : escapeHtml(row.publicKey.slice(0, 10)) + '…';
    return '<a href="#/nodes/' + encodeURIComponent(row.publicKey) + '">' + name + '</a>';
  }

  // ambiguousCaveat flags rows with a non-zero FIX 1 ambiguity count: hops
  // whose truncated hash prefix matched more than one declared target were
  // credited to none of them, so a notObserved finding here could be a
  // prefix collision rather than a confirmed gap.
  function ambiguousCaveat(row) {
    if (!row.ambiguousHops) return '';
    return ' <span class="sa-chip sa-chip-ambiguous" title="' + row.ambiguousHops + ' forwarder hop' + (row.ambiguousHops === 1 ? '' : 's') +
      ' in this window matched more than one declared target\'s pubkey prefix and could not be attributed to any of them. Any “not observed” entry on this row may be explained by that prefix collision rather than a real gap.">possibly ambiguous</span>';
  }

  function rowHtml(row) {
    var issues = [];
    if (row.notObserved.length) issues.push('<span class="ns-decl ns-decl-quiet" title="Declared flood-allowed but not observed forwarding it this window.">' + row.notObserved.length + ' not observed</span>');
    if (row.wildcardContradiction) issues.push('<span class="ns-decl ns-decl-warn" title="Observed forwarding plain (unscoped) floods, but the declared list omits the &#39;*&#39; wildcard that would allow that.">wildcard contradiction</span>');
    if (row.undeclaredObserved.length) issues.push('<span class="ns-decl ns-decl-unknown" title="Observed forwarding scopes absent from the declared list.">' + row.undeclaredObserved.length + ' undeclared</span>');
    var issuesHtml = issues.length ? issues.join(' ') : '<span class="ns-decl ns-decl-yes" title="Every declared region was observed forwarding, and nothing undeclared was observed.">agrees</span>';

    return '<tr>' +
      '<td class="sa-name">' + nameHtml(row) + (row.role != null && row.role !== '' ? '<span class="text-muted sa-role"> ' + escapeHtml(row.role) + '</span>' : '') + '</td>' +
      '<td>' + issuesHtml + '</td>' +
      '<td>' + scopeChips(row.declaredRegions, 'sa-chip-declared') + (row.declaredWildcard ? ' <span class="sa-chip sa-chip-wildcard" title="Declares the \'*\' wildcard — allows plain unscoped floods.">*</span>' : '') + '</td>' +
      '<td>' + scopeChips(row.notObserved, 'sa-chip-missing') + ambiguousCaveat(row) + '</td>' +
      '<td>' + undeclaredChips(row.undeclaredObserved) + '</td>' +
      '<td>' + ageHtml(row) + (row.truncated ? ' <span class="ns-truncated" title="Declared list was truncated by the repeater — a missing region here is not necessarily a real absence.">truncated</span>' : '') + '</td>' +
      '</tr>';
  }

  function renderBody(d) {
    var el = document.getElementById('saBody');
    if (!el) return;
    if (!d.repeaters.length) {
      el.innerHTML = '<div class="ns-empty">No repeater has declared a region list yet — this fills in as devices drive and answer over RF.</div>';
      return;
    }
    el.innerHTML = windowHonestyNote(d.window) +
      '<div class="sa-table-wrap"><table class="ns-table sa-table"><thead><tr>' +
      '<th>Repeater</th><th>Status</th><th>Declared</th><th>Not observed</th><th>Undeclared observed</th><th>Declared age</th>' +
      '</tr></thead><tbody>' +
      d.repeaters.map(rowHtml).join('') +
      '</tbody></table></div>' +
      '<div class="sa-count text-muted">' + d.repeaters.length + ' repeater' + (d.repeaters.length === 1 ? '' : 's') + ' with a declared region list.</div>';
  }

  async function load(w) {
    win = w;
    var myGen = ++loadGen;
    var head = document.getElementById('saWindow');
    if (head) {
      WINDOWS.forEach(function (wd) {
        var b = head.querySelector('button[data-window="' + wd.key + '"]');
        if (b) { b.classList.toggle('active', wd.key === w); b.setAttribute('aria-pressed', wd.key === w ? 'true' : 'false'); }
      });
    }
    var body = document.getElementById('saBody');
    if (body) body.innerHTML = '<div class="text-muted" style="padding:8px"><span class="spinner"></span> Loading scope audit…</div>';
    var d;
    try {
      d = await api('/scope-audit?window=' + encodeURIComponent(w), { ttl: 30000 });
    } catch (e) {
      if (myGen !== loadGen) return;
      if (body) body.innerHTML = '<div class="ns-empty">Failed to load scope audit: ' + escapeHtml(e.message) + '</div>';
      return;
    }
    if (myGen !== loadGen) return;
    renderBody(d);
    syncHash();
  }

  function syncHash() {
    try { history.replaceState(null, '', '#/scope-audit?window=' + win); } catch (e) {}
  }

  function init(container) {
    win = DEFAULT_WINDOW;
    try {
      var p = (typeof getHashParams === 'function') ? getHashParams() : null;
      var qw = p ? p.get('window') : null;
      if (qw && WINDOWS.some(function (w) { return w.key === qw; })) win = qw;
    } catch (e) {}
    container.innerHTML = pageHtml();
    var bar = document.getElementById('saWindow');
    if (bar) bar.addEventListener('click', function (e) {
      var b = e.target.closest('button[data-window]');
      if (b) load(b.getAttribute('data-window'));
    });
    load(win);
  }

  function destroy() { loadGen++; }

  registerPage('scope-audit', { init: init, destroy: destroy });
})();
