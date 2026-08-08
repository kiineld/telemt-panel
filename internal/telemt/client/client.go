// Package client is a typed client for telemt's Control API (/v1).
//
// It covers only the endpoints the panel calls: health, list users, and patch
// one user. Users are created by writing config.toml before the container
// starts, so there is deliberately no POST /v1/users here.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	token   string
	hc      *http.Client
}

// New returns a client for a telemt Control API rooted at baseURL, e.g.
// "http://telemt-abc123:9091". token is sent verbatim as Authorization.
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		hc:      &http.Client{Timeout: 10 * time.Second},
	}
}

type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("telemt api: http %d", e.Status)
	}
	return fmt.Sprintf("telemt api: %s (http %d): %s", e.Code, e.Status, e.Message)
}

type envelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type TLSDomainLink struct {
	Domain string `json:"domain"`
	Link   string `json:"link"`
}

type UserLinks struct {
	Classic    []string        `json:"classic"`
	Secure     []string        `json:"secure"`
	TLS        []string        `json:"tls"`
	TLSDomains []TLSDomainLink `json:"tls_domains"`
}

type UserInfo struct {
	Username            string    `json:"username"`
	Enabled             bool      `json:"enabled"`
	InRuntime           bool      `json:"in_runtime"`
	UserAdTag           *string   `json:"user_ad_tag"`
	CurrentConnections  uint64    `json:"current_connections"`
	ActiveUniqueIPs     int       `json:"active_unique_ips"`
	ActiveUniqueIPsList []string  `json:"active_unique_ips_list"`
	TotalOctets         uint64    `json:"total_octets"`
	DataQuotaBytes      *uint64   `json:"data_quota_bytes"`
	ExpirationRFC3339   *string   `json:"expiration_rfc3339"`
	MaxTCPConns         *int      `json:"max_tcp_conns"`
	MaxUniqueIPs        *int      `json:"max_unique_ips"`
	Links               UserLinks `json:"links"`
}

// Opt is a JSON Merge Patch field with three states: unset (omitted from the
// payload entirely), null (explicitly removes the server-side override), or a
// concrete value. A plain *T cannot express this: encoding/json's omitempty
// only tests pointer nil-ness, so there is no way to make a nil *T serialize
// as an explicit JSON null while a genuinely-omitted field serializes as
// nothing at all. Opt separates "was this field set at all" (set) from "what
// value, if any, does it carry" (val).
type Opt[T any] struct {
	set bool
	val *T
}

// Value returns a set Opt carrying a concrete value.
func Value[T any](v T) Opt[T] { return Opt[T]{set: true, val: &v} }

// Null returns a set Opt that marshals as an explicit JSON null, removing the
// server-side override for the field.
func Null[T any]() Opt[T] { return Opt[T]{set: true, val: nil} }

// From returns a set Opt from a pointer; a nil p means null.
func From[T any](p *T) Opt[T] { return Opt[T]{set: true, val: p} }

// IsSet reports whether the field was set at all (to a value or to null), as
// opposed to being left absent from the payload.
func (o Opt[T]) IsSet() bool { return o.set }

// PatchUser follows JSON Merge Patch semantics: a field left unset (the zero
// Opt) is omitted from the payload and leaves the server-side value
// unchanged. A field set to Null() is sent as an explicit JSON null, which
// removes the server-side override. A field set to Value(v) is sent as v.
type PatchUser struct {
	UserAdTag         Opt[string]
	DataQuotaBytes    Opt[uint64]
	ExpirationRFC3339 Opt[string]
	MaxTCPConns       Opt[int]
	MaxUniqueIPs      Opt[int]
	Enabled           Opt[bool]
}

// MarshalJSON builds the merge-patch payload from only the fields where
// IsSet() is true. encoding/json's omitempty does nothing useful on
// struct-typed fields (a zero-value Opt is not "empty" to the encoder), so a
// custom marshaller is what actually implements the omit/null/value tri-state.
func (p PatchUser) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, 6)
	if p.UserAdTag.IsSet() {
		m["user_ad_tag"] = p.UserAdTag.val
	}
	if p.DataQuotaBytes.IsSet() {
		m["data_quota_bytes"] = p.DataQuotaBytes.val
	}
	if p.ExpirationRFC3339.IsSet() {
		m["expiration_rfc3339"] = p.ExpirationRFC3339.val
	}
	if p.MaxTCPConns.IsSet() {
		m["max_tcp_conns"] = p.MaxTCPConns.val
	}
	if p.MaxUniqueIPs.IsSet() {
		m["max_unique_ips"] = p.MaxUniqueIPs.val
	}
	if p.Enabled.IsSet() {
		m["enabled"] = p.Enabled.val
	}
	return json.Marshal(m)
}

func (c *Client) Health(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodGet, "/v1/health", nil)
	return err
}

func (c *Client) Users(ctx context.Context) ([]UserInfo, error) {
	raw, err := c.do(ctx, http.MethodGet, "/v1/users", nil)
	if err != nil {
		return nil, err
	}
	var users []UserInfo
	if err := json.Unmarshal(raw, &users); err != nil {
		return nil, fmt.Errorf("telemt api: decode users: %w", err)
	}
	return users, nil
}

func (c *Client) PatchUser(ctx context.Context, username string, p PatchUser) (UserInfo, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return UserInfo{}, fmt.Errorf("telemt api: encode patch: %w", err)
	}
	raw, err := c.do(ctx, http.MethodPatch, "/v1/users/"+url.PathEscape(username), body)
	if err != nil {
		return UserInfo{}, err
	}
	var u UserInfo
	if err := json.Unmarshal(raw, &u); err != nil {
		return UserInfo{}, fmt.Errorf("telemt api: decode user: %w", err)
	}
	return u, nil
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) (json.RawMessage, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return nil, fmt.Errorf("telemt api: build request: %w", err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("telemt api: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("telemt api: read body: %w", err)
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		// Not a telemt envelope at all — a proxy in front, or a crash page.
		return nil, &APIError{Status: resp.StatusCode, Code: "invalid_response",
			Message: "response was not a telemt JSON envelope"}
	}
	if !env.OK || resp.StatusCode >= 400 {
		e := &APIError{Status: resp.StatusCode}
		if env.Error != nil {
			e.Code, e.Message = env.Error.Code, env.Error.Message
		}
		return nil, e
	}
	return env.Data, nil
}
