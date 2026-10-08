# Companion Linking F2 (server) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give `cmd/server` the HTTP side of sub-project F on top of the F1 store: device tokens (issue, bearer auth with a server-enforced scope, revoke), the companion challenge/link/list/unlink routes with Ed25519 proof, transfer audit and mail, the server-side merge into `meshcore-my-nodes`, `rx-coverage?mine=1`, CORS for the bearer routes, the `clientRxRequireLinkedCompanion` client-config flag, and OpenAPI entries.

**Architecture:** Bearer handling lives next to the cookie handling in `auth_session.go`: `withUser` judges a request with an `Authorization: Bearer` header on that token alone (device sessions only, scope by route, no CSRF), and the cookie path now refuses device sessions. `/api/auth/me` moves under `withUser`; `/api/auth/logout` checks the bearer header before its origin check. The login credential check is factored out of `handleLogin` into `verifyLogin`, which `handleDeviceToken` reuses (same constant-cost path, same rate-limit buckets, same audit action). Companion routes live in a new `companion_handlers.go`; the settings merge in `companion_mynodes.go` is an optimistic read-modify-write over the existing `GetSettings`/`PutSettings` revision check. Signature verification is a new generic `sigvalidate.VerifyMessage` (the package only had the advert-specific `ValidateAdvert`). A CORS preflight route (a `MatcherFunc`, not a path) makes OPTIONS reach `corsMiddleware` for the bearer routes, because gorilla/mux does not run middleware on a method mismatch.

**Tech Stack:** Go, gorilla/mux v1.8.1, stdlib `crypto/ed25519`, `net/http/httptest`; `internal/users` (F1 API), `internal/sigvalidate`, `internal/mailer` (`Fake` in tests).

**Spec:** `docs/specs/2026-10-08-companion-linking-design.md`, sections *API* (all), *CORS*, *Linked-only ingest* (config flag and `/api/config/client` only), *Error handling*, *Testing → cmd/server* and */api/config/client*. Store API: `docs/plans/2026-10-08-companion-linking-f1-store.md`.

---

## API for F3 (ingestor, account page, coverage page, CoreDrive RX)

| Item | Shape |
|---|---|
| `POST /api/auth/device-token` | Body `{email, password, deviceName}` → `200 {token, expiresAt, user: {id, displayName}}` (`expiresAt` RFC 3339). `401 {error}` same message as login; `429` with `Retry-After` (buckets shared with `/api/auth/login`). No Origin check. |
| Bearer use | `Authorization: Bearer <token>`. Allowed: `GET /api/auth/me`, `POST /api/auth/logout`, `GET/PUT/DELETE /api/account/settings`, `/api/account/companions*`. Elsewhere under `withUser`/`withAdmin`: `403`. Unknown, expired, revoked or web token: `401 {error: "invalid or expired token"}` (RX clears the token). Sliding 90 days. `GET /api/auth/me` with a bearer answers `csrfToken: ""`. |
| `POST /api/auth/logout` (bearer) | Revokes that device token, `200 {ok: true}`. |
| `GET /api/account/sessions` | Each item gains `kind` (`"web"` \| `"device"`) and `label` (device name, `""` for web). Same in admin user detail `sessions`. |
| `POST /api/account/companions/challenge` | `{pubkey}` → `200 {challenge, expiresAt}`; `challenge` is 64 lowercase hex. `400` bad pubkey, `429`. |
| Signed message | UTF-8 `"corescope-link:" + <host of userManagement.publicBaseUrl> + ":" + <challenge as sent>`; signature = 64-byte Ed25519 as hex. |
| `POST /api/account/companions` | `{pubkey, challenge, signature, name}` → `200 {pubkey, name, linkedAt, myNodes}`, `myNodes` ∈ `"added"`, `"present"`, `"full"`, `"failed"` (`"full"` = not added because the document would exceed 256 KiB; `"failed"` = not added because the merge failed for another reason, e.g. a stored list that does not decode or a settings write error; the link stands in both cases). `400` bad pubkey/signature, `410` challenge missing/expired/used/mismatched, `429`. |
| `GET /api/account/companions` | `[{pubkey, name, linkedAt, lastSeenAt}]`, newest link first; `lastSeenAt` is `client_receptions.rx_at` as stored, or `null`. Always an array. |
| `DELETE /api/account/companions/{pubkey}` | `204`; `400` bad pubkey; `404` not linked to the caller. |
| Admin `GET /api/admin/users/{id}` | Gains `companions: [{pubkey, name, linkedAt, lastSeenAt}]`. |
| Audit actions | `companion.link`, `companion.unlink`, `companion.transfer` (two rows, targets: new and previous owner; detail `{pubkey, from, to}`); device login is `user.login` with detail `{via: "device", device: <label>}`. |
| `GET /api/rx-coverage?mine=1` | Same GeoJSON as without; filtered to the caller's linked companions (empty collection when none). Cookie session only: `401` without one, `403` with a bearer header, `404` when user management is off. |
| Config | `clientRxCoverage.requireLinkedCompanion` (bool, default false). |
| `/api/config/client` | `clientRxRequireLinkedCompanion: true` only when that is set **and** user management is on; otherwise the field is absent. |
| CORS | For `corsAllowedOrigins`, on `/api/auth/device-token` and the bearer routes: `Access-Control-Allow-Methods: GET, HEAD, POST, PUT, DELETE, OPTIONS`, `Access-Control-Allow-Headers: Authorization, Content-Type`, never `Allow-Credentials`. |

## Design constraints the implementer must not relax

- **A device token never passes as a cookie session, and a web token never passes as a bearer.** A web token as a bearer would skip the CSRF check. Both directions have a test.
- **A bearer header is judged alone.** With `Authorization: Bearer` present, the cookie is not consulted.
- **The challenge is consumed before anything else can fail** (also for a malformed pubkey or signature), so a refused attempt never leaves a usable challenge.
- **Tokens, challenges and signatures are never logged.** Log lines name user ids and session ids only.
- **The link never fails because of `meshcore-my-nodes`.** A document at the size cap is reported as `myNodes: "full"`; any other merge error is logged and reported as `myNodes: "failed"`. Neither turns the link into an error.
- `/api/rx-coverage` is in `openapi_known_gaps.json`; do **not** add it to `routeDescriptions` here (the ratchet would then demand removing the gap entry; that backfill is not part of F).

## File Structure

| File | Change |
|---|---|
| `internal/sigvalidate/sigvalidate.go` | Add `VerifyMessage`. |
| `internal/sigvalidate/verify_message_test.go` | New. |
| `cmd/server/auth_session.go` | Cookie path refuses device sessions; `deviceScopeRX`, `bearerScopeFor`, `bearerToken`, `bearerUser`, `writeBearerFail`, `bearerCORSPath`; bearer branch in `withUser`, 403 in `withAdmin`. |
| `cmd/server/auth_types.go` | `sessionJSON` gains `kind`/`label`; device-token and companion request/response types; `adminUserDetailJSON.Companions`. |
| `cmd/server/auth_handlers.go` | `verifyLogin` (from `handleLogin`), `handleDeviceToken`; `handleMe` becomes an `authedHandler`; `handleLogout` handles bearer. |
| `cmd/server/auth_routes.go` | New routes, CORS preflight matcher; `/api/auth/me` under `withUser`; logout without `requireOrigin`. |
| `cmd/server/auth_service.go` | `companion` rate limiter (+ `gc`); startup warning for `requireLinkedCompanion` without user management. |
| `cmd/server/companion_mynodes.go` | New: `addToMyNodes`, `marshalNoEscape`. |
| `cmd/server/companion_handlers.go` | New: challenge, link, list, delete, last-seen, transfer audit and mail. |
| `cmd/server/admin_users_handlers.go` | Detail gains `companions`. |
| `cmd/server/rx_dashboard.go` | `queryCoverageFiltered` gains `rxIn`; `?mine=1` in `handleRxCoverage`; `myCompanionPubkeys`. |
| `cmd/server/rx_dashboard_test.go` | Three call sites gain the `nil` argument. |
| `cmd/server/cors.go` | Bearer routes get the write methods and headers. |
| `cmd/server/config.go`, `types.go`, `routes.go` | `RequireLinkedCompanion`, `ClientRxRequireLinkedCompanionSet`, `clientRxRequireLinkedCompanion`. |
| `cmd/server/openapi.go` | Entries for the new routes; updated descriptions for logout, me, sessions, admin detail. |
| `config.example.json` | Comment for `clientRxCoverage.requireLinkedCompanion`. |
| `cmd/server/*_test.go` | New: `device_session_test.go`, `device_token_test.go`, `bearer_auth_test.go`, `client_config_linked_test.go`, `companion_mynodes_test.go`, `companion_handlers_test.go`, `companion_transfer_test.go`, `admin_companions_test.go`, `rx_coverage_mine_test.go`, `cors_bearer_test.go`, `companion_off_test.go`. |

`cmd/server` commands run from `C:\dev\corescope\CoreScope\cmd\server`, `internal/sigvalidate` commands from `C:\dev\corescope\CoreScope\internal\sigvalidate`, git commands from the repo root. After every code step run `gofmt -w` on the touched files (struct literals realign when a longer field is added).

---

### Task 1: `sigvalidate.VerifyMessage`

**Files:**
- Create: `internal/sigvalidate/verify_message_test.go`
- Modify: `internal/sigvalidate/sigvalidate.go`

- [ ] **Step 1: Write the failing test**

Create `internal/sigvalidate/verify_message_test.go`:

```go
package sigvalidate

import (
	"bytes"
	"crypto/ed25519"
	"testing"
)

func TestVerifyMessage(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, 32))
	pub := priv.Public().(ed25519.PublicKey)
	msg := []byte("corescope-link:scope.example.org:00ff")
	sig := ed25519.Sign(priv, msg)

	if ok, err := VerifyMessage(pub, sig, msg); err != nil || !ok {
		t.Fatalf("valid signature: ok=%v err=%v", ok, err)
	}
	if ok, err := VerifyMessage(pub, sig, []byte("corescope-link:other.example.org:00ff")); err != nil || ok {
		t.Fatalf("other message: ok=%v err=%v", ok, err)
	}
	if _, err := VerifyMessage(pub[:31], sig, msg); err == nil {
		t.Fatal("31-byte pubkey accepted")
	}
	if _, err := VerifyMessage(pub, sig[:63], msg); err == nil {
		t.Fatal("63-byte signature accepted")
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd internal/sigvalidate && go test -run TestVerifyMessage .`
Expected: FAIL, build error `undefined: VerifyMessage`.

- [ ] **Step 3: Implement**

Append to `internal/sigvalidate/sigvalidate.go`:

```go
// VerifyMessage verifies an Ed25519 signature over an arbitrary message.
// pubKey must be 32 bytes, signature 64 bytes. Companion linking uses it:
// a companion signs a server challenge with its identity key.
func VerifyMessage(pubKey, signature, message []byte) (bool, error) {
	if len(pubKey) != 32 {
		return false, fmt.Errorf("invalid pubkey length: %d", len(pubKey))
	}
	if len(signature) != 64 {
		return false, fmt.Errorf("invalid signature length: %d", len(signature))
	}
	return ed25519.Verify(ed25519.PublicKey(pubKey), message, signature), nil
}
```

- [ ] **Step 4: Run the package tests and the consumers' builds**

Run: `cd internal/sigvalidate && go test .`
Expected: PASS.
Run: `cd cmd/server && go build ./...` and `cd cmd/ingestor && go build ./...`
Expected: no output.

- [ ] **Step 5: Commit**

```bash
git add internal/sigvalidate/sigvalidate.go internal/sigvalidate/verify_message_test.go
git commit -F - <<'EOF'
feat(sigvalidate): verify an Ed25519 signature over any message

ValidateAdvert is advert-specific; companion linking needs to verify a
signed challenge string.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 2: Sessions carry kind and label; the cookie path refuses device sessions

**Files:**
- Create: `cmd/server/device_session_test.go`
- Modify: `cmd/server/auth_session.go`, `cmd/server/auth_types.go`, `cmd/server/openapi.go`

- [ ] **Step 1: Write the failing tests**

Create `cmd/server/device_session_test.go`:

```go
package main

import (
	"net/http"
	"testing"
)

// LookupSession returns a session of any kind, so a device token sent as
// the session cookie must be refused by the cookie path.
func TestDeviceTokenIsNotACookieSession(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	raw, _, err := f.st.CreateDeviceSession(alice.me.ID, "Pixel", []string{"rx"}, "")
	if err != nil {
		t.Fatal(err)
	}
	forged := &client{cookie: &http.Cookie{Name: sessionCookieName, Value: raw}}
	for _, p := range []string{"/api/auth/me", "/api/account/sessions", "/api/account/settings"} {
		if w := f.do("GET", p, nil, as(forged)); w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s with a device token as cookie = %d, want 401", p, w.Code)
		}
	}
}

func TestSessionsListKindAndLabel(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	if _, _, err := f.st.CreateDeviceSession(alice.me.ID, "Pixel 8", []string{"rx"}, "CoreDriveRX/1.0"); err != nil {
		t.Fatal(err)
	}
	w := f.do("GET", "/api/account/sessions", nil, as(alice))
	expectStatus(t, w, http.StatusOK)
	byKind := map[string]sessionJSON{}
	for _, s := range decode[[]sessionJSON](t, w) {
		byKind[s.Kind] = s
	}
	if web, ok := byKind["web"]; !ok || web.Label != "" || !web.Current {
		t.Fatalf("web session = %+v (present %v)", web, ok)
	}
	if dev, ok := byKind["device"]; !ok || dev.Label != "Pixel 8" || dev.Current || dev.UserAgent != "CoreDriveRX/1.0" {
		t.Fatalf("device session = %+v (present %v)", dev, ok)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd cmd/server && go test -run 'TestDeviceTokenIsNotACookieSession|TestSessionsListKindAndLabel' .`
Expected: FAIL, build error `s.Kind undefined (type sessionJSON has no field or method Kind)`. (With only Step 3b applied, `TestDeviceTokenIsNotACookieSession` fails with `= 200, want 401`.)

- [ ] **Step 3: Implement**

a) In `cmd/server/auth_types.go`, replace `sessionJSON` and `sessionToJSON` with:

```go
type sessionJSON struct {
	ID         int64  `json:"id"`
	Kind       string `json:"kind"`  // "web" or "device"
	Label      string `json:"label"` // device name; "" for web sessions
	CreatedAt  string `json:"createdAt"`
	LastSeenAt string `json:"lastSeenAt"`
	ExpiresAt  string `json:"expiresAt"`
	UserAgent  string `json:"userAgent"`
	Current    bool   `json:"current"`
}
```

```go
func sessionToJSON(s users.Session, currentID int64) sessionJSON {
	return sessionJSON{ID: s.ID, Kind: s.Kind, Label: s.Label, CreatedAt: rfc3339(s.CreatedAt), LastSeenAt: rfc3339(s.LastSeenAt),
		ExpiresAt: rfc3339(s.ExpiresAt), UserAgent: s.UserAgent, Current: s.ID == currentID}
}
```

b) In `cmd/server/auth_session.go`, in `currentUser`, replace

```go
	sess, err := a.st.LookupSession(c.Value)
	if err != nil {
		return nil, nil
	}
```

with

```go
	sess, err := a.st.LookupSession(c.Value)
	// A device token is a bearer credential only: sent as the cookie it
	// must not pass as a browser session.
	if err != nil || sess.Kind != users.SessionKindWeb {
		return nil, nil
	}
```

c) In `cmd/server/openapi.go`, replace the `"GET /api/account/sessions"` entry with:

```go
		"GET /api/account/sessions":                        {Summary: "List own sessions", Description: "[{id, kind, label, createdAt, lastSeenAt, expiresAt, userAgent, current}]. kind is web (a browser) or device (a CoreDrive RX device token); label is the device name, empty for web sessions. DELETE /api/account/sessions/{id} revokes either kind.", Tag: "users", Session: true},
```

- [ ] **Step 4: Run the tests**

Run: `cd cmd/server && go test -run 'TestDeviceTokenIsNotACookieSession|TestSessionsListKindAndLabel|TestSession|TestAdmin|TestOpenAPI' .`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/server/auth_session.go cmd/server/auth_types.go cmd/server/openapi.go cmd/server/device_session_test.go
git commit -F - <<'EOF'
feat(server): sessions list kind and label; cookies refuse device tokens

GET /api/account/sessions (and the admin user detail) report kind and
label. The cookie path only accepts web sessions, so a device token
sent as cs_session is not a browser session.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 3: `POST /api/auth/device-token`

**Files:**
- Create: `cmd/server/device_token_test.go`
- Modify: `cmd/server/auth_handlers.go`, `cmd/server/auth_types.go`, `cmd/server/auth_session.go`, `cmd/server/auth_routes.go`, `cmd/server/openapi.go`

- [ ] **Step 1: Write the failing tests**

Create `cmd/server/device_token_test.go`:

```go
package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/users"
)

// rxOrigin is a CoreDrive RX deployment on another origin than testBase.
const rxOrigin = "https://rx.example.net"

func bearer(tok string) reqMod { return header("Authorization", "Bearer "+tok) }

// deviceToken logs in like CoreDrive RX: from a foreign origin, no cookie.
func (f *authFixture) deviceToken(t *testing.T, email, password, name string) deviceTokenResponse {
	t.Helper()
	w := f.do("POST", "/api/auth/device-token", deviceTokenRequest{Email: email, Password: password, DeviceName: name},
		header("Origin", rxOrigin))
	expectStatus(t, w, http.StatusOK)
	return decode[deviceTokenResponse](t, w)
}

func TestDeviceTokenIssue(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	got := f.deviceToken(t, "alice@example.org", pw, "  Pixel\x07 8  ")
	if got.Token == "" || got.User.ID != alice.me.ID || got.User.DisplayName != "Alice" {
		t.Fatalf("device-token response = %+v", got)
	}
	exp, err := time.Parse(time.RFC3339, got.ExpiresAt)
	if err != nil || exp.Before(time.Now().Add(users.DeviceSessionTTL-time.Hour)) {
		t.Fatalf("expiresAt = %q, %v", got.ExpiresAt, err)
	}
	sess, err := f.st.LookupSession(got.Token)
	if err != nil || sess.Kind != users.SessionKindDevice || sess.Label != "Pixel 8" ||
		!sess.HasScope(deviceScopeRX) || sess.HasScope("admin") {
		t.Fatalf("stored device session = %+v, %v", sess, err)
	}
	if w := f.serve("GET", "/api/auth/device-token", nil); w.Code == http.StatusOK {
		t.Fatal("GET answered 200")
	}
	entries, err := f.st.AuditFor(alice.me.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Action == "user.login" && e.Detail["via"] == "device" && e.Detail["device"] == "Pixel 8" {
			return
		}
	}
	t.Fatalf("no device login audit row in %+v", entries)
}

func TestDeviceTokenFailuresMatchLogin(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	for _, req := range []deviceTokenRequest{
		{Email: "alice@example.org", Password: "wrong password here", DeviceName: "x"},
		{Email: "nobody@example.org", Password: pw, DeviceName: "x"},
		{Email: "not an address", Password: pw, DeviceName: "x"},
	} {
		w := f.do("POST", "/api/auth/device-token", req)
		expectStatus(t, w, http.StatusUnauthorized)
		if !strings.Contains(w.Body.String(), msgBadLogin) {
			t.Fatalf("%s: body %s, want the login message", req.Email, w.Body.String())
		}
	}
	entries, _ := f.st.AuditFor(alice.me.ID, 20)
	failed := 0
	for _, e := range entries {
		if e.Action == "user.login.failed" && e.Detail["reason"] == "wrong_password" {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("failed-login audit rows = %d, want 1: %+v", failed, entries)
	}
	list, _ := f.st.ListSessions(alice.me.ID)
	for _, s := range list {
		if s.Kind == users.SessionKindDevice {
			t.Fatalf("a refused login created a device session: %+v", s)
		}
	}
}

// Device-token attempts and browser logins draw from the same buckets, so
// the endpoint is no way around the login rate limit.
func TestDeviceTokenSharesLoginRateLimit(t *testing.T) {
	f := newAuthFixture(t)
	f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	for i := 0; i < 10; i++ {
		f.do("POST", "/api/auth/login", loginRequest{Email: "alice@example.org", Password: "wrong password here"})
	}
	w := f.do("POST", "/api/auth/device-token", deviceTokenRequest{Email: "alice@example.org", Password: pw, DeviceName: "x"})
	expectStatus(t, w, http.StatusTooManyRequests)
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After")
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd cmd/server && go test -run 'TestDeviceToken' .`
Expected: FAIL, build error `undefined: deviceTokenRequest`, `undefined: deviceTokenResponse`, `undefined: deviceScopeRX`.

- [ ] **Step 3: Add the types**

In `cmd/server/auth_types.go`, after `loginRequest`, add:

```go
type deviceTokenRequest struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	DeviceName string `json:"deviceName"`
}

type deviceTokenUser struct {
	ID          int64  `json:"id"`
	DisplayName string `json:"displayName"`
}

type deviceTokenResponse struct {
	Token     string          `json:"token"`
	ExpiresAt string          `json:"expiresAt"`
	User      deviceTokenUser `json:"user"`
}
```

- [ ] **Step 4: Add the scope constant**

In `cmd/server/auth_session.go`, after the `sessionCookieName`/`csrfHeader` const block, add:

```go
// deviceScopeRX is the scope of CoreDrive RX device tokens: the routes
// bearerScopeFor lists.
const deviceScopeRX = "rx"
```

- [ ] **Step 5: Factor out the credential check and add the handler**

In `cmd/server/auth_handlers.go`, replace `handleLogin` with:

```go
// verifyLogin is the credential check shared by /api/auth/login and
// /api/auth/device-token: rate limits per IP and per address, the
// constant-cost answer for unknown addresses, the failure audit row and
// the config-admin promotion. It returns the user, or nil after writing
// the answer.
func (a *authService) verifyLogin(w http.ResponseWriter, r *http.Request, rawEmail, password string) *users.User {
	email, emailErr := users.NormalizeEmail(rawEmail)
	key := "email:" + email
	if emailErr != nil {
		key = "email:invalid"
	}
	if !a.allow(w, r, a.login, key) {
		return nil
	}
	var u *users.User
	if emailErr == nil {
		u, _ = a.st.GetByEmail(email)
	}
	if u == nil {
		users.BurnPasswordCheck(password)
		writeError(w, http.StatusUnauthorized, msgBadLogin)
		return nil
	}
	ok, err := users.VerifyPassword(u.PasswordHash, password)
	if err != nil || !ok || u.Status != users.StatusActive {
		writeError(w, http.StatusUnauthorized, msgBadLogin)
		// In the background after the answer is decided, so the write never
		// changes response timing.
		a.auditAsync(nil, "user.login.failed", idPtr(u.ID), map[string]string{"reason": loginFailReason(err == nil && ok, u.Status)})
		return nil
	}
	// Config wins: an address in adminEmails is always admin.
	if a.isConfigAdmin(u.Email) && u.Role != users.RoleAdmin {
		if err := a.st.SetRole(u.ID, users.RoleAdmin); err == nil {
			u.Role = users.RoleAdmin
			a.audit(nil, "user.role.config", idPtr(u.ID), map[string]string{"role": "admin"})
		}
	}
	return u
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	a := s.auth
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	u := a.verifyLogin(w, r, req.Email, req.Password)
	if u == nil {
		return
	}
	if a.startSession(w, r, u) {
		a.auditAsync(nil, "user.login", idPtr(u.ID), nil)
	}
}

// handleDeviceToken issues a CoreDrive RX device token. No Origin check:
// the token is returned in the body and nothing is stored in a browser,
// so login CSRF does not apply, and RX may run on another origin.
func (s *Server) handleDeviceToken(w http.ResponseWriter, r *http.Request) {
	a := s.auth
	var req deviceTokenRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	u := a.verifyLogin(w, r, req.Email, req.Password)
	if u == nil {
		return
	}
	raw, sess, err := a.st.CreateDeviceSession(u.ID, req.DeviceName, []string{deviceScopeRX}, r.UserAgent())
	if err != nil {
		log.Printf("[users] device token for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	_ = a.st.TouchLogin(u.ID)
	writeJSON(w, deviceTokenResponse{Token: raw, ExpiresAt: rfc3339(sess.ExpiresAt),
		User: deviceTokenUser{ID: u.ID, DisplayName: u.DisplayName}})
	a.auditAsync(nil, "user.login", idPtr(u.ID), map[string]string{"via": "device", "device": sess.Label})
}
```

- [ ] **Step 6: Register the route and document it**

In `cmd/server/auth_routes.go`, after the `/api/auth/login` line, add:

```go
	r.HandleFunc("/api/auth/device-token", s.handleDeviceToken).Methods("POST")
```

In `cmd/server/openapi.go`, after the `"POST /api/auth/login"` entry, add:

```go
		"POST /api/auth/device-token":                      {Summary: "Issue a device token (CoreDrive RX)", Description: "Body {email, password, deviceName}. 200 {token, expiresAt, user: {id, displayName}}. The token is a bearer credential (Authorization: Bearer <token>), valid 90 days and extended on use, limited to /api/auth/me, /api/auth/logout, /api/account/settings and /api/account/companions*; other account routes answer 403 to it. deviceName is cleaned (control characters removed, trimmed, at most 64 characters) and shown under Devices. Same 401 answer, rate limits (shared with /api/auth/login) and user.login audit row (detail via=device) as /api/auth/login. No Origin check: nothing is stored in the browser.", Tag: "users"},
```

- [ ] **Step 7: Run the tests**

Run: `cd cmd/server && go test -run 'TestDeviceToken|Login|TestOpenAPI' .`
Expected: PASS (the new tests and every existing login test, unchanged by the refactor).

- [ ] **Step 8: Commit**

```bash
git add cmd/server/auth_handlers.go cmd/server/auth_types.go cmd/server/auth_session.go cmd/server/auth_routes.go cmd/server/openapi.go cmd/server/device_token_test.go
git commit -F - <<'EOF'
feat(server): issue CoreDrive RX device tokens

POST /api/auth/device-token logs in with email and password and returns
a scoped bearer token (90 days, sliding). It shares the login's
credential check, constant-cost failure, rate-limit buckets and audit
row through the new verifyLogin.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 4: Bearer authentication and scope

**Files:**
- Create: `cmd/server/bearer_auth_test.go`
- Modify: `cmd/server/auth_session.go`, `cmd/server/auth_handlers.go`, `cmd/server/auth_routes.go`, `cmd/server/openapi.go`

- [ ] **Step 1: Write the failing tests**

Create `cmd/server/bearer_auth_test.go`:

```go
package main

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/meshcore-analyzer/users"
)

func TestBearerScopedRoutes(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	tok := f.deviceToken(t, "alice@example.org", pw, "Pixel").Token
	foreign := header("Origin", rxOrigin)

	w := f.do("GET", "/api/auth/me", nil, bearer(tok))
	expectStatus(t, w, http.StatusOK)
	if me := decode[meResponse](t, w); me.ID != alice.me.ID || me.CSRFToken != "" {
		t.Fatalf("me via bearer = %+v", me)
	}
	// A write from another origin without any CSRF header: allowed for a bearer.
	w = f.do("PUT", "/api/account/settings", settingsPutRequest{Doc: &settingsDoc{V: 1, Keys: map[string]string{"meshcore-theme": "dark"}}},
		bearer(tok), foreign)
	expectStatus(t, w, http.StatusOK)
	expectStatus(t, f.do("GET", "/api/account/settings", nil, bearer(tok)), http.StatusOK)

	for _, c := range []struct{ method, path string }{
		{"GET", "/api/account/sessions"},
		{"PATCH", "/api/account"},
		{"POST", "/api/account/password"},
		{"DELETE", "/api/account"},
		{"GET", "/api/admin/users"},
	} {
		if w := f.do(c.method, c.path, nil, bearer(tok), foreign); w.Code != http.StatusForbidden {
			t.Errorf("%s %s with a device token = %d, want 403", c.method, c.path, w.Code)
		}
	}
	if _, err := f.st.GetByID(alice.me.ID); err != nil {
		t.Fatalf("account touched by an out-of-scope request: %v", err)
	}
}

func TestBearerRejectsWebAndBadTokens(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	// alice.cookie.Value is a web session token; as a bearer it would skip CSRF.
	for _, tok := range []string{alice.cookie.Value, "nonsense", ""} {
		if w := f.do("GET", "/api/auth/me", nil, bearer(tok)); w.Code != http.StatusUnauthorized {
			t.Errorf("bearer %q = %d, want 401", tok, w.Code)
		}
	}
	// A bearer header is judged alone: a valid cookie next to it does not help.
	if w := f.do("GET", "/api/auth/me", nil, as(alice), bearer("nonsense")); w.Code != http.StatusUnauthorized {
		t.Fatalf("bad bearer + good cookie = %d, want 401", w.Code)
	}
}

func TestBearerLogoutRevokesDevice(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	tok := f.deviceToken(t, "alice@example.org", pw, "Pixel").Token

	expectStatus(t, f.do("POST", "/api/auth/logout", nil, bearer(tok), header("Origin", rxOrigin)), http.StatusOK)
	if _, err := f.st.LookupSession(tok); !errors.Is(err, users.ErrNotFound) {
		t.Fatalf("device token survived logout: %v", err)
	}
	expectStatus(t, f.do("GET", "/api/auth/me", nil, bearer(tok)), http.StatusUnauthorized)
	expectStatus(t, f.do("GET", "/api/auth/me", nil, as(alice)), http.StatusOK)
	// The cookie logout keeps its origin check.
	expectStatus(t, f.do("POST", "/api/auth/logout", nil, as(alice), header("Origin", rxOrigin)), http.StatusForbidden)
}

func TestBearerSlidingExpiry(t *testing.T) {
	f := newAuthFixture(t)
	f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	f.st.SetClock(func() time.Time { return time.Now().Add(-80 * 24 * time.Hour) })
	tok := f.deviceToken(t, "alice@example.org", pw, "Pixel").Token
	f.st.SetClock(time.Now)

	expectStatus(t, f.do("GET", "/api/auth/me", nil, bearer(tok)), http.StatusOK)
	sess, err := f.st.LookupSession(tok)
	if err != nil || sess.ExpiresAt.Before(time.Now().Add(users.DeviceSessionTTL-time.Hour)) {
		t.Fatalf("device token not extended on use: %+v, %v", sess, err)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd cmd/server && go test -run 'TestBearer' .`
Expected: FAIL: `TestBearerScopedRoutes` with `status = 401, want 200` (me ignores the bearer), `TestBearerLogoutRevokesDevice` with `status = 403, want 200`, `TestBearerSlidingExpiry` with `status = 401, want 200`.

- [ ] **Step 3: Bearer helpers and `withUser`/`withAdmin`**

In `cmd/server/auth_session.go`, after `deviceScopeRX`, add:

```go
// bearerScopeFor returns the scope a device token needs on path, or ""
// when no device token may use it (spec: /api/auth/me, /api/auth/logout,
// /api/account/companions*, /api/account/settings).
func bearerScopeFor(path string) string {
	switch {
	case path == "/api/auth/me", path == "/api/auth/logout", path == "/api/account/settings",
		path == "/api/account/companions", strings.HasPrefix(path, "/api/account/companions/"):
		return deviceScopeRX
	}
	return ""
}

// bearerCORSPath reports whether path takes cross-origin writes from an
// allowlisted origin: the device-token login and the bearer routes.
func bearerCORSPath(path string) bool {
	return path == "/api/auth/device-token" || bearerScopeFor(path) != ""
}

// bearerToken returns the token of an "Authorization: Bearer" header and
// whether the request carried such a header at all.
func bearerToken(r *http.Request) (string, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(h[len(prefix):]), true
}

// bearerUser resolves a device token for r. The code is 0 on success, 401
// for an unknown, expired or web token or an inactive account, and 403
// when the token's scope does not cover the route. A use more than a day
// after the last one extends the token by DeviceSessionTTL (sliding).
func (a *authService) bearerUser(r *http.Request, tok string) (*users.User, *users.Session, int) {
	if tok == "" {
		return nil, nil, http.StatusUnauthorized
	}
	sess, err := a.st.LookupSession(tok)
	// A web token as a bearer would skip the CSRF check: refused.
	if err != nil || sess.Kind != users.SessionKindDevice {
		return nil, nil, http.StatusUnauthorized
	}
	u, err := a.st.GetByID(sess.UserID)
	if err != nil || u.Status != users.StatusActive {
		return nil, nil, http.StatusUnauthorized
	}
	if need := bearerScopeFor(r.URL.Path); need == "" || !sess.HasScope(need) {
		return nil, nil, http.StatusForbidden
	}
	if time.Since(sess.LastSeenAt) > 24*time.Hour {
		_ = a.st.ExtendSession(sess.ID, users.DeviceSessionTTL)
	}
	return u, sess, 0
}

func writeBearerFail(w http.ResponseWriter, code int) {
	if code == http.StatusForbidden {
		writeError(w, http.StatusForbidden, "this token is not allowed on this route")
		return
	}
	writeError(w, http.StatusUnauthorized, "invalid or expired token")
}
```

Replace `withUser` with:

```go
// withUser requires a logged-in active user. A request with an
// Authorization: Bearer header is judged on that device token alone
// (scope check, no CSRF: browsers never attach it on their own).
// Otherwise the cookie session is used, and state-changing methods must
// pass the origin + CSRF-token check.
func (s *Server) withUser(h authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if tok, ok := bearerToken(r); ok {
			u, sess, code := s.auth.bearerUser(r, tok)
			if code != 0 {
				writeBearerFail(w, code)
				return
			}
			h(w, r, u, sess)
			return
		}
		u, sess := s.auth.currentUser(w, r)
		if u == nil {
			writeError(w, http.StatusUnauthorized, "not logged in")
			return
		}
		if !isSafeMethod(r.Method) && !s.auth.csrfOK(r, sess) {
			writeError(w, http.StatusForbidden, "CSRF check failed")
			return
		}
		h(w, r, u, sess)
	}
}
```

In `withAdmin`, insert as the first statement of the returned func:

```go
		if _, ok := bearerToken(r); ok {
			writeBearerFail(w, http.StatusForbidden)
			return
		}
```

- [ ] **Step 4: `me` and `logout`**

In `cmd/server/auth_handlers.go`, replace `handleLogout` and `handleMe` with:

```go
// handleLogout ends the caller's session: with a bearer header it revokes
// that device token, otherwise it ends the cookie session (origin-checked,
// as before).
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	a := s.auth
	if tok, ok := bearerToken(r); ok {
		_, sess, code := a.bearerUser(r, tok)
		if code != 0 {
			writeBearerFail(w, code)
			return
		}
		if err := a.st.DeleteSession(sess.UserID, sess.ID); err != nil && !errors.Is(err, users.ErrNotFound) {
			log.Printf("[users] revoke device session #%d: %v", sess.ID, err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, okResponse{OK: true})
		return
	}
	if !a.originOK(r) {
		writeError(w, http.StatusForbidden, "request origin not allowed")
		return
	}
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		_ = a.st.DeleteSessionByToken(c.Value)
	}
	a.clearSessionCookie(w)
	writeJSON(w, okResponse{OK: true})
}

// handleMe answers the caller; a device token gets no CSRF token (it never
// needs one).
func (s *Server) handleMe(w http.ResponseWriter, _ *http.Request, u *users.User, sess *users.Session) {
	me := meFrom(u, sess)
	if sess.Kind == users.SessionKindDevice {
		me.CSRFToken = ""
	}
	writeJSON(w, me)
}
```

In `cmd/server/auth_routes.go`, replace the logout and me lines with:

```go
	r.HandleFunc("/api/auth/logout", s.handleLogout).Methods("POST") // origin check inside (cookie path only)
	r.HandleFunc("/api/auth/me", s.withUser(s.handleMe)).Methods("GET")
```

- [ ] **Step 5: Document**

In `cmd/server/openapi.go`, replace the `"POST /api/auth/logout"` and `"GET /api/auth/me"` entries with:

```go
		"POST /api/auth/logout":                            {Summary: "Log out", Description: "With the cs_session cookie (Origin must be publicBaseUrl): ends that browser session and clears the cookie. With Authorization: Bearer <device token>: revokes that device token.", Tag: "users"},
		"GET /api/auth/me":                                 {Summary: "Current user", Description: "Returns the logged-in user and the CSRF token, or 401. Also accepts a device token (Authorization: Bearer); csrfToken is then empty.", Tag: "users", Session: true},
```

- [ ] **Step 6: Run the tests**

Run: `cd cmd/server && go test -run 'TestBearer|TestDeviceToken|Logout|TestMe|CSRF|TestAdmin|TestSettings|TestOpenAPI' .`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add cmd/server/auth_session.go cmd/server/auth_handlers.go cmd/server/auth_routes.go cmd/server/openapi.go cmd/server/bearer_auth_test.go
git commit -F - <<'EOF'
feat(server): bearer auth for device tokens with a route scope

withUser accepts Authorization: Bearer for device sessions only, limited
to me, logout, settings and companions (403 elsewhere, also on admin
routes), without CSRF. A web token is refused as a bearer. Logout with
a bearer revokes that device; use extends it by 90 days.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 5: `clientRxCoverage.requireLinkedCompanion` and the client-config flag

**Files:**
- Create: `cmd/server/client_config_linked_test.go`
- Modify: `cmd/server/config.go`, `cmd/server/types.go`, `cmd/server/routes.go`, `cmd/server/auth_service.go`, `config.example.json`

- [ ] **Step 1: Write the failing tests**

Create `cmd/server/client_config_linked_test.go`:

```go
package main

import (
	"bytes"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClientConfigRequireLinkedCompanion(t *testing.T) {
	srv, router := setupTestServer(t)
	get := func() string {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/api/config/client", nil))
		return w.Body.String()
	}
	if strings.Contains(get(), "clientRxRequireLinkedCompanion") {
		t.Fatal("field present by default")
	}
	srv.cfg.ClientRxCoverage = &ClientRxCoverageConfig{Enabled: true, RequireLinkedCompanion: true}
	if strings.Contains(get(), "clientRxRequireLinkedCompanion") {
		t.Fatal("field present without user management")
	}
	a, _ := newTestAuthService(t)
	srv.auth = a
	if !strings.Contains(get(), `"clientRxRequireLinkedCompanion":true`) {
		t.Fatalf("field missing with user management on: %s", get())
	}
	srv.cfg.ClientRxCoverage.RequireLinkedCompanion = false
	if strings.Contains(get(), "clientRxRequireLinkedCompanion") {
		t.Fatal("field present with the setting off")
	}
}

func TestRequireLinkedCompanionWarnsWithoutUserManagement(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	srv := &Server{cfg: &Config{ClientRxCoverage: &ClientRxCoverageConfig{Enabled: true, RequireLinkedCompanion: true}}}
	if err := srv.initUserManagement(filepath.Join(t.TempDir(), "meshcore.db")); err != nil || srv.auth != nil {
		t.Fatalf("initUserManagement: auth=%v err=%v", srv.auth, err)
	}
	if !strings.Contains(buf.String(), "requireLinkedCompanion") {
		t.Fatalf("no startup warning, log: %q", buf.String())
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd cmd/server && go test -run 'RequireLinkedCompanion' .`
Expected: FAIL, build error `unknown field RequireLinkedCompanion in struct literal of type ClientRxCoverageConfig`.

- [ ] **Step 3: Implement**

a) In `cmd/server/config.go`, replace `ClientRxCoverageConfig` with the following and add the method after `ClientRxCoverageEnabled`:

```go
// ClientRxCoverageConfig gates the opt-in mobile client-RX coverage feature.
type ClientRxCoverageConfig struct {
	Enabled bool `json:"enabled"`
	// RequireLinkedCompanion (companion linking): the ingestor drops client
	// messages from companions no user linked. Only with userManagement.enabled.
	RequireLinkedCompanion bool `json:"requireLinkedCompanion,omitempty"`
}
```

```go
// ClientRxRequireLinkedCompanionSet reports whether the operator set
// clientRxCoverage.requireLinkedCompanion (in effect only with user
// management on).
func (c *Config) ClientRxRequireLinkedCompanionSet() bool {
	return c != nil && c.ClientRxCoverage != nil && c.ClientRxCoverage.RequireLinkedCompanion
}
```

b) In `cmd/server/types.go`, in `ClientConfigResponse`, after `ClientRfSamples`, add:

```go
	// Present (true) only when clientRxCoverage.requireLinkedCompanion is set
	// and user management is on: CoreDrive RX then holds its queue until
	// its companion is linked.
	ClientRxRequireLinkedCompanion bool `json:"clientRxRequireLinkedCompanion,omitempty"`
```

c) In `cmd/server/routes.go`, in `handleConfigClient`, after `ClientRfSamples: s.cfg.ClientRfSamplesEnabled(),` add:

```go
		ClientRxRequireLinkedCompanion: s.auth != nil && s.cfg.ClientRxRequireLinkedCompanionSet(),
```

d) In `cmd/server/auth_service.go`, in `initUserManagement`, replace

```go
	if !s.cfg.UserManagementEnabled() {
		return nil
	}
```

with

```go
	if !s.cfg.UserManagementEnabled() {
		if s.cfg.ClientRxRequireLinkedCompanionSet() {
			log.Printf("[users] clientRxCoverage.requireLinkedCompanion is set but userManagement is off: the setting is ignored")
		}
		return nil
	}
```

e) In `config.example.json`, directly after the `"_comment_clientRxCoverage"` line, add:

```json
  "_comment_clientRxCoverage_requireLinkedCompanion": "Optional clientRxCoverage.requireLinkedCompanion (bool, default false): with userManagement.enabled, the ingestor drops every meshcore/client/<pubkey>/... message whose companion no user linked (account page, CoreDrive RX login), and /api/config/client tells RX to hold its queue. A filter for honest clients, not a security boundary: all RX clients share one broker account. Ignored, with a startup warning, when user management is off. Turn it on only once the RX clients in use support linking.",
```

Run `gofmt -w config.go types.go routes.go auth_service.go` (the `ClientConfigResponse` literal realigns).

- [ ] **Step 4: Run the tests**

Run: `cd cmd/server && go test -run 'RequireLinkedCompanion|ClientConfig|TestUserManagementOff' .`
Expected: PASS (the absent-vs-disabled byte comparison in `TestUserManagementOffIsUnchanged` still holds: the field is omitted).

- [ ] **Step 5: Commit**

```bash
git add cmd/server/config.go cmd/server/types.go cmd/server/routes.go cmd/server/auth_service.go cmd/server/client_config_linked_test.go config.example.json
git commit -F - <<'EOF'
feat(server): clientRxCoverage.requireLinkedCompanion config flag

/api/config/client reports clientRxRequireLinkedCompanion only when the
setting is on and user management is enabled; without user management
the server warns at startup and ignores it.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 6: Server-side merge into `meshcore-my-nodes`

**Files:**
- Create: `cmd/server/companion_mynodes.go`, `cmd/server/companion_mynodes_test.go`

- [ ] **Step 1: Write the failing tests**

Create `cmd/server/companion_mynodes_test.go`:

```go
package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/users"
)

func myNodesOf(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var d settingsDoc
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatal(err)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(d.Keys[myNodesKey]), &items); err != nil {
		t.Fatalf("my nodes %q: %v", d.Keys[myNodesKey], err)
	}
	return items
}

func TestAddToMyNodesMerges(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	a, uid := f.srv.auth, alice.me.ID
	pk1, pk2 := strings.Repeat("a1", 32), strings.Repeat("b2", 32)
	at := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

	// No document yet: one is started.
	if got, err := a.addToMyNodes(uid, pk1, "Car", at); err != nil || got != myNodesAdded {
		t.Fatalf("first add = %q, %v", got, err)
	}
	raw, v, _ := f.st.GetSettings(uid)
	items := myNodesOf(t, raw)
	if v.Revision != 1 || len(items) != 1 || items[0]["pubkey"] != pk1 || items[0]["name"] != "Car" ||
		items[0]["addedAt"] != "2026-10-08T12:00:00.000Z" {
		t.Fatalf("after first add: rev %d, items %v", v.Revision, items)
	}

	// Existing items and other keys are kept; a pubkey already listed (any case) is not added twice.
	existing := `[{"pubkey":"` + strings.ToUpper(pk2) + `","name":"Home <rpt>","addedAt":"2026-01-01T00:00:00.000Z","extra":1}]`
	doc, _ := encodeSettingsDoc(&settingsDoc{V: 1, Keys: map[string]string{"meshcore-theme": "dark", myNodesKey: existing}})
	v, err := f.st.PutSettings(uid, v, doc)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := a.addToMyNodes(uid, pk2, "x", at); err != nil || got != myNodesPresent {
		t.Fatalf("present = %q, %v", got, err)
	}
	if _, v2, _ := f.st.GetSettings(uid); v2 != v {
		t.Fatalf("present changed the version: %+v -> %+v", v, v2)
	}
	if got, err := a.addToMyNodes(uid, pk1, "", at); err != nil || got != myNodesAdded {
		t.Fatalf("second add = %q, %v", got, err)
	}
	raw, v3, _ := f.st.GetSettings(uid)
	if v3.Revision != v.Revision+1 || v3.Generation != v.Generation {
		t.Fatalf("revision not bumped in place: %+v -> %+v", v, v3)
	}
	var d settingsDoc
	_ = json.Unmarshal([]byte(raw), &d)
	items = myNodesOf(t, raw)
	if d.Keys["meshcore-theme"] != "dark" || len(items) != 2 || items[0]["name"] != "Home <rpt>" ||
		items[0]["extra"] != float64(1) || items[1]["pubkey"] != pk1 || items[1]["name"] != pk1[:12] {
		t.Fatalf("merged doc keys=%v items=%v", d.Keys, items)
	}
	if strings.Contains(raw, `\u003c`) {
		t.Fatal("merge HTML-escaped an existing item")
	}
}

func TestAddToMyNodesFullAtCap(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	a, uid := f.srv.auth, alice.me.ID
	pk1, pk2 := strings.Repeat("a1", 32), strings.Repeat("b2", 32)
	build := func(n int) string {
		items := `[{"pubkey":"` + pk2 + `","name":"` + strings.Repeat("x", n) + `","addedAt":"2026-01-01T00:00:00.000Z"}]`
		d, _ := encodeSettingsDoc(&settingsDoc{V: 1, Keys: map[string]string{myNodesKey: items}})
		return d
	}
	doc := build(settingsDocMaxBytes - 40 - len(build(0))) // 40 bytes below the cap: no room for an item
	if len(doc) != settingsDocMaxBytes-40 {
		t.Fatalf("fixture doc is %d bytes", len(doc))
	}
	v, err := f.st.PutSettings(uid, users.SettingsVersion{}, doc)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := a.addToMyNodes(uid, pk1, "Car", time.Now()); err != nil || got != myNodesFull {
		t.Fatalf("at the cap = %q, %v", got, err)
	}
	if raw, v2, _ := f.st.GetSettings(uid); v2 != v || raw != doc {
		t.Fatal("document changed at the cap")
	}
}

func TestAddToMyNodesErrorIsNotFull(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	a, uid := f.srv.auth, alice.me.ID
	// A stored my-nodes value that is not a JSON array cannot be merged.
	doc, _ := encodeSettingsDoc(&settingsDoc{V: 1, Keys: map[string]string{myNodesKey: `{"not":"a list"}`}})
	v, err := f.st.PutSettings(uid, users.SettingsVersion{}, doc)
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.addToMyNodes(uid, strings.Repeat("a1", 32), "Car", time.Now())
	if err == nil || got == myNodesFull {
		t.Fatalf("bad list = %q, %v; want an error, not full", got, err)
	}
	if raw, v2, _ := f.st.GetSettings(uid); v2 != v || raw != doc {
		t.Fatal("document changed on a merge error")
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd cmd/server && go test -run 'TestAddToMyNodes' .`
Expected: FAIL, build error `a.addToMyNodes undefined`, `undefined: myNodesKey`, `undefined: myNodesAdded`, `undefined: myNodesFull`.

- [ ] **Step 3: Implement**

Create `cmd/server/companion_mynodes.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/meshcore-analyzer/users"
)

const myNodesKey = "meshcore-my-nodes"

// Results of addToMyNodes, reported as myNodes by POST /api/account/companions.
const (
	myNodesAdded   = "added"
	myNodesPresent = "present"
	myNodesFull    = "full"   // strictly: the document would exceed settingsDocMaxBytes
	myNodesFailed  = "failed" // any other merge error (the handler maps a returned error to this)
)

// myNodeItem is the item shape public/home.js writes: {pubkey, name, addedAt}.
type myNodeItem struct {
	Pubkey  string `json:"pubkey"`
	Name    string `json:"name"`
	AddedAt string `json:"addedAt"`
}

// addToMyNodes adds pubkey to uid's synced meshcore-my-nodes unless it is
// listed already (any case). It is a read-modify-write of the settings
// document through PutSettings' revision check, so the revision is bumped
// and open browsers pick the change up through the normal conflict flow;
// a concurrent write is retried. Existing items are kept as they are
// (compacted). A document that would exceed settingsDocMaxBytes is left
// alone and the result is "full". Any other failure is returned as an
// error; the link handler logs it and reports "failed".
func (a *authService) addToMyNodes(uid int64, pubkey, name string, now time.Time) (string, error) {
	if name == "" {
		name = pubkey[:12] // what home.js shows for an unnamed node
	}
	item, err := marshalNoEscape(myNodeItem{Pubkey: pubkey, Name: name, AddedAt: now.UTC().Format("2006-01-02T15:04:05.000Z")})
	if err != nil {
		return "", err
	}
	for attempt := 0; attempt < 3; attempt++ {
		raw, v, err := a.st.GetSettings(uid)
		if err != nil {
			return "", err
		}
		doc := &settingsDoc{V: 1, Keys: map[string]string{}}
		if v.Revision != 0 {
			if err := json.Unmarshal([]byte(raw), doc); err != nil {
				return "", fmt.Errorf("stored settings do not decode: %w", err)
			}
			if doc.Keys == nil {
				doc.Keys = map[string]string{}
			}
		}
		var items []json.RawMessage
		if cur := doc.Keys[myNodesKey]; cur != "" {
			if err := json.Unmarshal([]byte(cur), &items); err != nil {
				return "", fmt.Errorf("%s is not a JSON array: %w", myNodesKey, err)
			}
		}
		for _, it := range items {
			var x struct {
				Pubkey string `json:"pubkey"`
			}
			if json.Unmarshal(it, &x) == nil && strings.EqualFold(strings.TrimSpace(x.Pubkey), pubkey) {
				return myNodesPresent, nil
			}
		}
		list, err := marshalNoEscape(append(items, json.RawMessage(item)))
		if err != nil {
			return "", err
		}
		doc.Keys[myNodesKey] = string(list)
		out, err := encodeSettingsDoc(doc)
		if err != nil {
			return "", err
		}
		if len(out) > settingsDocMaxBytes {
			return myNodesFull, nil
		}
		_, err = a.st.PutSettings(uid, v, out)
		if errors.Is(err, users.ErrSettingsConflict) {
			continue
		}
		if err != nil {
			return "", err
		}
		return myNodesAdded, nil
	}
	return "", errors.New("settings kept changing during three attempts")
}

// marshalNoEscape is json.Marshal without HTML escaping, like the
// browser's JSON.stringify (see encodeSettingsDoc).
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
```

- [ ] **Step 4: Run the tests**

Run: `cd cmd/server && go test -run 'TestAddToMyNodes|TestSettings' .`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/server/companion_mynodes.go cmd/server/companion_mynodes_test.go
git commit -F - <<'EOF'
feat(server): add a pubkey to meshcore-my-nodes server-side

addToMyNodes merges one {pubkey, name, addedAt} item into the synced
settings with a revision bump, keeps existing items and keys, skips a
pubkey already listed, and reports "full" instead of exceeding the
256 KiB cap.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 7: Companion routes (challenge, link, list, unlink)

**Files:**
- Create: `cmd/server/companion_handlers.go`, `cmd/server/companion_handlers_test.go`
- Modify: `cmd/server/auth_types.go`, `cmd/server/auth_service.go`, `cmd/server/auth_routes.go`, `cmd/server/openapi.go`

- [ ] **Step 1: Write the failing tests**

Create `cmd/server/companion_handlers_test.go`:

```go
package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/users"
)

// Deterministic companion identities (Ed25519 signatures are deterministic too).
var (
	companionKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, 32))
	otherKey     = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x24}, 32))
)

const testHost = "scope.example.org" // host of testBase

func pubHex(k ed25519.PrivateKey) string { return hex.EncodeToString(k.Public().(ed25519.PublicKey)) }

func signLink(k ed25519.PrivateKey, host, challenge string) string {
	return hex.EncodeToString(ed25519.Sign(k, []byte("corescope-link:"+host+":"+challenge)))
}

func (f *authFixture) companionChallenge(t *testing.T, tok, pk string) string {
	t.Helper()
	w := f.do("POST", "/api/account/companions/challenge", companionChallengeRequest{Pubkey: pk}, bearer(tok), header("Origin", rxOrigin))
	expectStatus(t, w, http.StatusOK)
	return decode[companionChallengeResponse](t, w).Challenge
}

// linkCompanion runs the RX flow: challenge, sign with the companion key, link.
func (f *authFixture) linkCompanion(t *testing.T, tok string, k ed25519.PrivateKey, name string) *httptest.ResponseRecorder {
	t.Helper()
	ch := f.companionChallenge(t, tok, pubHex(k))
	return f.do("POST", "/api/account/companions",
		companionLinkRequest{Pubkey: pubHex(k), Challenge: ch, Signature: signLink(k, testHost, ch), Name: name},
		bearer(tok), header("Origin", rxOrigin))
}

func hasCompanionAudit(t *testing.T, f *authFixture, uid int64, action, pk string) bool {
	t.Helper()
	f.srv.auth.waitAudits()
	entries, err := f.st.AuditFor(uid, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Action == action && e.Detail["pubkey"] == pk {
			return true
		}
	}
	return false
}

func TestCompanionLinkFlow(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	tok := f.deviceToken(t, "alice@example.org", pw, "Pixel").Token
	pk := pubHex(companionKey)

	w := f.linkCompanion(t, tok, companionKey, "Car")
	expectStatus(t, w, http.StatusOK)
	got := decode[companionLinkResponse](t, w)
	if got.Pubkey != pk || got.Name != "Car" || got.LinkedAt == "" || got.MyNodes != myNodesAdded {
		t.Fatalf("link response = %+v", got)
	}
	if l, err := f.st.GetCompanionLink(pk); err != nil || l.UserID != alice.me.ID {
		t.Fatalf("stored link = %+v, %v", l, err)
	}
	if !hasCompanionAudit(t, f, alice.me.ID, "companion.link", pk) {
		t.Fatal("no companion.link audit row")
	}

	// The browser lists it; no analyzer data, so lastSeenAt is null.
	w = f.do("GET", "/api/account/companions", nil, as(alice))
	expectStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `"lastSeenAt":null`) {
		t.Fatalf("list = %s", w.Body.String())
	}
	if list := decode[[]companionJSON](t, w); len(list) != 1 || list[0].Pubkey != pk || list[0].Name != "Car" {
		t.Fatalf("list = %+v", list)
	}

	// Unlink from the browser (cookie + CSRF); my nodes stay.
	expectStatus(t, f.do("DELETE", "/api/account/companions/"+pk, nil, as(alice)), http.StatusNoContent)
	if _, err := f.st.GetCompanionLink(pk); !errors.Is(err, users.ErrNotFound) {
		t.Fatalf("link survived unlink: %v", err)
	}
	if raw, _, _ := f.st.GetSettings(alice.me.ID); !strings.Contains(raw, pk) {
		t.Fatal("unlink removed the companion from my nodes")
	}
	if !hasCompanionAudit(t, f, alice.me.ID, "companion.unlink", pk) {
		t.Fatal("no companion.unlink audit row")
	}
	expectStatus(t, f.do("DELETE", "/api/account/companions/"+pk, nil, as(alice)), http.StatusNotFound)
	expectStatus(t, f.do("DELETE", "/api/account/companions/nothex", nil, as(alice)), http.StatusBadRequest)
	w = f.do("GET", "/api/account/companions", nil, bearer(tok))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("empty list = %d %s", w.Code, w.Body.String())
	}
}

func TestCompanionLinkRejections(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	tok := f.deviceToken(t, "alice@example.org", pw, "Pixel").Token
	pk := pubHex(companionKey)
	post := func(req companionLinkRequest) *httptest.ResponseRecorder {
		return f.do("POST", "/api/account/companions", req, bearer(tok))
	}

	// Wrong host in the signed message: 400, and the challenge is burned.
	ch := f.companionChallenge(t, tok, pk)
	expectStatus(t, post(companionLinkRequest{Pubkey: pk, Challenge: ch, Signature: signLink(companionKey, "other.example.org", ch)}), http.StatusBadRequest)
	expectStatus(t, post(companionLinkRequest{Pubkey: pk, Challenge: ch, Signature: signLink(companionKey, testHost, ch)}), http.StatusGone)

	// Signed by another key; malformed signature.
	ch = f.companionChallenge(t, tok, pk)
	expectStatus(t, post(companionLinkRequest{Pubkey: pk, Challenge: ch, Signature: signLink(otherKey, testHost, ch)}), http.StatusBadRequest)
	ch = f.companionChallenge(t, tok, pk)
	expectStatus(t, post(companionLinkRequest{Pubkey: pk, Challenge: ch, Signature: "abcd"}), http.StatusBadRequest)

	// A challenge bound to another pubkey; an unknown challenge.
	ch = f.companionChallenge(t, tok, pubHex(otherKey))
	expectStatus(t, post(companionLinkRequest{Pubkey: pk, Challenge: ch, Signature: signLink(companionKey, testHost, ch)}), http.StatusGone)
	unknown := strings.Repeat("00", 32)
	expectStatus(t, post(companionLinkRequest{Pubkey: pk, Challenge: unknown, Signature: signLink(companionKey, testHost, unknown)}), http.StatusGone)

	// Malformed pubkey: 400, and the challenge is still consumed.
	ch = f.companionChallenge(t, tok, pk)
	expectStatus(t, post(companionLinkRequest{Pubkey: "nothex", Challenge: ch, Signature: signLink(companionKey, testHost, ch)}), http.StatusBadRequest)
	expectStatus(t, post(companionLinkRequest{Pubkey: pk, Challenge: ch, Signature: signLink(companionKey, testHost, ch)}), http.StatusGone)
	expectStatus(t, f.do("POST", "/api/account/companions/challenge", companionChallengeRequest{Pubkey: "zz"}, bearer(tok)), http.StatusBadRequest)

	// Expired challenge.
	f.st.SetClock(func() time.Time { return time.Now().Add(-10 * time.Minute) })
	ch = f.companionChallenge(t, tok, pk)
	f.st.SetClock(time.Now)
	expectStatus(t, post(companionLinkRequest{Pubkey: pk, Challenge: ch, Signature: signLink(companionKey, testHost, ch)}), http.StatusGone)

	// Reused challenge: the first use links, the second answers 410.
	ch = f.companionChallenge(t, tok, pk)
	good := companionLinkRequest{Pubkey: pk, Challenge: ch, Signature: signLink(companionKey, testHost, ch), Name: "Car"}
	expectStatus(t, post(good), http.StatusOK)
	expectStatus(t, post(good), http.StatusGone)

	if list, _ := f.st.ListCompanionLinks(alice.me.ID); len(list) != 1 {
		t.Fatalf("links = %+v, want only the good one", list)
	}
}

func TestCompanionListLastSeen(t *testing.T) {
	f := newAuthFixture(t)
	f.srv.db = seedCoverageDB(t)
	pk := pubHex(companionKey)
	for _, at := range []string{"2026-10-01T10:00:00Z", "2026-10-02T09:00:00Z"} {
		mustExecDB(t, f.srv.db, `INSERT INTO client_receptions (rx_pubkey,heard_key,heard_keylen,snr,lat,lon,rx_at,ingested_at,src)
			VALUES ('`+pk+`','aabbcc',3,-6,51.05,3.72,'`+at+`','t','rxlog')`)
	}
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	tok := f.deviceToken(t, "alice@example.org", pw, "Pixel").Token
	expectStatus(t, f.linkCompanion(t, tok, companionKey, "Car"), http.StatusOK)

	w := f.do("GET", "/api/account/companions", nil, as(alice))
	expectStatus(t, w, http.StatusOK)
	list := decode[[]companionJSON](t, w)
	if len(list) != 1 || list[0].LastSeenAt == nil || *list[0].LastSeenAt != "2026-10-02T09:00:00Z" {
		t.Fatalf("list = %s", w.Body.String())
	}
}

// A merge error other than the size cap answers myNodes "failed", and the link stands.
func TestCompanionLinkMyNodesFailed(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	doc, _ := encodeSettingsDoc(&settingsDoc{V: 1, Keys: map[string]string{myNodesKey: `"not a list"`}})
	if _, err := f.st.PutSettings(alice.me.ID, users.SettingsVersion{}, doc); err != nil {
		t.Fatal(err)
	}
	tok := f.deviceToken(t, "alice@example.org", pw, "Pixel").Token
	w := f.linkCompanion(t, tok, companionKey, "Car")
	expectStatus(t, w, http.StatusOK)
	if got := decode[companionLinkResponse](t, w); got.MyNodes != myNodesFailed {
		t.Fatalf("myNodes = %q, want %q", got.MyNodes, myNodesFailed)
	}
	if l, err := f.st.GetCompanionLink(pubHex(companionKey)); err != nil || l.UserID != alice.me.ID {
		t.Fatalf("link did not stand: %+v, %v", l, err)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd cmd/server && go test -run 'TestCompanion' .`
Expected: FAIL, build error `undefined: companionChallengeRequest`, `undefined: companionLinkRequest`, `undefined: companionLinkResponse`, `undefined: companionJSON`.

- [ ] **Step 3: Types and rate limiter**

In `cmd/server/auth_types.go`, after `deviceTokenResponse`, add:

```go
type companionChallengeRequest struct {
	Pubkey string `json:"pubkey"`
}

type companionChallengeResponse struct {
	Challenge string `json:"challenge"`
	ExpiresAt string `json:"expiresAt"`
}

type companionLinkRequest struct {
	Pubkey    string `json:"pubkey"`
	Challenge string `json:"challenge"`
	Signature string `json:"signature"`
	Name      string `json:"name"`
}

type companionLinkResponse struct {
	Pubkey   string `json:"pubkey"`
	Name     string `json:"name"`
	LinkedAt string `json:"linkedAt"`
	MyNodes  string `json:"myNodes"` // myNodesAdded, myNodesPresent, myNodesFull or myNodesFailed
}

type companionJSON struct {
	Pubkey     string  `json:"pubkey"`
	Name       string  `json:"name"`
	LinkedAt   string  `json:"linkedAt"`
	LastSeenAt *string `json:"lastSeenAt"` // newest client_receptions.rx_at, or null
}
```

In `cmd/server/auth_service.go`:
- in `authService`, after `settingsPut`, add the field
  ```go
  	companion   *rateLimiter // companion challenge + link, per IP and per user
  ```
- in `newAuthService`, after `settingsPut: newRateLimiter(60, time.Hour),`, add
  ```go
  		companion:   newRateLimiter(60, time.Hour),
  ```
- in `prune()`, after `a.settingsPut.gc()`, add
  ```go
  	a.companion.gc()
  ```

- [ ] **Step 4: Handlers**

Create `cmd/server/companion_handlers.go`:

```go
package main

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/sigvalidate"
	"github.com/meshcore-analyzer/users"
)

// linkMessage is what a companion signs to prove it holds its key. The
// instance's public host is part of it, so a signature cannot be replayed
// against another CoreScope.
func (a *authService) linkMessage(challenge string) []byte {
	return []byte("corescope-link:" + a.set.baseURL.Host + ":" + challenge)
}

func (a *authService) allowCompanion(w http.ResponseWriter, r *http.Request, u *users.User) bool {
	return a.allow(w, r, a.companion, "user:"+strconv.FormatInt(u.ID, 10))
}

func (s *Server) handleCompanionChallenge(w http.ResponseWriter, r *http.Request, u *users.User, _ *users.Session) {
	a := s.auth
	if !a.allowCompanion(w, r, u) {
		return
	}
	var req companionChallengeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ch, exp, err := a.st.CreateLinkChallenge(u.ID, req.Pubkey)
	if errors.Is(err, users.ErrBadPubkey) {
		writeError(w, http.StatusBadRequest, "pubkey must be 64 hex characters")
		return
	}
	if err != nil {
		log.Printf("[users] link challenge for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, companionChallengeResponse{Challenge: ch, ExpiresAt: rfc3339(exp)})
}

func (s *Server) handleCompanionLink(w http.ResponseWriter, r *http.Request, u *users.User, _ *users.Session) {
	a := s.auth
	if !a.allowCompanion(w, r, u) {
		return
	}
	var req companionLinkRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// The challenge is consumed before anything else can fail, so a refused
	// attempt never leaves a usable challenge behind.
	pk, pkErr := users.NormalizePubkey(req.Pubkey)
	chErr := a.st.ConsumeLinkChallenge(u.ID, req.Pubkey, req.Challenge)
	if pkErr != nil {
		writeError(w, http.StatusBadRequest, "pubkey must be 64 hex characters")
		return
	}
	switch {
	case chErr == nil:
	case errors.Is(chErr, users.ErrChallengeMissing), errors.Is(chErr, users.ErrChallengeExpired), errors.Is(chErr, users.ErrChallengeMismatch):
		writeError(w, http.StatusGone, "challenge expired or already used, request a new one")
		return
	default:
		log.Printf("[users] consume link challenge for user #%d: %v", u.ID, chErr)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	pub, _ := hex.DecodeString(pk)
	sig, err := hex.DecodeString(strings.TrimSpace(req.Signature))
	if err != nil || len(sig) != 64 {
		writeError(w, http.StatusBadRequest, "signature must be 64 bytes as hex")
		return
	}
	if ok, err := sigvalidate.VerifyMessage(pub, sig, a.linkMessage(req.Challenge)); err != nil || !ok {
		writeError(w, http.StatusBadRequest, "signature does not verify for this pubkey")
		return
	}
	link, prev, err := a.st.UpsertCompanionLink(u.ID, pk, req.Name)
	if err != nil {
		log.Printf("[users] link companion for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	a.companionLinked(u, link, prev)
	myNodes, err := a.addToMyNodes(u.ID, link.Pubkey, link.Name, time.Now())
	if err != nil {
		log.Printf("[users] add linked companion to my nodes for user #%d: %v", u.ID, err)
		myNodes = myNodesFailed
	}
	writeJSON(w, companionLinkResponse{Pubkey: link.Pubkey, Name: link.Name, LinkedAt: rfc3339(link.LinkedAt), MyNodes: myNodes})
}

// companionLinked writes the audit row of a link.
func (a *authService) companionLinked(u *users.User, link *users.CompanionLink, _ int64) {
	a.auditAsync(idPtr(u.ID), "companion.link", idPtr(u.ID), map[string]string{"pubkey": link.Pubkey})
}

func (s *Server) handleCompanionList(w http.ResponseWriter, _ *http.Request, u *users.User, _ *users.Session) {
	links, err := s.auth.st.ListCompanionLinks(u.ID)
	if err != nil {
		log.Printf("[users] list companions for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, s.companionsJSON(links))
}

func (s *Server) handleCompanionDelete(w http.ResponseWriter, r *http.Request, u *users.User, _ *users.Session) {
	raw := mux.Vars(r)["pubkey"]
	switch err := s.auth.st.DeleteCompanionLink(u.ID, raw); {
	case err == nil:
	case errors.Is(err, users.ErrBadPubkey):
		writeError(w, http.StatusBadRequest, "pubkey must be 64 hex characters")
		return
	case errors.Is(err, users.ErrNotFound):
		writeError(w, http.StatusNotFound, "companion not linked to this account")
		return
	default:
		log.Printf("[users] unlink companion for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	pk, _ := users.NormalizePubkey(raw)
	s.auth.auditAsync(idPtr(u.ID), "companion.unlink", idPtr(u.ID), map[string]string{"pubkey": pk})
	w.WriteHeader(http.StatusNoContent)
}

// companionsJSON renders links with their last reception; always a
// non-nil slice.
func (s *Server) companionsJSON(links []users.CompanionLink) []companionJSON {
	pks := make([]string, 0, len(links))
	for _, l := range links {
		pks = append(pks, l.Pubkey)
	}
	seen := s.companionLastSeen(pks)
	out := make([]companionJSON, 0, len(links))
	for _, l := range links {
		c := companionJSON{Pubkey: l.Pubkey, Name: l.Name, LinkedAt: rfc3339(l.LinkedAt)}
		if at, ok := seen[l.Pubkey]; ok {
			c.LastSeenAt = &at
		}
		out = append(out, c)
	}
	return out
}

// companionLastSeen returns the newest client_receptions.rx_at per pubkey
// from the analyzer DB. It is informational: no DB, no table (client RX
// coverage never enabled) or a failed query give an empty map.
func (s *Server) companionLastSeen(pks []string) map[string]string {
	out := map[string]string{}
	if len(pks) == 0 || s.db == nil || s.db.conn == nil {
		return out
	}
	args := make([]interface{}, len(pks))
	for i, pk := range pks {
		args[i] = pk
	}
	rows, err := s.db.conn.Query(`SELECT rx_pubkey, MAX(rx_at) FROM client_receptions WHERE rx_pubkey IN (`+
		sqlPlaceholders(len(pks))+`) GROUP BY rx_pubkey`, args...)
	if err != nil {
		if !strings.Contains(err.Error(), "no such table") {
			log.Printf("[users] companion last seen: %v", err)
		}
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var pk string
		var at sql.NullString
		if rows.Scan(&pk, &at) == nil && at.Valid {
			out[strings.ToLower(pk)] = at.String
		}
	}
	return out
}
```

- [ ] **Step 5: Routes and OpenAPI**

In `cmd/server/auth_routes.go`, after the `/api/account/settings` DELETE line, add:

```go
	r.HandleFunc("/api/account/companions/challenge", s.withUser(s.handleCompanionChallenge)).Methods("POST")
	r.HandleFunc("/api/account/companions", s.withUser(s.handleCompanionList)).Methods("GET")
	r.HandleFunc("/api/account/companions", s.withUser(s.handleCompanionLink)).Methods("POST")
	r.HandleFunc("/api/account/companions/{pubkey}", s.withUser(s.handleCompanionDelete)).Methods("DELETE")
```

In `cmd/server/openapi.go`, after the `"DELETE /api/account/settings"` entry, add:

```go
		"POST /api/account/companions/challenge":           {Summary: "Start linking a companion", Description: "Body {pubkey} (64 hex). 200 {challenge, expiresAt}: 32 random bytes as hex, single use, valid 5 minutes, bound to the caller and the pubkey. 400 malformed pubkey; 429 above 60 challenge + link requests per hour per user and per IP. Accepts a device token (Authorization: Bearer).", Tag: "users", Session: true},
		"POST /api/account/companions":                     {Summary: "Link a companion", Description: "Body {pubkey, challenge, signature, name}. signature is the companion's Ed25519 signature (64 bytes, hex) over the UTF-8 string \"corescope-link:\" + host of userManagement.publicBaseUrl + \":\" + challenge. The challenge is consumed in every case. 200 {pubkey, name, linkedAt, myNodes}: myNodes is added, present, full (not added: the synced settings would exceed 256 KiB) or failed (not added: the merge failed for another reason); the link stands in both cases. The pubkey is added to meshcore-my-nodes with a revision bump. A companion linked to another account moves to the caller (the newest proof wins); both get a companion.transfer audit row and the previous owner a mail when notifications are on for them. 400 malformed pubkey or signature, or a signature that does not verify; 410 challenge missing, expired, used, or bound to another user or pubkey; 429 rate limited. Accepts a device token.", Tag: "users", Session: true},
		"GET /api/account/companions":                      {Summary: "List own linked companions", Description: "[{pubkey, name, linkedAt, lastSeenAt}], newest link first. lastSeenAt is the newest client reception of that companion (rx_at as stored) or null. Accepts a device token.", Tag: "users", Session: true},
		"DELETE /api/account/companions/{pubkey}":          {Summary: "Unlink an own companion", Description: "204. meshcore-my-nodes and stored coverage stay. 400 malformed pubkey, 404 not linked to the caller. Accepts a device token.", Tag: "users", Session: true},
```

- [ ] **Step 6: Run the tests**

Run: `cd cmd/server && go test -run 'TestCompanion|TestBearer|TestOpenAPI|TestJanitor' .`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add cmd/server/companion_handlers.go cmd/server/companion_handlers_test.go cmd/server/auth_types.go cmd/server/auth_service.go cmd/server/auth_routes.go cmd/server/openapi.go
git commit -F - <<'EOF'
feat(server): link companions with a signed challenge

POST /api/account/companions/challenge issues a single-use challenge;
POST /api/account/companions verifies the companion's Ed25519 signature
over "corescope-link:<host>:<challenge>" (410 for a missing, expired,
used or mismatched challenge, 400 for a bad signature or pubkey), links
it, and adds it to meshcore-my-nodes. GET lists links with lastSeenAt
from client_receptions; DELETE unlinks. Rate-limited, audited.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 8: Transfer between users: audit rows and mail

**Files:**
- Create: `cmd/server/companion_transfer_test.go`
- Modify: `cmd/server/companion_handlers.go`

- [ ] **Step 1: Write the failing tests**

Create `cmd/server/companion_transfer_test.go`:

```go
package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/users"
)

func TestCompanionTransfer(t *testing.T) {
	f := newAuthFixture(t)
	f.srv.auth.set.notify.enabled = true
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	bob := f.registerAndActivate(t, "bob@example.org", "Bob", pw)
	aliceTok := f.deviceToken(t, "alice@example.org", pw, "Pixel").Token
	bobTok := f.deviceToken(t, "bob@example.org", pw, "iPhone").Token
	pk := pubHex(companionKey)

	expectStatus(t, f.linkCompanion(t, aliceTok, companionKey, "Car"), http.StatusOK)
	sent := len(f.fake.Sent())
	expectStatus(t, f.linkCompanion(t, bobTok, companionKey, "Bike"), http.StatusOK)

	if l, err := f.st.GetCompanionLink(pk); err != nil || l.UserID != bob.me.ID {
		t.Fatalf("after transfer: %+v, %v", l, err)
	}
	for _, uid := range []int64{alice.me.ID, bob.me.ID} {
		if !hasCompanionAudit(t, f, uid, "companion.transfer", pk) {
			t.Fatalf("no companion.transfer audit row for user #%d", uid)
		}
	}
	msgs := f.fake.Sent()
	if len(msgs) != sent+1 {
		t.Fatalf("mails sent for the transfer = %d, want 1", len(msgs)-sent)
	}
	if m := msgs[len(msgs)-1]; m.To != "alice@example.org" || !strings.Contains(m.Text, "Car") || !strings.Contains(m.Text, pk[:12]) {
		t.Fatalf("transfer mail = to %q, text %q", m.To, m.Text)
	}
	if list, _ := f.st.ListCompanionLinks(alice.me.ID); len(list) != 0 {
		t.Fatalf("previous owner still lists it: %+v", list)
	}
}

func TestCompanionTransferMailNeedsNotifications(t *testing.T) {
	for _, tc := range []struct {
		name           string
		instance, user bool
	}{
		{"instance off", false, true},
		{"user opted out", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAuthFixture(t)
			f.srv.auth.set.notify.enabled = tc.instance
			alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
			f.registerAndActivate(t, "bob@example.org", "Bob", pw)
			if !tc.user {
				if _, err := f.st.SetNotifyPrefs(alice.me.ID, false, users.NodeNotifyEvents); err != nil {
					t.Fatal(err)
				}
			}
			aliceTok := f.deviceToken(t, "alice@example.org", pw, "Pixel").Token
			bobTok := f.deviceToken(t, "bob@example.org", pw, "iPhone").Token
			expectStatus(t, f.linkCompanion(t, aliceTok, companionKey, "Car"), http.StatusOK)
			sent := len(f.fake.Sent())
			expectStatus(t, f.linkCompanion(t, bobTok, companionKey, "Bike"), http.StatusOK)
			if n := len(f.fake.Sent()) - sent; n != 0 {
				t.Fatalf("%d transfer mail(s) sent", n)
			}
			if !hasCompanionAudit(t, f, alice.me.ID, "companion.transfer", pubHex(companionKey)) {
				t.Fatal("audit row missing without a mail")
			}
		})
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd cmd/server && go test -run 'TestCompanionTransfer' .`
Expected: FAIL with `no companion.transfer audit row for user #1` (the link writes `companion.link` only).

- [ ] **Step 3: Implement**

In `cmd/server/companion_handlers.go`, add `"context"` and `"github.com/meshcore-analyzer/mailer"` to the imports, and replace `companionLinked` with:

```go
// companionTransferMailPurpose labels the transfer mail in mail_log.
const companionTransferMailPurpose = "companion.transfer"

// companionLinked writes the audit rows of a link. For a transfer (prev is
// the previous owner) both users get a companion.transfer row and the
// previous owner a mail. Everything runs in the background, tracked by
// auditWG so waitAudits (tests, shutdown) covers the mail too.
func (a *authService) companionLinked(u *users.User, link *users.CompanionLink, prev int64) {
	if prev == 0 {
		a.auditAsync(idPtr(u.ID), "companion.link", idPtr(u.ID), map[string]string{"pubkey": link.Pubkey})
		return
	}
	detail := map[string]string{"pubkey": link.Pubkey, "from": strconv.FormatInt(prev, 10), "to": strconv.FormatInt(u.ID, 10)}
	a.auditAsync(idPtr(u.ID), "companion.transfer", idPtr(u.ID), detail)
	a.auditAsync(idPtr(u.ID), "companion.transfer", idPtr(prev), detail)
	a.auditWG.Add(1)
	go func() {
		defer a.auditWG.Done()
		a.mailCompanionTransfer(prev, link)
	}()
}

// mailCompanionTransfer tells the previous owner that their companion now
// belongs to another account, when node notifications are on for the
// instance and for them (the same opt-in as watched-node mails), and the
// account is active with a working address.
func (a *authService) mailCompanionTransfer(prevID int64, link *users.CompanionLink) {
	if !a.set.notify.enabled {
		return
	}
	prev, err := a.st.GetByID(prevID)
	if err != nil || prev.Status != users.StatusActive || prev.EmailBouncing {
		return
	}
	p, err := a.st.NotifyPrefsFor(prevID)
	if err != nil {
		log.Printf("[users] companion transfer mail for user #%d: preferences: %v", prevID, err)
		return
	}
	if !p.Enabled {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), notifySendTimeout)
	defer cancel()
	_ = a.sendMail(ctx, prev, companionTransferMailPurpose, a.companionTransferMail(prev, link)) // failure logged by sendMail
}

func (a *authService) companionTransferMail(u *users.User, link *users.CompanionLink) mailer.Message {
	what := link.Pubkey[:12]
	if name := mailSafeText(link.Name); name != "" {
		what = name + " (" + what + ")"
	}
	return a.render(u.Email, u.DisplayName, "companion", mailContent{
		subject:  "Your companion was linked to another account",
		greeting: "Hello " + u.DisplayName + ",",
		paragraphs: []string{
			"Your companion " + what + " was just linked to another account on " + a.set.baseURL.Host +
				". That account proved it holds the companion's private key, so the companion and its coverage now count for that account, not yours.",
			"If you passed the companion on, nothing needs to be done. If not, its key is in someone else's hands.",
		},
		actionLabel: "Your companions", actionURL: a.set.baseURL.String() + "/#/account?section=companions",
	})
}
```

- [ ] **Step 4: Run the tests**

Run: `cd cmd/server && go test -run 'TestCompanion|TestNotif|Mail' .`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/server/companion_handlers.go cmd/server/companion_transfer_test.go
git commit -F - <<'EOF'
feat(server): audit and mail a companion transfer

When a newer proof moves a companion to another account, both users get
a companion.transfer audit row, and the previous owner a mail when node
notifications are on for the instance and for them.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 9: Admin user detail lists companions

**Files:**
- Create: `cmd/server/admin_companions_test.go`
- Modify: `cmd/server/auth_types.go`, `cmd/server/admin_users_handlers.go`, `cmd/server/openapi.go`

- [ ] **Step 1: Write the failing test**

Create `cmd/server/admin_companions_test.go`:

```go
package main

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestAdminUserDetailCompanions(t *testing.T) {
	f := newAuthFixture(t, "admin@example.org")
	admin := f.registerAndActivate(t, "admin@example.org", "Admin", pw)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	tok := f.deviceToken(t, "alice@example.org", pw, "Pixel").Token
	expectStatus(t, f.linkCompanion(t, tok, companionKey, "Car"), http.StatusOK)

	w := f.do("GET", "/api/admin/users/"+strconv.FormatInt(alice.me.ID, 10), nil, as(admin))
	expectStatus(t, w, http.StatusOK)
	d := decode[adminUserDetailJSON](t, w)
	if len(d.Companions) != 1 || d.Companions[0].Pubkey != pubHex(companionKey) || d.Companions[0].Name != "Car" {
		t.Fatalf("companions = %+v", d.Companions)
	}
	w = f.do("GET", "/api/admin/users/"+strconv.FormatInt(admin.me.ID, 10), nil, as(admin))
	expectStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `"companions":[]`) {
		t.Fatalf("no companions should be [], body %s", w.Body.String())
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd cmd/server && go test -run TestAdminUserDetailCompanions .`
Expected: FAIL, build error `d.Companions undefined (type adminUserDetailJSON has no field or method Companions)`.

- [ ] **Step 3: Implement**

In `cmd/server/auth_types.go`, replace `adminUserDetailJSON` with:

```go
type adminUserDetailJSON struct {
	User       adminUserJSON   `json:"user"`
	Sessions   []sessionJSON   `json:"sessions"`
	Companions []companionJSON `json:"companions"`
	Mail       []mailJSON      `json:"mail"`
	Audit      []auditJSON     `json:"audit"`
}
```

In `cmd/server/admin_users_handlers.go`, in `handleAdminUserDetail`, directly after the loop that fills `d.Sessions`, add:

```go
	links, err := a.st.ListCompanionLinks(u.ID)
	if err != nil {
		adminStoreFail(w, "list companions", u.ID, err)
		return
	}
	d.Companions = s.companionsJSON(links)
```

In `cmd/server/openapi.go`, replace the `"GET /api/admin/users/{id}"` entry with:

```go
		"GET /api/admin/users/{id}":                        {Summary: "User detail with sessions, companions, mail log and audit (admin)", Description: "{user, sessions, companions, mail, audit}. companions is [{pubkey, name, linkedAt, lastSeenAt}] like GET /api/account/companions.", Tag: "users", Session: true},
```

- [ ] **Step 4: Run the tests**

Run: `cd cmd/server && go test -run 'TestAdmin|TestOpenAPI' .`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/server/auth_types.go cmd/server/admin_users_handlers.go cmd/server/openapi.go cmd/server/admin_companions_test.go
git commit -F - <<'EOF'
feat(server): admin user detail lists linked companions

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 10: `GET /api/rx-coverage?mine=1`

**Files:**
- Create: `cmd/server/rx_coverage_mine_test.go`
- Modify: `cmd/server/rx_dashboard.go`, `cmd/server/rx_dashboard_test.go`

- [ ] **Step 1: Write the failing tests**

Create `cmd/server/rx_coverage_mine_test.go`:

```go
package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRxCoverageMine(t *testing.T) {
	f := newAuthFixture(t)
	f.srv.db = seedCoverageDB(t)
	f.srv.cfg.ClientRxCoverage = &ClientRxCoverageConfig{Enabled: true}
	f.router.HandleFunc("/api/rx-coverage", f.srv.handleRxCoverage).Methods("GET")
	mine, other := pubHex(companionKey), pubHex(otherKey)
	now := time.Now().UTC().Format(time.RFC3339)
	for i, pk := range []string{mine, other, other} {
		mustExecDB(t, f.srv.db, fmt.Sprintf(`INSERT INTO client_receptions (rx_pubkey,heard_key,heard_keylen,snr,lat,lon,rx_at,ingested_at,src)
			VALUES ('%s','aabbcc',3,-6,%f,3.72,'%s','t','rxlog')`, pk, 51.05+float64(i)*0.05, now))
	}
	const q = "/api/rx-coverage?bbox=50,3,52,4&z=10"
	get := func(path string, mods ...reqMod) *httptest.ResponseRecorder { return f.do("GET", path, nil, mods...) }
	empty := get(q + "&rx=" + strings.Repeat("0", 64)).Body.String()

	expectStatus(t, get(q+"&mine=1"), http.StatusUnauthorized)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	w := get(q+"&mine=1", as(alice))
	expectStatus(t, w, http.StatusOK)
	if w.Body.String() != empty {
		t.Fatalf("nothing linked: got %s, want the empty collection", w.Body.String())
	}

	tok := f.deviceToken(t, "alice@example.org", pw, "Pixel").Token
	expectStatus(t, f.linkCompanion(t, tok, companionKey, "Car"), http.StatusOK)
	w = get(q+"&mine=1", as(alice))
	expectStatus(t, w, http.StatusOK)
	if w.Body.String() != get(q+"&rx="+mine).Body.String() {
		t.Fatalf("mine=1 differs from rx=<linked companion>: %s", w.Body.String())
	}
	if w.Body.String() == get(q).Body.String() {
		t.Fatal("mine=1 returned everyone's coverage")
	}
	// Coverage is not in a device token's scope.
	expectStatus(t, get(q+"&mine=1", bearer(tok)), http.StatusForbidden)
}

func TestRxCoverageMineWithoutUserManagement(t *testing.T) {
	srv := &Server{db: seedCoverageDB(t), cfg: &Config{ClientRxCoverage: &ClientRxCoverageConfig{Enabled: true}}}
	w := httptest.NewRecorder()
	srv.handleRxCoverage(w, httptest.NewRequest("GET", "/api/rx-coverage?bbox=50,3,52,4&mine=1", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("mine=1 with user management off = %d, want 404", w.Code)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `cd cmd/server && go test -run 'TestRxCoverageMine' .`
Expected: FAIL: `status = 200, want 401` (mine is ignored today) and `mine=1 with user management off = 200, want 404`.

- [ ] **Step 3: Implement**

In `cmd/server/rx_dashboard.go`, replace the signature and first lines of `queryCoverageFiltered` up to (not including) `if days > 0 {` with:

```go
// queryCoverageFiltered reads coverage rows in b. rxIn, when non-nil,
// keeps only receptions by those companions (rx_pubkey, lowercase); an
// empty non-nil rxIn matches nothing.
func (s *Server) queryCoverageFiltered(node, rx string, rxIn []string, days int, b bbox) ([]coverageRow, error) {
	if rxIn != nil && len(rxIn) == 0 {
		return nil, nil
	}
	where := []string{"lat BETWEEN ? AND ?", "lon BETWEEN ? AND ?"}
	args := []interface{}{b.MinLat, b.MaxLat, b.MinLon, b.MaxLon}
	if node != "" {
		// Sargable heard_key IN-list (see coverageHeardKeyCandidates) so the
		// (heard_key, …) composite index is used instead of a substr() scan (#5).
		cands := coverageHeardKeyCandidates(node)
		where = append(where, "heard_key IN ("+sqlPlaceholders(len(cands))+")")
		for _, c := range cands {
			args = append(args, c)
		}
	}
	if rx != "" {
		where = append(where, "rx_pubkey = ?")
		args = append(args, strings.ToLower(rx))
	}
	if len(rxIn) > 0 {
		where = append(where, "rx_pubkey IN ("+sqlPlaceholders(len(rxIn))+")")
		for _, pk := range rxIn {
			args = append(args, pk)
		}
	}
```

Replace `handleRxCoverage` with:

```go
func (s *Server) handleRxCoverage(w http.ResponseWriter, r *http.Request) {
	if !s.requireClientRxCoverage(w, r) {
		return
	}
	var mine []string
	if r.URL.Query().Get("mine") == "1" {
		var ok bool
		if mine, ok = s.myCompanionPubkeys(w, r); !ok {
			return
		}
	}
	b, ok := parseBBox(r.URL.Query().Get("bbox"))
	if !ok {
		http.Error(w, "bbox required as minLat,minLon,maxLat,maxLon", http.StatusBadRequest)
		return
	}
	if s.db == nil || s.db.conn == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	days := clampDays(atoiDefault(r.URL.Query().Get("days"), 7))
	z, _ := strconv.Atoi(r.URL.Query().Get("z"))
	rows, err := s.queryCoverageFiltered(r.URL.Query().Get("node"), r.URL.Query().Get("rx"), mine, days, b)
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	fc := aggregateCoverage(rows, zoomToHexRes(z), s.heardKeyResolverFor(rows))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(fc)
}

// myCompanionPubkeys resolves ?mine=1 to the caller's linked companions
// (never nil, so an empty list filters everything out). Browser session
// only: 404 with user management off, 403 for a bearer token (coverage is
// outside its scope), 401 without a session.
func (s *Server) myCompanionPubkeys(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	if s.auth == nil {
		http.NotFound(w, r)
		return nil, false
	}
	if _, ok := bearerToken(r); ok {
		writeBearerFail(w, http.StatusForbidden)
		return nil, false
	}
	u, _ := s.auth.currentUser(w, r)
	if u == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return nil, false
	}
	links, err := s.auth.st.ListCompanionLinks(u.ID)
	if err != nil {
		log.Printf("[users] coverage mine for user #%d: %v", u.ID, err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return nil, false
	}
	out := make([]string, 0, len(links))
	for _, l := range links {
		out = append(out, l.Pubkey)
	}
	return out, true
}
```

In `cmd/server/rx_dashboard_test.go`, give the three existing calls the new argument:

```bash
sed -i 's/queryCoverageFiltered("", "\([a-z]*\)", /queryCoverageFiltered("", "\1", nil, /' cmd/server/rx_dashboard_test.go
```

(they become `queryCoverageFiltered("", "", nil, 7, bb)`, `queryCoverageFiltered("", "compa", nil, 7, bb)` and `queryCoverageFiltered("", "", nil, 0, bb)`).

- [ ] **Step 4: Run the tests**

Run: `cd cmd/server && go test -run 'TestRxCoverage|Coverage|TestMobileRx' .`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/server/rx_dashboard.go cmd/server/rx_dashboard_test.go cmd/server/rx_coverage_mine_test.go
git commit -F - <<'EOF'
feat(server): rx-coverage?mine=1 filters to the caller's companions

A read-time join on companion_links: all coverage of a companion counts
for its current owner. Needs a browser session (401 without, 403 for a
device token, 404 with user management off); the response shape is
unchanged.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 11: CORS for the bearer routes

**Files:**
- Create: `cmd/server/cors_bearer_test.go`
- Modify: `cmd/server/cors.go`, `cmd/server/auth_routes.go`

- [ ] **Step 1: Write the failing test**

Create `cmd/server/cors_bearer_test.go`:

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
)

func TestCORSBearerRoutes(t *testing.T) {
	f := newAuthFixture(t)
	f.srv.cfg.CORSAllowedOrigins = []string{rxOrigin}
	r := mux.NewRouter()
	r.Use(f.srv.corsMiddleware)
	f.srv.registerAuthRoutes(r)
	f.router = r

	preflight := func(path, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("OPTIONS", path, nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	for _, p := range []string{"/api/auth/device-token", "/api/auth/me", "/api/auth/logout", "/api/account/settings",
		"/api/account/companions", "/api/account/companions/challenge", "/api/account/companions/" + strings.Repeat("ab", 32)} {
		w := preflight(p, rxOrigin)
		h := w.Header()
		if w.Code != http.StatusNoContent || h.Get("Access-Control-Allow-Origin") != rxOrigin ||
			h.Get("Access-Control-Allow-Methods") != "GET, HEAD, POST, PUT, DELETE, OPTIONS" ||
			h.Get("Access-Control-Allow-Headers") != "Authorization, Content-Type" ||
			h.Get("Access-Control-Allow-Credentials") != "" {
			t.Errorf("preflight %s = %d %v", p, w.Code, h)
		}
	}
	if w := preflight("/api/account/companions", "https://evil.example"); w.Code != http.StatusForbidden || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("foreign origin preflight = %d %v", w.Code, w.Header())
	}
	if m := preflight("/api/account/sessions", rxOrigin).Header().Get("Access-Control-Allow-Methods"); strings.Contains(m, "POST") {
		t.Errorf("non-bearer route allows writes cross-origin: %q", m)
	}

	// The actual cross-origin request carries the origin echo, never credentials.
	f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	w := f.do("POST", "/api/auth/device-token", deviceTokenRequest{Email: "alice@example.org", Password: pw, DeviceName: "x"},
		header("Origin", rxOrigin))
	expectStatus(t, w, http.StatusOK)
	if w.Header().Get("Access-Control-Allow-Origin") != rxOrigin || w.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("device-token CORS headers = %v", w.Header())
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `cd cmd/server && go test -run TestCORSBearerRoutes .`
Expected: FAIL: `preflight /api/auth/device-token = 405 …` (mux answers a method mismatch before any middleware runs) for every bearer path.

- [ ] **Step 3: Implement**

In `cmd/server/auth_routes.go`, change the import to

```go
import (
	"net/http"

	"github.com/gorilla/mux"
)
```

and make this the first statement of `registerAuthRoutes`:

```go
	// CORS preflight for the bearer routes. gorilla/mux does not run
	// middleware on a method mismatch, so without this route an OPTIONS on
	// a POST-only path would answer 405 before corsMiddleware could. A
	// matcher, not a path: it is no API route of its own (not in the spec).
	r.MatcherFunc(func(req *http.Request, _ *mux.RouteMatch) bool {
		return req.Method == http.MethodOptions && bearerCORSPath(req.URL.Path)
	}).HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
```

In `cmd/server/cors.go`, replace

```go
		// Read-only embed contract — see comment above.
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key")
```

with

```go
		if s.auth != nil && bearerCORSPath(r.URL.Path) {
			// Companion linking: CoreDrive RX on an allowlisted origin logs in
			// and writes with a device token in Authorization. Still no
			// credentials: nothing rides on cookies.
			w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		} else {
			// Read-only embed contract — see comment above.
			w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key")
		}
```

and extend the `corsMiddleware` doc comment's embed paragraph with one sentence: `The exception is the bearer routes of companion linking (bearerCORSPath, only with user management on), which also allow POST, PUT, DELETE and the Authorization header.`

- [ ] **Step 4: Run the tests**

Run: `cd cmd/server && go test -run 'CORS|TestOpenAPI|TestBearer|TestDeviceToken' .`
Expected: PASS (existing `TestCORS_*` unchanged: `srv.auth` is nil there).

- [ ] **Step 5: Commit**

```bash
git add cmd/server/cors.go cmd/server/auth_routes.go cmd/server/cors_bearer_test.go
git commit -F - <<'EOF'
feat(server): CORS writes for the device-token routes

For allowlisted origins, the device-token login and the bearer routes
allow POST, PUT, DELETE and Authorization, Content-Type; credentials
stay off. A preflight matcher lets OPTIONS reach corsMiddleware.

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF
```

---

### Task 12: Feature-off guard and full verification

**Files:**
- Create: `cmd/server/companion_off_test.go`

- [ ] **Step 1: Write the guard test**

Create `cmd/server/companion_off_test.go`:

```go
package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// With user management off every F route answers 404, like the rest of A–E.
func TestCompanionRoutesAbsentWhenOff(t *testing.T) {
	srv, router := setupTestServer(t)
	srv.cfg.ClientRxCoverage = &ClientRxCoverageConfig{Enabled: true}
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/auth/device-token"},
		{"GET", "/api/account/companions"},
		{"POST", "/api/account/companions"},
		{"POST", "/api/account/companions/challenge"},
		{"DELETE", "/api/account/companions/" + strings.Repeat("ab", 32)},
		{"GET", "/api/rx-coverage?mine=1&bbox=50,3,52,4"},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(c.method, c.path, nil))
		if w.Code != 404 {
			t.Errorf("%s %s = %d with the feature off; want 404", c.method, c.path, w.Code)
		}
	}
}
```

- [ ] **Step 2: Run it**

Run: `cd cmd/server && go test -run TestCompanionRoutesAbsentWhenOff .`
Expected: PASS (a guard for Tasks 3, 7 and 10; it passes as soon as they are in).

- [ ] **Step 3: Full verification**

Run: `cd internal/sigvalidate && go test . && gofmt -l .`
Run: `cd internal/users && go test .`
Run: `cd cmd/server && go test ./... && gofmt -l . && go vet ./...`
Run: `cd cmd/ingestor && go build ./... && go test ./...`
Expected: every test PASS; no output from `gofmt -l` or `go vet`. `TestOpenAPICompleteness` passes without new entries in `openapi_known_gaps.json`.

- [ ] **Step 4: Commit**

```bash
git add cmd/server/companion_off_test.go
git commit -F - <<'EOF'
test(server): companion-linking routes answer 404 with the feature off

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
EOF
```

---

## Self-review against the spec

- *Device token*: issue with the login's constant-cost path, rate limits (same buckets) and `user.login` audit row, `deviceName` cleaned by F1's `CleanLabel` (Task 3); bearer in `withUser` with no CSRF and a scope check, 403 outside it (also `withAdmin`, Task 4); logout with a bearer revokes the device row (Task 4); sessions list `kind` and `label` (Task 2). The cookie path refuses device sessions and the bearer path refuses web sessions (Tasks 2 and 4, both tested).
- *Companions*: challenge 32 bytes hex, 5 minutes, single use, bound (F1, exposed in Task 7); link steps 1–5 in order, the challenge consumed in every case, 410 for missing/expired/mismatch, 400 for bad signature or pubkey, signature over `"corescope-link:" + host + ":" + challenge` via `internal/sigvalidate` (Tasks 1 and 7); upsert and transfer audit rows for both users, mail to the previous owner only with notifications on (Task 8); `meshcore-my-nodes` merge with revision bump, `added`/`present`/`full`/`failed` (`full` strictly for the size cap, `failed` for any other merge error, both tested), link succeeds either way (Tasks 6–7); list with `lastSeenAt` from `client_receptions` (Task 7); delete leaves my nodes alone (Task 7); admin detail `companions` (Task 9); audit kinds `companion.link`, `companion.unlink`, `companion.transfer` (Tasks 7–8).
- *Coverage attribution*: `?mine=1` filters by a read-time join, 401 without a session, shape unchanged (Task 10).
- *Linked-only ingest* (server part): config field, client-config flag only when in effect, startup warning (Task 5). The ingestor filter is F3.
- *CORS*: only allowlisted origins, only bearer routes, POST/PUT/DELETE and Authorization/Content-Type, no credentials (Task 11).
- *Error handling*: 404 feature off (Task 12), 401 wrong credentials with the login message (Task 3), 403 out of scope (Task 4), 401 expired/revoked (Task 4), 410 challenge (Task 7), 400 signature/pubkey (Task 7), 429 with `Retry-After` (Task 3; companion limiter Task 7). Tokens, challenges and signatures are never logged.
- *Testing → cmd/server*: every bullet has a test above; the "real MeshCore key" fixture is a fixed-seed Ed25519 key (MeshCore identities are Ed25519; signatures are deterministic).
- Names match F1: `SessionKindWeb`/`SessionKindDevice`, `DeviceSessionTTL`, `HasScope`, `CreateDeviceSession(userID, label, scopes, userAgent)`, `CreateLinkChallenge`, `ConsumeLinkChallenge(userID, pubkey, challenge)`, `UpsertCompanionLink` → `(*CompanionLink, prevOwner, error)`, `ListCompanionLinks`, `GetCompanionLink`, `DeleteCompanionLink(userID, pubkey)`, `NormalizePubkey`, `ErrBadPubkey`, `ErrChallenge*`. `LinkedPubkeys` and `PruneLinkChallenges` are not called by F2 (the janitor call is F1's).
- Deviations from the brief, resolved here: `internal/sigvalidate` had only `ValidateAdvert`, so Task 1 adds `VerifyMessage`. `/api/auth/me` and `/api/auth/logout` were not under `withUser` (me read the cookie directly, logout sat behind `requireOrigin`), so Task 4 moves me under `withUser` and gives logout its own bearer branch before the origin check. The device-token endpoint has no Origin check (RX runs cross-origin, and no cookie is set). gorilla/mux skips middleware on a method mismatch, hence the preflight matcher in Task 11. `/api/rx-coverage` stays in `openapi_known_gaps.json`, so `mine` is documented in this plan's API table only.
