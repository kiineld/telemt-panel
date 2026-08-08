package tconfig

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func minimalSpec() Spec {
	return Spec{
		Username:     "user",
		Secret:       "00112233445566778899aabbccddeeff",
		Port:         443,
		TLSDomain:    "petrovich.ru",
		APIToken:     "tok_abc",
		APIWhitelist: []string{"172.28.0.0/16"},
	}
}

func fullSpec() Spec {
	s := minimalSpec()
	s.AdTag = "ffeeddccbbaa99887766554433221100"
	s.PublicHost = "proxy.example.com"
	quota := uint64(107374182400)
	exp := "2027-01-01T00:00:00Z"
	conns := 200
	ips := 8
	s.DataQuotaBytes = &quota
	s.ExpirationRFC3339 = &exp
	s.MaxTCPConns = &conns
	s.MaxUniqueIPs = &ips
	return s
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update to create)", path, err)
	}
	if got != string(want) {
		t.Errorf("rendered config does not match %s:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestRenderMinimal(t *testing.T) {
	got, err := Render(minimalSpec())
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	assertGolden(t, "minimal.golden.toml", got)
}

func TestRenderFull(t *testing.T) {
	got, err := Render(fullSpec())
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	assertGolden(t, "full.golden.toml", got)
}

func TestRenderOmitsEmptyOptionals(t *testing.T) {
	got, err := Render(minimalSpec())
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	for _, absent := range []string{
		"user_ad_tags", "user_data_quota", "user_expirations",
		"user_max_tcp_conns", "user_max_unique_ips", "public_host",
	} {
		if strings.Contains(got, absent) {
			t.Errorf("minimal config unexpectedly contains %q:\n%s", absent, got)
		}
	}
}

func TestRenderValidation(t *testing.T) {
	cases := map[string]func(*Spec){
		"short secret":   func(s *Spec) { s.Secret = "abc" },
		"non-hex secret": func(s *Spec) { s.Secret = "zz112233445566778899aabbccddeeff" },
		"port zero":      func(s *Spec) { s.Port = 0 },
		"port high":      func(s *Spec) { s.Port = 70000 },
		"empty domain":   func(s *Spec) { s.TLSDomain = "" },
		"empty user":     func(s *Spec) { s.Username = "" },
		"bad ad tag":     func(s *Spec) { s.AdTag = "nope" },
		"empty token":    func(s *Spec) { s.APIToken = "" },
		"empty whitelist": func(s *Spec) {
			s.APIWhitelist = nil
		},
		"quote injection": func(s *Spec) { s.TLSDomain = `a.com" evil = "1` },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := minimalSpec()
			mutate(&s)
			if _, err := Render(s); err == nil {
				t.Fatal("Render() error = nil, want error")
			}
		})
	}
}
