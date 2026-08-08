// Package link builds Telegram proxy links for telemt's fake-TLS ("ee") mode.
//
// A fake-TLS secret is the literal prefix "ee", followed by the 32-hex user
// secret, followed by the hex encoding of the SNI domain's raw bytes.
package link

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// FakeTLS returns a tg://proxy link for the given proxy parameters.
func FakeTLS(host string, port int, secretHex, domain string) (string, error) {
	return build("tg://proxy", host, port, secretHex, domain)
}

// FakeTLSHTTPS returns the https://t.me/proxy form of the same link, which is
// what you paste into a chat so it renders as a tappable button.
func FakeTLSHTTPS(host string, port int, secretHex, domain string) (string, error) {
	return build("https://t.me/proxy", host, port, secretHex, domain)
}

func build(base, host string, port int, secretHex, domain string) (string, error) {
	secret, err := normalizeSecret(secretHex)
	if err != nil {
		return "", err
	}
	if host == "" {
		return "", fmt.Errorf("link: host is empty")
	}
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("link: port %d out of range 1-65535", port)
	}
	if domain == "" {
		return "", fmt.Errorf("link: domain is empty")
	}

	// Built by concatenation rather than url.Values.Encode, which sorts keys
	// alphabetically: every other MTProto tool emits server, port, secret in
	// that order, and keeping it makes links diffable against telemt's own
	// output. The secret is pure hex, so it needs no escaping.
	return base + "?server=" + url.QueryEscape(host) +
		"&port=" + strconv.Itoa(port) +
		"&secret=ee" + secret + hex.EncodeToString([]byte(domain)), nil
}

// normalizeSecret validates a 32-character hex secret and lowercases it.
func normalizeSecret(s string) (string, error) {
	if len(s) != 32 {
		return "", fmt.Errorf("link: secret must be 32 hex chars, got %d", len(s))
	}
	s = strings.ToLower(s)
	if _, err := hex.DecodeString(s); err != nil {
		return "", fmt.Errorf("link: secret is not hex: %w", err)
	}
	return s, nil
}
