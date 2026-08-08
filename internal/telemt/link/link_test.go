package link

import "testing"

// hex("petrovich.ru") == 706574726f766963682e7275
const petrovichHex = "706574726f766963682e7275"

func TestFakeTLS(t *testing.T) {
	secret := "00112233445566778899aabbccddeeff"
	got, err := FakeTLS("1.2.3.4", 443, secret, "petrovich.ru")
	if err != nil {
		t.Fatalf("FakeTLS() error = %v", err)
	}
	want := "tg://proxy?server=1.2.3.4&port=443&secret=ee" + secret + petrovichHex
	if got != want {
		t.Errorf("FakeTLS() =\n  %q\nwant\n  %q", got, want)
	}
}

func TestFakeTLSHTTPS(t *testing.T) {
	secret := "00112233445566778899aabbccddeeff"
	got, err := FakeTLSHTTPS("1.2.3.4", 443, secret, "petrovich.ru")
	if err != nil {
		t.Fatalf("FakeTLSHTTPS() error = %v", err)
	}
	want := "https://t.me/proxy?server=1.2.3.4&port=443&secret=ee" + secret + petrovichHex
	if got != want {
		t.Errorf("FakeTLSHTTPS() =\n  %q\nwant\n  %q", got, want)
	}
}

func TestFakeTLSLowercasesSecret(t *testing.T) {
	got, err := FakeTLS("h", 443, "00112233445566778899AABBCCDDEEFF", "a.com")
	if err != nil {
		t.Fatalf("FakeTLS() error = %v", err)
	}
	want := "tg://proxy?server=h&port=443&secret=ee00112233445566778899aabbccddeeff612e636f6d"
	if got != want {
		t.Errorf("FakeTLS() = %q, want %q", got, want)
	}
}

func TestFakeTLSRejectsBadInput(t *testing.T) {
	cases := []struct {
		name   string
		host   string
		port   int
		secret string
		domain string
	}{
		{"short secret", "h", 443, "00112233", "a.com"},
		{"non-hex secret", "h", 443, "zz112233445566778899aabbccddeeff", "a.com"},
		{"empty domain", "h", 443, "00112233445566778899aabbccddeeff", ""},
		{"empty host", "", 443, "00112233445566778899aabbccddeeff", "a.com"},
		{"port zero", "h", 0, "00112233445566778899aabbccddeeff", "a.com"},
		{"port too high", "h", 70000, "00112233445566778899aabbccddeeff", "a.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := FakeTLS(tc.host, tc.port, tc.secret, tc.domain); err == nil {
				t.Fatal("FakeTLS() error = nil, want error")
			}
		})
	}
}

func TestFakeTLSEscapesHost(t *testing.T) {
	// IPv6 literals contain colons, which must survive as query-safe text.
	got, err := FakeTLS("2001:db8::1", 443, "00112233445566778899aabbccddeeff", "a.com")
	if err != nil {
		t.Fatalf("FakeTLS() error = %v", err)
	}
	want := "tg://proxy?server=2001%3Adb8%3A%3A1&port=443&secret=ee00112233445566778899aabbccddeeff612e636f6d"
	if got != want {
		t.Errorf("FakeTLS() = %q, want %q", got, want)
	}
}
