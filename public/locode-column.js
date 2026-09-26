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
    // A province or Bundesland is NOT a location, and this column must not fill
    // itself with one. An earlier version looked in `regions` first, the way
    // buildLocodeHtml does for the tooltip, and the result was that 601 of the 964
    // resolving nodes showed a province while only 363 showed a real place:
    // DE-NW-* as "Nordrhein-Westfalen", BE-VAN-* as "Antwerpen" the province,
    // nl-li-* as "Limburg". The tooltip may say that, since it lists every field
    // it can decode. A column headed Location may not.
    //
    // Reading the city code out of the THIRD position does not rescue them and
    // was measured, not assumed: of those 601 it resolves for 80, and roughly half
    // of those 80 are wrong, because German Kfz codes occupy the same two- and
    // three-letter space as UN/LOCODE with different meanings. DE-NW-HSK is
    // Hochsauerlandkreis and resolves to Kaisersesch in Rhineland-Palatinate;
    // DE-NW-HER is Herne and resolves to Herbrechtingen in Baden-Wuerttemberg.
    // Those rows are better served by the GPS fallback, which gives a real place
    // with a distance the reader can judge.
    //
    // Checking regions first and rejecting is deliberate rather than just reading
    // locations: DE-NRW is both a Bundesland and the town Neuweier, and an
    // operator writing DE-NRW means the Bundesland.
    if (((data.regions || {})[parsed.cc] || {})[parsed.loc]) return null;
    var place = ((data.locations || {})[parsed.cc] || {})[parsed.loc];
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
  // A row with neither a resolvable name nor usable GPS yields data-value="",
  // which sorts to the top ascending. That is a choice over a sentinel: those are
  // the rows an operator may want to go and rename.
  //
  // Two kinds of answer, and they are deliberately not the same. A DECLARED
  // location is what the operator put in the node name. A DERIVED one is the
  // nearest coded place to the node's GPS. On a page about what operators declare
  // versus what is observed, collapsing that difference would be the wrong
  // simplification, so derived values are emphasised and say so in the tooltip.
  //
  // <em> rather than a CSS italic on purpose: it puts the distinction in the
  // markup, so it is not visual-only, which is what the axe-core gate exists to
  // catch. The reason and the distance travel in the title as well.
  function cellHtml(name, pubkey) {
    var r = placeOf(name, _data);
    if (r) {
      return '<td class="lc-place" data-value="' + escapeAttr(r.place) + '" title="' +
        escapeAttr('Declared in the node name: ' + r.country + ' · ' + r.cc + '-' + r.loc) + '">' +
        escapeAttr(r.place) + '</td>';
    }
    var ll = pubkey && _coords ? _coords[pubkey] : null;
    if (ll) {
      var near = nearestPlace(ll[0], ll[1]);
      if (near) {
        var dist = near.km < 1 ? (Math.round(near.km * 1000) + ' m') : (near.km.toFixed(1) + ' km');
        return '<td class="lc-place lc-derived" data-value="' + escapeAttr(near.place) + '" title="' +
          escapeAttr('Derived from the node’s GPS, not declared in its name: nearest coded place is ' +
            near.place + ' (' + near.country + ' · ' + near.cc + '-' + near.loc + '), ' + dist +
            ' away. UN/LOCODE coordinates are precise to about 1.85 km.') + '"><em>' +
          escapeAttr(near.place) + '</em></td>';
      }
    }
    return '<td class="lc-place" data-value=""></td>';
  }

  var _data = null;
  var _index = null;   // bucketed place coordinates, built once
  var _coords = null;  // pubkey -> [lat, lon]

  // How far a node may be from a place before the label stops being a claim
  // about where it is. UN/LOCODE coordinates are degrees and minutes, so the
  // source itself is only precise to one arc-minute, about 1.85 km. Measured on
  // the live Scope Audit rows that carry GPS but no resolvable name: median
  // 1.3 km to the nearest place, and all 93 within 5 km. Beyond that the nearest
  // place is regularly in the next municipality, so 5 km is where it stops.
  var MAX_KM = 5;

  // 0.1 degrees is about 11 km of latitude and 7 km of longitude at this
  // latitude, both comfortably more than MAX_KM, so a query only has to look at
  // its own bucket and the eight around it. Without this, one render is 283 rows
  // times 30,613 places.
  function bucketKey(lat, lon) {
    return Math.round(lat * 10) + ':' + Math.round(lon * 10);
  }

  function buildIndex(coordsByCc) {
    var idx = {};
    for (var cc in coordsByCc) {
      if (!Object.prototype.hasOwnProperty.call(coordsByCc, cc)) continue;
      var byLoc = coordsByCc[cc];
      for (var loc in byLoc) {
        if (!Object.prototype.hasOwnProperty.call(byLoc, loc)) continue;
        var ll = byLoc[loc];
        var k = bucketKey(ll[0], ll[1]);
        (idx[k] = idx[k] || []).push([ll[0], ll[1], cc, loc]);
      }
    }
    return idx;
  }

  function km(lat1, lon1, lat2, lon2) {
    var toRad = Math.PI / 180;
    var dLat = (lat2 - lat1) * toRad;
    var dLon = (lon2 - lon1) * toRad;
    var a = Math.sin(dLat / 2) * Math.sin(dLat / 2) +
      Math.cos(lat1 * toRad) * Math.cos(lat2 * toRad) * Math.sin(dLon / 2) * Math.sin(dLon / 2);
    return 6371 * 2 * Math.asin(Math.sqrt(a));
  }

  // nearestPlace returns { place, country, cc, loc, km } for the closest coded
  // place within MAX_KM, or null. Null rather than the nearest-at-any-distance:
  // a place 40 km away is not where the node is, and saying so would be worse
  // than saying nothing.
  function nearestPlace(lat, lon) {
    if (!_index || !_data || typeof lat !== 'number' || typeof lon !== 'number') return null;
    if (!isFinite(lat) || !isFinite(lon) || (lat === 0 && lon === 0)) return null;
    var bl = Math.round(lat * 10), bo = Math.round(lon * 10);
    var best = null;
    for (var i = -1; i <= 1; i++) {
      for (var j = -1; j <= 1; j++) {
        var bucket = _index[(bl + i) + ':' + (bo + j)];
        if (!bucket) continue;
        for (var n = 0; n < bucket.length; n++) {
          var d = km(lat, lon, bucket[n][0], bucket[n][1]);
          if (d <= MAX_KM && (!best || d < best.km)) {
            best = { km: d, cc: bucket[n][2], loc: bucket[n][3] };
          }
        }
      }
    }
    if (!best) return null;
    var country = (_data.countries || {})[best.cc];
    var place = ((_data.regions || {})[best.cc] || {})[best.loc] ||
                ((_data.locations || {})[best.cc] || {})[best.loc];
    if (!place || !country) return null;
    best.place = place;
    best.country = country;
    return best;
  }

  // prime resolves locode.json once and keeps it for the page lifetime, so
  // cellHtml stays synchronous and can be called from inside a row template.
  // Callers await this before rendering.
  //
  // It never rejects, and that is the point rather than laziness. The caller
  // awaits it next to the page's own fetch, so a rejection here would surface as
  // "Failed to load scope audit" for a page that loaded perfectly well, and a
  // rejection that the caller skips past on its own error path would go
  // unhandled. A missing locode.json costs the column, nothing else.
  // withGps also loads the place coordinates and the node coordinates, which is
  // what the derived fallback needs. Both are extra weight, so a caller that only
  // wants declared locations does not pay for them: locode-coords.json is 647 KB
  // and the node list is several pages.
  function prime(opts) {
    var withGps = !!(opts && opts.withGps);
    return ensure().then(function (d) {
      _data = d;
      if (!withGps || !d) return null;
      return Promise.all([
        fetch('/locode-coords.json').then(function (r) { return r.json(); }),
        typeof fetchAllNodes === 'function'
          ? fetchAllNodes('&sortBy=lastSeen', { ttl: 60000 })
          : Promise.resolve(null),
      ]).then(function (both) {
        _index = buildIndex(both[0] || {});
        var nodes = both[1] && both[1].nodes ? both[1].nodes : (Array.isArray(both[1]) ? both[1] : []);
        var map = {};
        for (var i = 0; i < nodes.length; i++) {
          var n = nodes[i];
          if (typeof n.lat === 'number' && typeof n.lon === 'number' && (n.lat || n.lon)) {
            map[n.public_key] = [n.lat, n.lon];
          }
        }
        _coords = map;
        return null;
      });
    }).catch(function (e) {
      // Never rejects: prime() is awaited beside the page's own fetch, so a
      // rejection would render "Failed to load scope audit" for a page that
      // loaded fine, and on the caller's error path it would go unhandled.
      // Whatever failed, the column degrades to what it still has.
      console.warn('[locode-column] a data source did not load, Location degrades:', e && e.message);
      return null;
    }).then(function () { return _data; });
  }

  function headerHtml() {
    return '<th data-sort-key="place" title="Derived from the CC-LOC prefix in the node name, via locode.json. Empty means the name does not carry one.">Location</th>';
  }

  window.LocodeColumn = {
    prime: prime,
    headerHtml: headerHtml,
    cellHtml: cellHtml,
    // Exposed for tests. placeOf is pure; the other two read module state that
    // prime() fills, and the tests set it through prime with injected loaders.
    placeOf: placeOf,
    nearestPlace: nearestPlace,
    MAX_KM: MAX_KM,
    _setForTest: function (data, coordsByCc, nodeCoords) {
      _data = data; _index = coordsByCc ? buildIndex(coordsByCc) : null; _coords = nodeCoords || null;
    },
  };
})();
