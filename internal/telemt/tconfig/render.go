// Package tconfig renders a telemt config.toml for a single panel-managed proxy.
//
// Every proxy container runs exactly one telemt user, named by Spec.Username,
// so all the per-user maps in [access] have exactly one entry.
package tconfig

import (
	_ "embed"
	"encoding/hex"
	"fmt"
	"strings"
	"text/template"
)

//go:embed config.toml.tmpl
var tmplSrc string

var tmpl = template.Must(template.New("config.toml").Parse(tmplSrc))

// Spec is everything that varies between one proxy's config and another's.
type Spec struct {
	Username          string
	Secret            string
	Port              int
	TLSDomain         string
	AdTag             string
	APIToken          string
	APIWhitelist      []string
	PublicHost        string
	DataQuotaBytes    *uint64
	ExpirationRFC3339 *string
	MaxTCPConns       *int
	MaxUniqueIPs      *int
}

// Render produces the complete config.toml text for one proxy.
func Render(s Spec) (string, error) {
	if err := s.validate(); err != nil {
		return "", err
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, s); err != nil {
		return "", fmt.Errorf("tconfig: render: %w", err)
	}
	out := b.String()
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out, nil
}

func (s Spec) validate() error {
	if s.Username == "" {
		return fmt.Errorf("tconfig: username is empty")
	}
	if err := hex32("secret", s.Secret); err != nil {
		return err
	}
	if s.AdTag != "" {
		if err := hex32("ad_tag", s.AdTag); err != nil {
			return err
		}
	}
	if s.Port < 1 || s.Port > 65535 {
		return fmt.Errorf("tconfig: port %d out of range 1-65535", s.Port)
	}
	if s.TLSDomain == "" {
		return fmt.Errorf("tconfig: tls_domain is empty")
	}
	if s.APIToken == "" {
		return fmt.Errorf("tconfig: api token is empty")
	}
	if len(s.APIWhitelist) == 0 {
		return fmt.Errorf("tconfig: api whitelist is empty, which would expose the control API")
	}
	// Guard against a quoted string breaking out of the TOML value. The
	// whitelist is checked too: it reaches the same quoted-string context.
	values := []string{s.TLSDomain, s.APIToken, s.PublicHost, s.Username}
	values = append(values, s.APIWhitelist...)
	for _, v := range values {
		if strings.ContainsAny(v, "\"\n\\") {
			return fmt.Errorf("tconfig: value %q contains a quote, backslash or newline", v)
		}
	}
	return nil
}

func hex32(field, v string) error {
	if len(v) != 32 {
		return fmt.Errorf("tconfig: %s must be 32 hex chars, got %d", field, len(v))
	}
	if _, err := hex.DecodeString(v); err != nil {
		return fmt.Errorf("tconfig: %s is not hex: %w", field, err)
	}
	return nil
}
