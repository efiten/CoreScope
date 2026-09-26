/* locode-column.js — FORK-LOCAL. A Location column derived from the node name.
 *
 * MeshCore node names in this region follow a CC-LOC convention (BE-ANR-...,
 * DE-NW-...), and public/locode.json maps those codes to country and place
 * names. So a repeater's location is already in its name and needs no server
 * field, no API change and no schema change: this is a pure client-side lookup.
 *
 * Measured on the live deployment, a 500-node sample (the /api/nodes cap): 330
 * names follow the convention and 321 resolve to a place, of which 318 are
 * repeaters, out of 487. So roughly two in three repeaters get a location, and
 * the third that does not is a naming gap rather than a gap in locode.json,
 * which failed to resolve only 9 parsed codes, each once.
 *
 * Why a separate file, and not a few lines inside scope-audit.js: upstream
 * churns the screens, not the files it does not know about. public/nodes.js took
 * 19 upstream commits in three months and public/scope-audit.js 3, while
 * public/radio-band.js took 0 because it is fork-only. Keeping the logic here and
 * the wiring down to three lines per screen is what makes this survive an
 * integration. Same pattern as radio-band.js, and the reason is recorded at each
 * wiring point so a merge conflict there is legible.
 *
 * Deliberately NOT upstreamed: upstream has no locode at all.
 */
(function () {
  'use strict';

  // locode.json is 947 KB, so this shares locode.js's single load rather than
  // fetching it again. Both files are fork-local and always ship together.
  function ensure() {
    if (!window.ensureLocodeData) {
      console.warn('[locode-column] locode.js did not expose ensureLocodeData; no Location values');
      return Promise.resolve(null);
    }
    return window.ensureLocodeData();
  }

  // placeOf resolves a node name to { place, country }, or null when the name
  // does not follow the convention or the code is not in locode.json. Null is
  // the honest answer: an unresolved name must render as empty rather than be
  // guessed at.
  function placeOf(name, data) {
    if (!data || !window.parseLocodeName) return null;
    var parsed = window.parseLocodeName(name);
    if (!parsed) return null;
    var country = (data.countries || {})[parsed.cc];
    if (!country) return null;
    // Regions before locations, so an explicit ISO 3166-2 region code wins over
    // a colliding city code (NRW the Bundesland over Neuweier the town). This
    // mirrors buildLocodeHtml in locode.js on purpose; if one changes the other
    // has to, or the column and the tooltip will disagree about the same node.
    var place = ((data.regions || {})[parsed.cc] || {})[parsed.loc] ||
                ((data.locations || {})[parsed.cc] || {})[parsed.loc];
    if (!place) return null;
    return { place: place, country: country, cc: parsed.cc, loc: parsed.loc };
  }

  function escapeAttr(s) {
    return String(s)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }

  // cellHtml returns a complete <td> for the sortable table. The sort value goes
  // in data-value, which is what public/table-sort.js reads (preferring it over
  // textContent), so the column sorts on the place name rather than on whatever
  // the cell happens to render.
  //
  // An unresolved name yields data-value="", which sorts to the top ascending.
  // That is a deliberate choice over a sentinel: the rows without a location are
  // the ones an operator may want to go and rename.
  function cellHtml(name) {
    var r = placeOf(name, _data);
    if (!r) return '<td class="lc-place" data-value=""></td>';
    return '<td class="lc-place" data-value="' + escapeAttr(r.place) + '" title="' +
      escapeAttr(r.country + ' · ' + r.cc + '-' + r.loc) + '">' +
      escapeAttr(r.place) + '</td>';
  }

  var _data = null;

  // prime resolves locode.json once and keeps it for the page lifetime, so
  // cellHtml stays synchronous and can be called from inside a row template.
  // Callers await this before rendering.
  //
  // It never rejects, and that is the point rather than laziness. The caller
  // awaits it next to the page's own fetch, so a rejection here would surface as
  // "Failed to load scope audit" for a page that loaded perfectly well, and a
  // rejection that the caller skips past on its own error path would go
  // unhandled. A missing locode.json costs the column, nothing else.
  function prime() {
    return ensure().then(function (d) {
      _data = d;
      return d;
    }).catch(function (e) {
      console.warn('[locode-column] locode.json did not load, Location stays empty:', e && e.message);
      _data = null;
      return null;
    });
  }

  function headerHtml() {
    return '<th data-sort-key="place" title="Derived from the CC-LOC prefix in the node name, via locode.json. Empty means the name does not carry one.">Location</th>';
  }

  window.LocodeColumn = {
    prime: prime,
    headerHtml: headerHtml,
    cellHtml: cellHtml,
    // Exposed for tests: pure, takes its data rather than reading module state.
    placeOf: placeOf,
  };
})();
