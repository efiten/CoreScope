package main

// Fork-local (meshmap owner): who runs a repeater, from the partner claims
// feed of meshmap.radio-actief.be (contract: docs/PARTNERS.md in the public
// radio-actief/RRY-Map-Bot repository). Fetched server to server with the
// partner secret every refreshMinutes into memory, served in bulk on
// GET /api/meshmap/owners; the Repeater info section shows the owner and
// managers. Each fetch replaces the whole list (the contract asks partners to
// drop whatever left the feed). Same shape as known_channels_cache.go: never
// blocks startup, fail-soft to the last good list, no DB.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

const (
	defaultMeshmapOwnersURL     = "https://meshmap.radio-actief.be"
	defaultMeshmapOwnersRefresh = time.Hour
	maxMeshmapOwnersBytes       = 10 * 1024 * 1024
	maxMeshmapOwnerNameRunes    = 64
)

// MeshmapOwnersConfig is the fork-local config block "meshmapOwners".
type MeshmapOwnersConfig struct {
	Enabled        bool   `json:"enabled"`
	URL            string `json:"url"`
	Secret         string `json:"secret"` // partner secret; server side only, never sent to a browser
	RefreshMinutes int    `json:"refreshMinutes"`
}

// resolve returns the base URL, secret and refresh interval, and false when
// the feature is off (nil block, enabled=false, or no secret: the feed
// answers 401 without one).
func (c *MeshmapOwnersConfig) resolve() (string, string, time.Duration, bool) {
	if c == nil || !c.Enabled {
		return "", "", 0, false
	}
	secret := strings.TrimSpace(c.Secret)
	if secret == "" {
		return "", "", 0, false
	}
	url := strings.TrimRight(strings.TrimSpace(c.URL), "/")
	if url == "" {
		url = defaultMeshmapOwnersURL
	}
	refresh := defaultMeshmapOwnersRefresh
	if c.RefreshMinutes > 0 {
		refresh = time.Duration(c.RefreshMinutes) * time.Minute
	}
	return url, secret, refresh, true
}

// meshmapPerson is one owner or manager. Name is empty when the person hides
// their name on meshmap; the UI then shows "claimed" without a name. Discord is
// a link to the person's Discord profile, only for a named person and only a
// https://discord.com/users/<id> address; otherwise empty.
type meshmapPerson struct {
	Name    string `json:"name"`
	Discord string `json:"discord"`
}

// meshmapNodeClaims is everyone who claimed one node: at most one owner, any
// number of managers (named first, by name; hidden names last).
type meshmapNodeClaims struct {
	Pubkey   string          `json:"pubkey"`
	Owner    *meshmapPerson  `json:"owner"`
	Managers []meshmapPerson `json:"managers"`
}

type meshmapOwnersResponse struct {
	Nodes     []meshmapNodeClaims `json:"nodes"`
	FetchedAt *time.Time          `json:"fetchedAt"`
	Source    string              `json:"source"`
}

// meshmapClaimsFeed mirrors the fields of /api/v1/partner/claims this
// feature reads. Claims is a pointer so a feed without the field (or a
// literal null) is an error instead of an empty list that wipes every owner.
type meshmapClaimsFeed struct {
	Claims *[]meshmapClaim `json:"claims"`
}

type meshmapClaim struct {
	PublicKey   string  `json:"publicKey"`
	Role        string  `json:"role"`
	DisplayName *string `json:"displayName"`
	DiscordURL  *string `json:"discordUrl"`
}

var meshmapPubkeyRe = regexp.MustCompile(`^[0-9a-f]{64}$`)
var meshmapDiscordRe = regexp.MustCompile(`^https://discord\.com/users/[0-9]{15,21}$`)

// cleanMeshmapDiscord keeps only a Discord profile address.
func cleanMeshmapDiscord(p *string) string {
	if p == nil || !meshmapDiscordRe.MatchString(*p) {
		return ""
	}
	return *p
}

// cleanMeshmapName drops control characters, trims, and cuts at 64 runes.
func cleanMeshmapName(p *string) string {
	if p == nil {
		return ""
	}
	name := strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, *p))
	if r := []rune(name); len(r) > maxMeshmapOwnerNameRunes {
		name = string(r[:maxMeshmapOwnerNameRunes])
	}
	return name
}

// parseMeshmapClaims groups the feed per node, sorted by pubkey. Claims with
// an invalid key or an unknown role are skipped.
func parseMeshmapClaims(raw []byte) ([]meshmapNodeClaims, error) {
	var feed meshmapClaimsFeed
	if err := json.Unmarshal(raw, &feed); err != nil {
		return nil, fmt.Errorf("decode meshmap claims: %w", err)
	}
	if feed.Claims == nil {
		return nil, errors.New("meshmap claims feed has no claims list")
	}
	byKey := make(map[string]*meshmapNodeClaims)
	for _, c := range *feed.Claims {
		pk := strings.ToLower(strings.TrimSpace(c.PublicKey))
		if !meshmapPubkeyRe.MatchString(pk) || (c.Role != "owner" && c.Role != "manager") {
			continue
		}
		n := byKey[pk]
		if n == nil {
			n = &meshmapNodeClaims{Pubkey: pk, Managers: []meshmapPerson{}}
			byKey[pk] = n
		}
		person := meshmapPerson{Name: cleanMeshmapName(c.DisplayName)}
		if person.Name != "" {
			person.Discord = cleanMeshmapDiscord(c.DiscordURL)
		}
		if c.Role == "owner" {
			if n.Owner == nil {
				n.Owner = &person
			}
		} else {
			n.Managers = append(n.Managers, person)
		}
	}
	out := make([]meshmapNodeClaims, 0, len(byKey))
	for _, n := range byKey {
		m := n.Managers
		sort.SliceStable(m, func(i, j int) bool {
			if (m[i].Name == "") != (m[j].Name == "") {
				return m[j].Name == ""
			}
			return strings.ToLower(m[i].Name) < strings.ToLower(m[j].Name)
		})
		out = append(out, *n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pubkey < out[j].Pubkey })
	return out, nil
}

type meshmapOwnersSnap struct {
	nodes     []meshmapNodeClaims
	fetchedAt time.Time
}

type meshmapOwnersCache struct {
	ptr     atomic.Pointer[meshmapOwnersSnap]
	baseURL string
	secret  string
	refresh time.Duration
	client  *http.Client
	// retryBase is the first wait before retrying a failed first fetch; it
	// doubles per attempt, capped at refresh (tests shorten it).
	retryBase time.Duration

	mu      sync.Mutex // guards etag and failing (fetchOnce runs from one goroutine, tests call it directly)
	etag    string
	failing bool
}

func newMeshmapOwnersCache(baseURL, secret string, refresh time.Duration) *meshmapOwnersCache {
	if refresh <= 0 {
		refresh = defaultMeshmapOwnersRefresh
	}
	return &meshmapOwnersCache{
		baseURL:   strings.TrimRight(baseURL, "/"),
		secret:    secret,
		refresh:   refresh,
		client:    &http.Client{Timeout: 30 * time.Second},
		retryBase: time.Minute,
	}
}

func (c *meshmapOwnersCache) snapshot() meshmapOwnersResponse {
	resp := meshmapOwnersResponse{Nodes: []meshmapNodeClaims{}, Source: c.baseURL}
	if s := c.ptr.Load(); s != nil {
		resp.Nodes = s.nodes
		at := s.fetchedAt
		resp.FetchedAt = &at
	}
	return resp
}

// fetchOnce does one conditional fetch; on any failure the previous list stays.
func (c *meshmapOwnersCache) fetchOnce(ctx context.Context) error {
	err := c.fetch(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil && !c.failing {
		log.Printf("[meshmap] claims feed fetch failed, keeping the last list: %v", err)
	} else if err == nil && c.failing {
		log.Printf("[meshmap] claims feed fetch recovered")
	}
	c.failing = err != nil
	return err
}

func (c *meshmapOwnersCache) fetch(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/partner/claims", nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "CoreScope-MeshmapOwners/1.0 (+https://github.com/efiten/CoreScope)")
	req.Header.Set("Authorization", "Bearer "+c.secret)
	c.mu.Lock()
	if c.etag != "" && c.ptr.Load() != nil {
		req.Header.Set("If-None-Match", c.etag)
	}
	c.mu.Unlock()
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		if s := c.ptr.Load(); s != nil {
			c.ptr.Store(&meshmapOwnersSnap{nodes: s.nodes, fetchedAt: time.Now().UTC()})
		}
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("meshmap status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMeshmapOwnersBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxMeshmapOwnersBytes {
		return errors.New("meshmap claims feed larger than 10 MB")
	}
	nodes, err := parseMeshmapClaims(body)
	if err != nil {
		return err
	}
	if prev := c.ptr.Load(); prev == nil || len(prev.nodes) != len(nodes) {
		log.Printf("[meshmap] claims feed: %d nodes", len(nodes))
	}
	c.ptr.Store(&meshmapOwnersSnap{nodes: nodes, fetchedAt: time.Now().UTC()})
	c.mu.Lock()
	c.etag = resp.Header.Get("ETag")
	c.mu.Unlock()
	return nil
}

// run starts the background refresh: one fetch now, retried with backoff
// (retryBase, doubling, capped at refresh) until the first success, then
// one every refresh. Without the retries a restart during a meshmap hiccup
// would show no owners for a whole refresh interval.
func (c *meshmapOwnersCache) run(ctx context.Context) {
	go func() {
		wait := c.retryBase
		for c.fetchOnce(ctx) != nil && c.ptr.Load() == nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			if wait *= 2; wait > c.refresh {
				wait = c.refresh
			}
		}
		t := time.NewTicker(c.refresh)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				_ = c.fetchOnce(ctx)
			}
		}
	}()
}

// handleMeshmapOwners — GET /api/meshmap/owners. 404 when the feature is off
// (the route is registered before the config-driven caches are built).
func (s *Server) handleMeshmapOwners(w http.ResponseWriter, r *http.Request) {
	if s.meshmapOwners == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, s.meshmapOwners.snapshot())
}
