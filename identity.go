// Resolves a Tinfoil API key to its owning Clerk user via the controlplane.
// Validation and identity resolution are the same call: a 401 response means
// the key is invalid; a 2xx response carries the user_id we map to a tenant.
//
// Positive lookups are LRU-cached for identityCacheTTL keyed by sha256(apiKey)
package main

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Identity struct {
	UserID string `json:"user_id"`
}

type Resolver interface {
	Resolve(ctx context.Context, apiKey string) (Identity, error)
}

var (
	ErrInvalidToken        = errors.New("invalid api key")
	ErrUpstreamUnavailable = errors.New("identity service unavailable")
)

// userIDPattern bounds Clerk's user_xxx shape (and anything similarly safe)
// so the derived tenant id "user-<userID>" always satisfies the sidecar's
// X-Tinfoil-Tenant-Id regex [A-Za-z0-9_-]{1,64}. We reserve 5 chars for the
// "user-" prefix → cap UserID at 59.
var userIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,59}$`)

const (
	identityCacheTTL     = 5 * time.Minute
	identityCacheMaxSize = 1024
)

type HTTPResolver struct {
	baseURL string
	client  *http.Client
	cache   *identityCache
}

func NewHTTPResolver(baseURL string) *HTTPResolver {
	return &HTTPResolver{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 5 * time.Second},
		cache:   newIdentityCache(identityCacheTTL, identityCacheMaxSize),
	}
}

type identityRequest struct {
	APIKey string `json:"api_key"`
}

func (r *HTTPResolver) Resolve(ctx context.Context, apiKey string) (Identity, error) {
	if id, ok := r.cache.get(apiKey); ok {
		return id, nil
	}
	id, err := r.resolveUpstream(ctx, apiKey)
	if err != nil {
		return Identity{}, err
	}
	r.cache.put(apiKey, id)
	return id, nil
}

func (r *HTTPResolver) resolveUpstream(ctx context.Context, apiKey string) (Identity, error) {
	body, err := json.Marshal(identityRequest{APIKey: apiKey})
	if err != nil {
		return Identity{}, fmt.Errorf("marshal identity request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/api/internal/key-identity", bytes.NewReader(body))
	if err != nil {
		return Identity{}, fmt.Errorf("build identity request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %v", ErrUpstreamUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return Identity{}, ErrInvalidToken
	}
	if resp.StatusCode/100 != 2 {
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return Identity{}, fmt.Errorf("%w: status %d: %s", ErrUpstreamUnavailable, resp.StatusCode, strings.TrimSpace(string(preview)))
	}

	var id Identity
	if err := json.NewDecoder(resp.Body).Decode(&id); err != nil {
		return Identity{}, fmt.Errorf("%w: decode response: %v", ErrUpstreamUnavailable, err)
	}
	if id.UserID == "" {
		return Identity{}, fmt.Errorf("%w: missing user_id in response", ErrUpstreamUnavailable)
	}
	if !userIDPattern.MatchString(id.UserID) {
		return Identity{}, fmt.Errorf("%w: user_id %q has unexpected shape", ErrUpstreamUnavailable, id.UserID)
	}
	return id, nil
}

// identityCache is a small LRU keyed by sha256(apiKey). Plain bearers never
// rest in process memory longer than the time it takes to hash them.
type identityCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	maxSize int
	ll      *list.List
	items   map[[32]byte]*list.Element
}

type cacheEntry struct {
	key       [32]byte
	id        Identity
	expiresAt time.Time
}

func newIdentityCache(ttl time.Duration, maxSize int) *identityCache {
	return &identityCache{
		ttl:     ttl,
		maxSize: maxSize,
		ll:      list.New(),
		items:   make(map[[32]byte]*list.Element, maxSize),
	}
}

func (c *identityCache) get(apiKey string) (Identity, bool) {
	h := sha256.Sum256([]byte(apiKey))
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[h]
	if !ok {
		return Identity{}, false
	}
	e := el.Value.(*cacheEntry)
	if time.Now().After(e.expiresAt) {
		c.ll.Remove(el)
		delete(c.items, h)
		return Identity{}, false
	}
	c.ll.MoveToFront(el)
	return e.id, true
}

func (c *identityCache) put(apiKey string, id Identity) {
	h := sha256.Sum256([]byte(apiKey))
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[h]; ok {
		e := el.Value.(*cacheEntry)
		e.id = id
		e.expiresAt = time.Now().Add(c.ttl)
		c.ll.MoveToFront(el)
		return
	}
	if c.ll.Len() >= c.maxSize {
		if oldest := c.ll.Back(); oldest != nil {
			c.ll.Remove(oldest)
			delete(c.items, oldest.Value.(*cacheEntry).key)
		}
	}
	c.items[h] = c.ll.PushFront(&cacheEntry{key: h, id: id, expiresAt: time.Now().Add(c.ttl)})
}
