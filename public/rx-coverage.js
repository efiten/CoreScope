/* === CoreScope — rx-coverage.js ===
   Mobile RX coverage hub (route #/rx-coverage):
   - global H3-style hex coverage map (all mobile observers), time-windowed
   - leaderboard of top mobile observers (companion name + counts)
   - click an observer to filter the map to just their coverage
   Fork-only feature; isolated page (no changes to the core map). */
'use strict';
(function () {
  var map = null, covLayer = null, days = 7, selectedRx = '', selectedName = '', boardCache = [], destroyed = false;

  function cssColor(varName) {
    try { return getComputedStyle(document.documentElement).getPropertyValue(varName).trim() || '#888'; }
    catch (e) { return '#888'; }
  }
  function colorVar(p) {
    if (!p || !p.has_sig || p.best_snr == null) return '--nq-cov-grey';
    var s = Number(p.best_snr);
    if (s >= -6) return '--nq-cov-strong';
    if (s >= -14) return '--nq-cov-mid';
    return '--nq-cov-weak';
  }

  function dayBtn(d) { return '<button data-days="' + d + '"' + (d === days ? ' class="active"' : '') + '>' + (d === 1 ? '24h' : d + 'd') + '</button>'; }

  function pageHtml() {
    return '<div style="max-width:1100px;margin:0 auto;padding:12px 16px">' +
      '<h2 style="margin:4px 0 2px;font-size:18px">🗺️ Mobile RX coverage</h2>' +
      '<div style="color:var(--text-muted);font-size:11px">Where roaming CoreScope-RX clients heard nodes. Colour = best signal per cell.</div>' +
      '<div class="analytics-time-range" id="rxDays" style="margin:8px 0">' + dayBtn(1) + dayBtn(7) + dayBtn(14) + dayBtn(30) + '</div>' +
      '<div class="nq-cov-legend"><span><i style="background:var(--nq-cov-strong)"></i>strong</span><span><i style="background:var(--nq-cov-mid)"></i>medium</span><span><i style="background:var(--nq-cov-weak)"></i>weak</span><span><i style="background:var(--nq-cov-grey)"></i>no signal</span></div>' +
      '<div id="rxMap" style="height:60vh;min-height:360px;border:1px solid var(--border,#d0d7de);border-radius:6px;margin:8px 0"></div>' +
      '<div class="nq-group-h">Top mobile observers</div>' +
      '<div id="rxBoard" class="rxb"></div>' +
      '</div>';
  }

  function drawCoverage() {
    if (!map || destroyed) return;
    var b = map.getBounds();
    var bbox = [b.getSouth(), b.getWest(), b.getNorth(), b.getEast()].join(',');
    var url = '/api/rx-coverage?bbox=' + bbox + '&z=' + map.getZoom() + '&days=' + days + (selectedRx ? '&rx=' + encodeURIComponent(selectedRx) : '');
    fetch(url).then(function (r) { return r.json(); }).then(function (fc) {
      if (destroyed || !covLayer) return;
      covLayer.clearLayers();
      (fc.features || []).forEach(function (f) {
        var ring = (f.geometry.coordinates[0] || []).map(function (c) { return [c[1], c[0]]; });
        var col = cssColor(colorVar(f.properties));
        L.polygon(ring, { color: col, weight: 1, fillColor: col, fillOpacity: 0.45 }).addTo(covLayer)
          .bindTooltip('n=' + f.properties.count + (f.properties.best_snr != null ? ' · SNR ' + f.properties.best_snr : ' · no signal'));
      });
    }).catch(function () {});
  }

  function renderBoard() {
    var el = document.getElementById('rxBoard');
    if (!el) return;
    if (!boardCache.length) { el.innerHTML = '<div class="muted" style="color:var(--text-muted);font-size:13px">No mobile observers in this window yet.</div>'; return; }
    var rows = boardCache.map(function (o, i) {
      var nm = o.name ? escapeHtml(o.name) : (o.pubkey.slice(0, 10) + '…');
      return '<div class="rxb-row' + (o.pubkey === selectedRx ? ' sel' : '') + '" data-rx="' + o.pubkey + '" data-name="' + escapeHtml(o.name || '') + '">' +
        '<span class="rxb-rank">' + (i + 1) + '</span><span class="rxb-name">' + nm + '</span>' +
        '<span class="rxb-rec">' + o.receptions + '</span><span class="rxb-nodes">' + o.nodes + '</span></div>';
    }).join('');
    el.innerHTML = (selectedRx ? '<button id="rxAll" class="btn-primary" style="margin:0 0 8px">← Show all observers</button>' : '') +
      '<div class="rxb-row rxb-head"><span class="rxb-rank">#</span><span class="rxb-name">Observer (companion)</span><span class="rxb-rec">pkts</span><span class="rxb-nodes">nodes</span></div>' + rows;
    el.querySelectorAll('.rxb-row[data-rx]').forEach(function (r) {
      r.addEventListener('click', function () {
        selectedRx = r.dataset.rx; selectedName = r.dataset.name || '';
        renderBoard(); drawCoverage(); syncHash();
      });
    });
    var all = document.getElementById('rxAll');
    if (all) all.addEventListener('click', function () { selectedRx = ''; selectedName = ''; renderBoard(); drawCoverage(); syncHash(); });
  }

  function loadBoard() {
    fetch('/api/rx-leaderboard?days=' + days + '&limit=25').then(function (r) { return r.json(); })
      .then(function (d) { if (destroyed) return; boardCache = d.observers || []; renderBoard(); }).catch(function () {});
  }

  function setDays(d) {
    days = d;
    var bar = document.getElementById('rxDays');
    if (bar) bar.querySelectorAll('button').forEach(function (b) { b.classList.toggle('active', +b.dataset.days === d); });
    loadBoard(); drawCoverage(); syncHash();
  }

  function syncHash() {
    var q = 'days=' + days + (selectedRx ? '&rx=' + selectedRx : '');
    try { history.replaceState(null, '', '#/rx-coverage?' + q); } catch (e) {}
  }

  function init(container) {
    destroyed = false; selectedRx = ''; selectedName = ''; days = 7; boardCache = [];
    try {
      var p = (typeof getHashParams === 'function') ? getHashParams() : null;
      if (p) { var dd = parseInt(p.get('days'), 10); if ([1, 7, 14, 30].indexOf(dd) >= 0) days = dd; selectedRx = (p.get('rx') || '').toLowerCase(); }
    } catch (e) {}
    container.innerHTML = pageHtml();
    map = L.map('rxMap', { zoomControl: true, attributionControl: false }).setView([51.0, 4.8], 8);
    if (typeof window._applyTilesToNodeMap === 'function') window._applyTilesToNodeMap(map);
    else L.tileLayer('https://tile.openstreetmap.org/{z}/{x}/{y}.png', { maxZoom: 19 }).addTo(map);
    covLayer = L.layerGroup().addTo(map);
    map.on('moveend zoomend', drawCoverage);
    var bar = document.getElementById('rxDays');
    if (bar) bar.addEventListener('click', function (e) { var b = e.target.closest('button[data-days]'); if (b) setDays(+b.dataset.days); });
    setTimeout(function () { if (!destroyed && map) { map.invalidateSize(); drawCoverage(); } }, 150);
    loadBoard();
  }

  function destroy() {
    destroyed = true;
    if (map) { try { map.remove(); } catch (e) {} map = null; }
    covLayer = null;
  }

  registerPage('rx-coverage', { init: init, destroy: destroy });
})();
