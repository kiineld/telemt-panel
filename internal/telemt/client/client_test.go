package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// handlerTransport dispatches a request straight to an http.Handler in the
// same process. httptest.NewServer would be the obvious choice, but it binds a
// TCP port, which some sandboxed CI environments deny; this exercises the same
// request-building and envelope-decoding paths without a socket.
type handlerTransport struct{ h http.Handler }

func (t handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	t.h.ServeHTTP(rec, r)
	return rec.Result(), nil
}

func newTestClient(t *testing.T, token string, h http.HandlerFunc) *Client {
	t.Helper()
	c := New("http://telemt.test", token)
	c.hc = &http.Client{Transport: handlerTransport{h: h}}
	return c
}

func TestHealthOK(t *testing.T) {
	c := newTestClient(t, "tok", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/health" {
			t.Errorf("path = %q, want /v1/health", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "tok" {
			t.Errorf("Authorization = %q, want %q", got, "tok")
		}
		_, _ = w.Write([]byte(`{"ok":true,"data":{"status":"ok","read_only":false}}`))
	})
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health() error = %v", err)
	}
}

func TestHealthUnauthorized(t *testing.T) {
	c := newTestClient(t, "wrong", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"unauthorized","message":"bad token"}}`))
	})
	err := c.Health(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("Health() error = %v, want *APIError", err)
	}
	if apiErr.Code != "unauthorized" {
		t.Errorf("Code = %q, want %q", apiErr.Code, "unauthorized")
	}
	if apiErr.Status != http.StatusUnauthorized {
		t.Errorf("Status = %d, want 401", apiErr.Status)
	}
}

func TestUsers(t *testing.T) {
	body := `{"ok":true,"revision":"abc","data":[{
		"username":"user","enabled":true,"in_runtime":true,
		"user_ad_tag":"ffeeddccbbaa99887766554433221100",
		"current_connections":7,"active_unique_ips":3,
		"active_unique_ips_list":["1.2.3.4","5.6.7.8","::1"],
		"total_octets":123456,"data_quota_bytes":1000000,
		"max_tcp_conns":200,
		"links":{"classic":[],"secure":[],
			"tls":["tg://proxy?server=1.2.3.4&port=443&secret=eeaa"],
			"tls_domains":[{"domain":"petrovich.ru","link":"tg://proxy?server=1.2.3.4&port=443&secret=eebb"}]}
	}]}`
	c := newTestClient(t, "tok", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/users" {
			t.Errorf("path = %q, want /v1/users", r.URL.Path)
		}
		_, _ = w.Write([]byte(body))
	})

	users, err := c.Users(context.Background())
	if err != nil {
		t.Fatalf("Users() error = %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("len(users) = %d, want 1", len(users))
	}
	u := users[0]
	if u.Username != "user" {
		t.Errorf("Username = %q", u.Username)
	}
	if u.ActiveUniqueIPs != 3 {
		t.Errorf("ActiveUniqueIPs = %d, want 3", u.ActiveUniqueIPs)
	}
	if len(u.ActiveUniqueIPsList) != 3 {
		t.Errorf("len(ActiveUniqueIPsList) = %d, want 3", len(u.ActiveUniqueIPsList))
	}
	if u.CurrentConnections != 7 {
		t.Errorf("CurrentConnections = %d, want 7", u.CurrentConnections)
	}
	if u.TotalOctets != 123456 {
		t.Errorf("TotalOctets = %d", u.TotalOctets)
	}
	if u.DataQuotaBytes == nil || *u.DataQuotaBytes != 1000000 {
		t.Errorf("DataQuotaBytes = %v, want 1000000", u.DataQuotaBytes)
	}
	if u.MaxUniqueIPs != nil {
		t.Errorf("MaxUniqueIPs = %v, want nil (absent in payload)", u.MaxUniqueIPs)
	}
	if len(u.Links.TLS) != 1 {
		t.Fatalf("len(Links.TLS) = %d, want 1", len(u.Links.TLS))
	}
	if len(u.Links.TLSDomains) != 1 || u.Links.TLSDomains[0].Domain != "petrovich.ru" {
		t.Errorf("Links.TLSDomains = %+v", u.Links.TLSDomains)
	}
}

func TestPatchUserSendsMergePatch(t *testing.T) {
	var got map[string]any
	c := newTestClient(t, "tok", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %q, want PATCH", r.Method)
		}
		if r.URL.Path != "/v1/users/user" {
			t.Errorf("path = %q, want /v1/users/user", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"data":{"username":"user","enabled":true}}`))
	})

	u, err := c.PatchUser(context.Background(), "user", PatchUser{DataQuotaBytes: Value(uint64(500))})
	if err != nil {
		t.Fatalf("PatchUser() error = %v", err)
	}
	if u.Username != "user" {
		t.Errorf("Username = %q", u.Username)
	}
	if v, ok := got["data_quota_bytes"]; !ok || v.(float64) != 500 {
		t.Errorf("body[data_quota_bytes] = %v, want 500", v)
	}
	if _, ok := got["max_tcp_conns"]; ok {
		t.Error("body contains max_tcp_conns; omitted fields must not be sent")
	}
}

func TestPatchUserMarshalUnsetFieldIsAbsent(t *testing.T) {
	body, err := json.Marshal(PatchUser{})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(m) != 0 {
		t.Errorf("marshalled unset PatchUser = %s, want {}", body)
	}
}

func TestPatchUserMarshalNullFieldIsPresentAndNull(t *testing.T) {
	body, err := json.Marshal(PatchUser{DataQuotaBytes: Null[uint64]()})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	raw, ok := m["data_quota_bytes"]
	if !ok {
		t.Fatalf("marshalled = %s, want key data_quota_bytes present", body)
	}
	if string(raw) != "null" {
		t.Errorf("data_quota_bytes = %s, want null", raw)
	}
}

func TestPatchUserMarshalValueFieldIsValue(t *testing.T) {
	body, err := json.Marshal(PatchUser{MaxTCPConns: Value(200)})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if v, ok := m["max_tcp_conns"]; !ok || v.(float64) != 200 {
		t.Errorf("max_tcp_conns = %v, want 200", v)
	}
}

func TestOptFromNilMeansNull(t *testing.T) {
	var p *string
	o := From(p)
	if !o.IsSet() {
		t.Fatal("From(nil).IsSet() = false, want true")
	}
	body, err := json.Marshal(PatchUser{UserAdTag: o})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if string(m["user_ad_tag"]) != "null" {
		t.Errorf("user_ad_tag = %s, want null", m["user_ad_tag"])
	}
}

func TestPatchUserEscapesUsername(t *testing.T) {
	c := newTestClient(t, "tok", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/users/a b" {
			t.Errorf("path = %q, want %q", r.URL.Path, "/v1/users/a b")
		}
		_, _ = w.Write([]byte(`{"ok":true,"data":{"username":"a b"}}`))
	})
	if _, err := c.PatchUser(context.Background(), "a b", PatchUser{}); err != nil {
		t.Fatalf("PatchUser() error = %v", err)
	}
}

func TestNonJSONBodyIsAnError(t *testing.T) {
	c := newTestClient(t, "tok", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>nginx</html>"))
	})
	err := c.Health(context.Background())
	if err == nil {
		t.Fatal("Health() error = nil, want error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadGateway {
		t.Fatalf("error = %v, want *APIError with status 502", err)
	}
}
