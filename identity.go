// Resolves a Tinfoil API key to its owning Clerk user via the controlplane.
// Validation and identity resolution are the same call: a 401 response means
// the key is invalid; a 2xx response carries the user_id we map to a tenant.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
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

type HTTPResolver struct {
	baseURL string
	client  *http.Client
}

func NewHTTPResolver(baseURL string) *HTTPResolver {
	return &HTTPResolver{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 5 * time.Second},
	}
}

type identityRequest struct {
	APIKey string `json:"api_key"`
}

func (r *HTTPResolver) Resolve(ctx context.Context, apiKey string) (Identity, error) {
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
	return id, nil
}
