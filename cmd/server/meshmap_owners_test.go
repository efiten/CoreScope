package main

// Fork-local (meshmap owner): tests for meshmap_owners.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const mmPK = "efef7943505052b47f1809488ea4b4d3942d4ed72d2b1953b90a9f5e62a65fb5"

func TestParseMeshmapClaims(t *testing.T) {
	long := strings.Repeat("é", 70)
	upper := strings.ToUpper(strings.Repeat("ab", 32))
	raw := `{"version":1,"generatedAt":"2026-11-17T10:00:00Z","claims":[
	 {"publicKey":"` + mmPK + `","role":"owner","subject":"s1","displayName":"erwinfiten","discordUrl":"https://discord.com/users/123456789012345678","since":null},
	 {"publicKey":"` + mmPK + `","role":"manager","subject":"s2","displayName":"zed","discordUrl":"https://evil.example/users/123456789012345678"},
	 {"publicKey":"` + mmPK + `","role":"manager","subject":"s3","displayName":null,"discordUrl":"https://discord.com/users/223456789012345678"},
	 {"publicKey":"` + mmPK + `","role":"manager","subject":"s4","displayName":"amy","discordUrl":"https://discord.com/users/323456789012345678\"><x"},
	 {"publicKey":"` + strings.Repeat("22", 32) + `","role":"owner","subject":"s5","displayName":null},
	 {"publicKey":"` + strings.Repeat("33", 32) + `","role":"owner","subject":"s6","displayName":"   "},
	 {"publicKey":"` + upper + `","role":"owner","subject":"s7","displayName":"upper"},
	 {"publicKey":"` + strings.Repeat("4", 63) + `","role":"owner","subject":"s8","displayName":"short"},
	 {"publicKey":"` + strings.Repeat("55", 32) + `","role":"owner","subject":"s9","displayName":"a\nb<i>"},
	 {"publicKey":"` + strings.Repeat("66", 32) + `","role":"owner","subject":"sa","displayName":"` + long + `"},
	 {"publicKey":"` + strings.Repeat("77", 32) + `","role":"viewer","subject":"sb","displayName":"x"},
	 {"publicKey":"` + strings.Repeat("88", 32) + `","role":"manager","subject":"sc","displayName":"onlymgr"}
	]}`
	got, err := parseMeshmapClaims([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(got)
	b := bytes.TrimSpace(buf.Bytes())
	want := `[` +
		`{"pubkey":"` + strings.Repeat("22", 32) + `","owner":{"name":"","discord":""},"managers":[]},` +
		`{"pubkey":"` + strings.Repeat("33", 32) + `","owner":{"name":"","discord":""},"managers":[]},` +
		`{"pubkey":"` + strings.Repeat("55", 32) + `","owner":{"name":"ab<i>","discord":""},"managers":[]},` +
		`{"pubkey":"` + strings.Repeat("66", 32) + `","owner":{"name":"` + strings.Repeat("é", 64) + `","discord":""},"managers":[]},` +
		`{"pubkey":"` + strings.Repeat("88", 32) + `","owner":null,"managers":[{"name":"onlymgr","discord":""}]},` +
		`{"pubkey":"` + strings.Repeat("ab", 32) + `","owner":{"name":"upper","discord":""},"managers":[]},` +
		`{"pubkey":"` + mmPK + `","owner":{"name":"erwinfiten","discord":"https://discord.com/users/123456789012345678"},"managers":[{"name":"amy","discord":""},{"name":"zed","discord":""},{"name":"","discord":""}]}` +
		`]`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
}

func TestParseMeshmapClaimsBadJSON(t *testing.T) {
	for _, raw := range []string{`{`, `[]`, `{"version":1}`, `null`, ``} {
		if _, err := parseMeshmapClaims([]byte(raw)); err == nil {
			t.Fatalf("no error for %q", raw)
		}
	}
}

// fakeMeshmap serves /api/v1/partner/claims; respond decides each answer.
// A request without the test secret gets the contract's 401.
func fakeMeshmap(t *testing.T, respond func(w http.ResponseWriter, r *http.Request, n int)) *httptest.Server {
	t.Helper()
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/partner/claims" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+mmSecret {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}
		respond(w, r, int(calls.Add(1)))
	}))
	t.Cleanup(ts.Close)
	return ts
}

const mmSecret = "test-secret-0123456789abcdef0123456789"

const oneOwner = `{"version":1,"claims":[{"publicKey":"` + mmPK + `","role":"owner","subject":"s1","displayName":"erwinfiten"}]}`

func TestMeshmapOwnersFetchETag(t *testing.T) {
	var sentINM string
	ts := fakeMeshmap(t, func(w http.ResponseWriter, r *http.Request, n int) {
		switch n {
		case 1:
			w.Header().Set("ETag", `"v1"`)
			w.Write([]byte(oneOwner))
		case 2:
			sentINM = r.Header.Get("If-None-Match")
			w.WriteHeader(http.StatusNotModified)
		default:
			w.Write([]byte(`{"version":1,"claims":[]}`))
		}
	})
	c := newMeshmapOwnersCache(ts.URL, mmSecret, time.Hour)
	ctx := context.Background()
	if err := c.fetchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.fetchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if sentINM != `"v1"` {
		t.Fatalf("If-None-Match = %q", sentINM)
	}
	if s := c.snapshot(); len(s.Nodes) != 1 || s.Nodes[0].Owner == nil || s.Nodes[0].Owner.Name != "erwinfiten" {
		t.Fatalf("304 changed the map: %+v", s.Nodes)
	}
	if err := c.fetchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if s := c.snapshot(); len(s.Nodes) != 0 {
		t.Fatalf("empty list kept owners: %+v", s.Nodes)
	}
}

func TestMeshmapOwnersFetchFailureKeepsMap(t *testing.T) {
	ts := fakeMeshmap(t, func(w http.ResponseWriter, r *http.Request, n int) {
		switch n {
		case 1:
			w.Write([]byte(oneOwner))
		case 2:
			w.WriteHeader(http.StatusInternalServerError)
		case 3:
			w.Write([]byte(`{"claims":[` + strings.Repeat(" ", maxMeshmapOwnersBytes) + `]}`))
		default:
			w.Write([]byte(`{`))
		}
	})
	c := newMeshmapOwnersCache(ts.URL, mmSecret, time.Hour)
	ctx := context.Background()
	if err := c.fetchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := c.fetchOnce(ctx); err == nil {
			t.Fatalf("fetch %d: no error", i+2)
		}
		if s := c.snapshot(); len(s.Nodes) != 1 {
			t.Fatalf("fetch %d dropped the map: %+v", i+2, s.Nodes)
		}
	}
}

// Review finding: a failed first fetch must not leave the list empty for a
// whole refresh interval; run retries with backoff until the first success.
func TestMeshmapOwnersRunRetriesFirstFetch(t *testing.T) {
	ts := fakeMeshmap(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if n < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Write([]byte(oneOwner))
	})
	c := newMeshmapOwnersCache(ts.URL, mmSecret, time.Hour)
	c.retryBase = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.run(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for len(c.snapshot().Nodes) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no owners 3s after start: first fetch not retried before the refresh interval")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestMeshmapOwnersRoute(t *testing.T) {
	srv, router := setupTestServer(t)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/meshmap/owners", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("disabled: status %d, want 404", w.Code)
	}

	ts := fakeMeshmap(t, func(w http.ResponseWriter, r *http.Request, n int) { w.Write([]byte(oneOwner)) })
	srv.meshmapOwners = newMeshmapOwnersCache(ts.URL, mmSecret, time.Hour)

	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/meshmap/owners", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("before first fetch: status %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"nodes":[]`) || !strings.Contains(w.Body.String(), `"fetchedAt":null`) {
		t.Fatalf("empty body = %s", w.Body.String())
	}

	if err := srv.meshmapOwners.fetchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/meshmap/owners", nil))
	var resp meshmapOwnersResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.Body.String(), mmSecret) {
		t.Fatal("secret leaked into the API answer")
	}
	if len(resp.Nodes) != 1 || resp.Nodes[0].Pubkey != mmPK || resp.FetchedAt == nil || resp.Source != ts.URL {
		t.Fatalf("resp = %s", w.Body.String())
	}
}

func TestMeshmapOwnersConfigDefaults(t *testing.T) {
	var cfg Config
	if err := json.Unmarshal([]byte(`{"meshmapOwners":{"enabled":true,"secret":" s3cret "}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	url, secret, refresh, ok := cfg.MeshmapOwners.resolve()
	if !ok || url != "https://meshmap.radio-actief.be" || secret != "s3cret" || refresh != time.Hour {
		t.Fatalf("defaults = %q %q %v %v", url, secret, refresh, ok)
	}
	cfg.MeshmapOwners.Secret = ""
	if _, _, _, ok := cfg.MeshmapOwners.resolve(); ok {
		t.Fatal("enabled without a secret resolved as on")
	}
	cfg.MeshmapOwners.Secret = "s3cret"
	cfg.MeshmapOwners.Enabled = false
	if _, _, _, ok := cfg.MeshmapOwners.resolve(); ok {
		t.Fatal("disabled config resolved as on")
	}
	var nilCfg *MeshmapOwnersConfig
	if _, _, _, ok := nilCfg.resolve(); ok {
		t.Fatal("nil config resolved as on")
	}
}
