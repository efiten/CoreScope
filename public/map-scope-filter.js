'use strict';
/* global window */

// #1862 — client-side region-scope filtering for repeaters on the map.
//
// The bulk /api/nodes payload already carries `transported_scopes` per relaying
// node (#1751), so filtering by "which regions does this repeater forward"
// needs no extra request and no server-side filter params.
//
// Kept in its own file rather than inlined into map.js: upstream PR #1852
// rewrites map.js for the same feature, so isolating the logic here keeps the
// re-apply cost after that merge down to the wiring in map.js.

// Roles that can forward transport-scoped traffic. Other roles are never
// hidden by these controls — a companion has no scopes to speak of, and
// filtering it out would silently empty the map.
const SCOPE_ROLES = { repeater: true, room: true };

function scopesOf(node) {
  const s = node && node.transported_scopes;
  return Array.isArray(s) ? s : [];
}

// collectScopes returns every distinct scope across the given nodes, sorted,
// so the region dropdown lists exactly what has actually been observed.
function collectScopes(nodes) {
  const set = {};
  for (const n of nodes || []) {
    for (const s of scopesOf(n)) if (s) set[s] = true;
  }
  return Object.keys(set).sort();
}

// nodePassesScopeFilters gates a node against the region dropdown and the
// has-scope control. filters.scopeRegion is 'all' or a scope name;
// filters.hasScope is 'all' | 'yes' | 'no'.
function nodePassesScopeFilters(node, filters) {
  if (!SCOPE_ROLES[(node && node.role) || 'companion']) return true;
  const scopes = scopesOf(node);
  if (filters.hasScope === 'yes' && scopes.length === 0) return false;
  if (filters.hasScope === 'no' && scopes.length > 0) return false;
  if (filters.scopeRegion !== 'all' && scopes.indexOf(filters.scopeRegion) === -1) return false;
  return true;
}

window.collectScopes = collectScopes;
window.nodePassesScopeFilters = nodePassesScopeFilters;
