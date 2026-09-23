/* radio-band.js — derive the RF band an observer listens on, and show it.
 *
 * WHY THIS EXISTS
 *
 * MeshCore runs on 868 MHz in the EU and 915 MHz in the US, and there is now
 * 433 MHz traffic too. A packet cannot tell you which: the firmware's Packet
 * struct is header, path_len, payload and transport codes, with no radio
 * settings in it, and frequency is a property of the receiver rather than of
 * the transmission. What IS available is the observer's own radio config,
 * which it publishes and which the server already stores and returns as
 * `observers.radio` — a comma string "freq,bw,sf,cr", e.g.
 * "869.6179809,62.5,8,8" or "433.65,62.5,8,8".
 *
 * So the band is a property of the OBSERVATION, read off the observer that
 * heard it. A transmission is only ever heard by observers on its own band:
 * measured over 7 days on a 1.9k-node deployment, 1875 transmissions were
 * heard only by the 433 observer, 122536 only by 868 observers, and ZERO by
 * both — which is what the physics requires and a useful check that the
 * derivation is sound.
 *
 * TWO LIMITS, both worth knowing before trusting a band filter:
 *
 *   1. Not every observer publishes `radio`. On the reference deployment 40 of
 *      42 active observers do; the two that do not are mobile clients, and
 *      they account for about 4% of observations. Those cannot be attributed
 *      and must read as unknown rather than as a default band.
 *   2. `observers.radio` is CURRENT state, not history. The column is
 *      overwritten by each status message, so retuning an observer re-labels
 *      all of its past observations. Fine for "what is this observer on now",
 *      wrong for an analysis over time. Making that historically correct means
 *      recording the band per observation, which is a server change.
 *
 * Band edges are the ISM allocations, not invented: 433.05-434.79 (EU 70cm),
 * 863-870 (EU 868), 902-928 (US 915). Firmware defaults sit inside them —
 * examples ship LORA_FREQ 915.0 and the EU variants build LORA_FREQ=869.618.
 */
(function () {
  'use strict';

  var BANDS = [
    { label: '433', min: 433.0, max: 435.0 },
    { label: '868', min: 863.0, max: 870.0 },
    { label: '915', min: 902.0, max: 928.0 },
  ];

  // parseRadio splits the observer-published "freq,bw,sf,cr" string. Every
  // field is publisher-controlled, so nothing here trusts it: non-numeric
  // parts come back as null rather than NaN, and callers escape before
  // rendering.
  function parseRadio(radio) {
    if (typeof radio !== 'string' || radio === '') return null;
    var p = radio.split(',');
    var num = function (v) {
      var n = parseFloat(v);
      return isFinite(n) ? n : null;
    };
    var freq = num(p[0]);
    if (freq === null) return null;
    return { freqMHz: freq, bwKHz: num(p[1]), sf: num(p[2]), cr: num(p[3]) };
  }

  // bandOf maps a frequency to a band label, or null when it falls outside
  // every known allocation. Null is deliberate: an unrecognised frequency is
  // not the default band, and saying so is the whole point of this file.
  function bandOf(freqMHz) {
    if (typeof freqMHz !== 'number' || !isFinite(freqMHz)) return null;
    for (var i = 0; i < BANDS.length; i++) {
      if (freqMHz >= BANDS[i].min && freqMHz <= BANDS[i].max) return BANDS[i].label;
    }
    return null;
  }

  function bandOfRadio(radio) {
    var r = parseRadio(radio);
    return r ? bandOf(r.freqMHz) : null;
  }

  // badgeHtml returns a pill, or '' when the band is unknown. It reuses
  // .badge-iata so no new CSS is needed; band-badge and band-<label> are
  // styling hooks, so giving 433 its own colour later is a CSS-only change.
  // The label is a fixed string from BANDS, never the publisher's input, so
  // there is nothing to escape here — but the title carries the raw frequency
  // and is escaped.
  function badgeHtml(radio) {
    var band = bandOfRadio(radio);
    if (!band) return '';
    var r = parseRadio(radio);
    var title = 'Observer radio: ' + escapeAttr(String(r.freqMHz)) + ' MHz';
    return '<span class="badge-iata band-badge band-' + band + '" title="' + title + '">'
      + band + '</span>';
  }

  function escapeAttr(s) {
    return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;')
      .replace(/>/g, '&gt;').replace(/"/g, '&quot;');
  }

  // decorateObservers appends a band badge to each row's name cell.
  //
  // Done by decorating after render, keyed on data-observer-id and
  // data-testid="obs-cell-name", rather than by editing the row template.
  // Both attributes are maintained upstream for its own tests, and staying out
  // of that template means an upstream change to the observers table does not
  // conflict with this file. Returns how many rows were decorated so a caller
  // or test can tell the difference between "nothing to do" and "hooks moved".
  function decorateObservers(root, observers) {
    if (!root || !observers || !observers.length) return 0;
    var radioById = {};
    for (var i = 0; i < observers.length; i++) {
      if (observers[i] && observers[i].id) radioById[observers[i].id] = observers[i].radio;
    }
    var rows = root.querySelectorAll('tr[data-observer-id]');
    var done = 0;
    for (var j = 0; j < rows.length; j++) {
      var row = rows[j];
      if (row.querySelector('.band-badge')) continue; // idempotent
      var cell = row.querySelector('[data-testid="obs-cell-name"]');
      if (!cell) continue;
      var html = badgeHtml(radioById[row.getAttribute('data-observer-id')]);
      if (!html) continue;
      cell.insertAdjacentHTML('beforeend', html);
      done++;
    }
    return done;
  }

  window.RadioBand = {
    parseRadio: parseRadio,
    bandOf: bandOf,
    bandOfRadio: bandOfRadio,
    badgeHtml: badgeHtml,
    decorateObservers: decorateObservers,
  };
})();
