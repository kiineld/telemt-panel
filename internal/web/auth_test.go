package web

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiineld/telemt-panel/internal/store"
)

func newAuth(t *testing.T) (*Auth, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewAuth(st), st
}

func TestHashAndVerify(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if !strings.HasPrefix(h, "$argon2id$") {
		t.Errorf("hash = %q, want an argon2id PHC string", h)
	}
	if !VerifyPassword(h, "correct horse battery") {
		t.Error("VerifyPassword() = false for the correct password")
	}
	if VerifyPassword(h, "wrong") {
		t.Error("VerifyPassword() = true for a wrong password")
	}
}

func TestHashIsSalted(t *testing.T) {
	a, _ := HashPassword("same")
	b, _ := HashPassword("same")
	if a == b {
		t.Error("two hashes of the same password are identical; the salt is not random")
	}
}

func TestVerifyRejectsGarbage(t *testing.T) {
	for _, h := range []string{"", "notahash", "$argon2id$v=19$m=1", "$argon2id$v=19$m=65536,t=1,p=1$!!!$!!!"} {
		if VerifyPassword(h, "x") {
			t.Errorf("VerifyPassword(%q) = true, want false", h)
		}
	}
}

func TestVerifyRejectsDangerousParams(t *testing.T) {
	// t=0 and p=0 make golang.org/x/crypto/argon2 panic outright; a huge m=
	// value (still well within uint32 range) would otherwise make it attempt
	// a multi-terabyte allocation. VerifyPassword must reject all of these
	// instead of crashing the process.
	cases := []string{
		"$argon2id$v=19$m=65536,t=0,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=65536,t=1,p=0$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=4000000000,t=1,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	}
	for _, h := range cases {
		if VerifyPassword(h, "x") {
			t.Errorf("VerifyPassword(%q) = true, want false", h)
		}
	}
}

func TestBootstrapCreatesAdminOnce(t *testing.T) {
	a, st := newAuth(t)
	ctx := context.Background()

	pw, err := a.Bootstrap(ctx)
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if len(pw) < MinPasswordLen {
		t.Errorf("generated password %q is shorter than %d chars", pw, MinPasswordLen)
	}

	adm, err := st.AdminByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("AdminByUsername() error = %v", err)
	}
	if !VerifyPassword(adm.PasswordHash, pw) {
		t.Error("the generated password does not verify against the stored hash")
	}
	if !adm.MustChangePassword {
		t.Error("bootstrapped admin should be required to change the password")
	}

	pw2, err := a.Bootstrap(ctx)
	if err != nil {
		t.Fatalf("second Bootstrap() error = %v", err)
	}
	if pw2 != "" {
		t.Errorf("second Bootstrap() = %q, want empty — it must not reset the password", pw2)
	}
}

func TestLoginSuccess(t *testing.T) {
	a, _ := newAuth(t)
	ctx := context.Background()
	pw, _ := a.Bootstrap(ctx)

	token, adm, err := a.Login(ctx, "1.2.3.4", "admin", pw)
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if token == "" {
		t.Fatal("Login() returned an empty token")
	}
	if adm.Username != "admin" {
		t.Errorf("Username = %q", adm.Username)
	}

	got, err := a.Session(ctx, token)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if got.ID != adm.ID {
		t.Errorf("Session() returned admin %d, want %d", got.ID, adm.ID)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	a, _ := newAuth(t)
	ctx := context.Background()
	_, _ = a.Bootstrap(ctx)

	_, _, err := a.Login(ctx, "1.2.3.4", "admin", "nope")
	if !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("Login() error = %v, want ErrBadCredentials", err)
	}
}

func TestLoginUnknownUserIsIndistinguishable(t *testing.T) {
	a, _ := newAuth(t)
	ctx := context.Background()
	_, _ = a.Bootstrap(ctx)

	_, _, err := a.Login(ctx, "1.2.3.4", "ghost", "whatever")
	if !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("Login() error = %v, want ErrBadCredentials", err)
	}
}

func TestLoginRateLimited(t *testing.T) {
	a, _ := newAuth(t)
	ctx := context.Background()
	_, _ = a.Bootstrap(ctx)

	for i := 0; i < 5; i++ {
		if _, _, err := a.Login(ctx, "9.9.9.9", "admin", "bad"); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("attempt %d error = %v, want ErrBadCredentials", i, err)
		}
	}
	if _, _, err := a.Login(ctx, "9.9.9.9", "admin", "bad"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("6th attempt error = %v, want ErrRateLimited", err)
	}
}

func TestRateLimitIsPerIP(t *testing.T) {
	a, _ := newAuth(t)
	ctx := context.Background()
	pw, _ := a.Bootstrap(ctx)

	for i := 0; i < 6; i++ {
		_, _, _ = a.Login(ctx, "9.9.9.9", "admin", "bad")
	}
	if _, _, err := a.Login(ctx, "8.8.8.8", "admin", pw); err != nil {
		t.Fatalf("a different IP should not be limited, got %v", err)
	}
}

func TestSuccessfulLoginClearsRateLimit(t *testing.T) {
	a, _ := newAuth(t)
	ctx := context.Background()
	pw, _ := a.Bootstrap(ctx)

	for i := 0; i < 3; i++ {
		_, _, _ = a.Login(ctx, "7.7.7.7", "admin", "bad")
	}
	if _, _, err := a.Login(ctx, "7.7.7.7", "admin", pw); err != nil {
		t.Fatalf("Login() with the right password error = %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, _, err := a.Login(ctx, "7.7.7.7", "admin", "bad"); errors.Is(err, ErrRateLimited) {
			t.Fatalf("attempt %d was rate limited; the counter should have reset", i)
		}
	}
}

func TestLogoutInvalidatesSession(t *testing.T) {
	a, _ := newAuth(t)
	ctx := context.Background()
	pw, _ := a.Bootstrap(ctx)
	token, _, _ := a.Login(ctx, "1.2.3.4", "admin", pw)

	if err := a.Logout(ctx, token); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if _, err := a.Session(ctx, token); err == nil {
		t.Fatal("Session() = nil error after logout, want failure")
	}
}

func TestChangePassword(t *testing.T) {
	a, _ := newAuth(t)
	ctx := context.Background()
	pw, _ := a.Bootstrap(ctx)
	_, adm, _ := a.Login(ctx, "1.2.3.4", "admin", pw)

	if err := a.ChangePassword(ctx, adm.ID, "short"); err == nil {
		t.Error("ChangePassword() accepted a password below the minimum length")
	}
	if err := a.ChangePassword(ctx, adm.ID, "a-long-enough-password"); err != nil {
		t.Fatalf("ChangePassword() error = %v", err)
	}
	if _, _, err := a.Login(ctx, "1.2.3.4", "admin", "a-long-enough-password"); err != nil {
		t.Fatalf("Login() with the new password error = %v", err)
	}
	if _, _, err := a.Login(ctx, "1.2.3.4", "admin", pw); !errors.Is(err, ErrBadCredentials) {
		t.Error("the old password still works after a change")
	}
}

// TestRecordFailureMapSizeIsBounded is the regression test for the Important
// finding: a botnet failing one login from each of many distinct source IPs
// must not grow the attempts map without bound.
func TestRecordFailureMapSizeIsBounded(t *testing.T) {
	a, _ := newAuth(t)

	old := maxTrackedIPs
	maxTrackedIPs = 50
	t.Cleanup(func() { maxTrackedIPs = old })

	for i := 0; i < 10*maxTrackedIPs; i++ {
		a.recordFailure(fmt.Sprintf("10.%d.%d.%d", i/65536, (i/256)%256, i%256))
	}

	a.mu.Lock()
	n := len(a.attempts)
	a.mu.Unlock()
	if n > maxTrackedIPs {
		t.Fatalf("tracked IPs = %d, want <= %d (cap)", n, maxTrackedIPs)
	}
}

// TestRateLimitSurvivesTableAtCapacity proves the anti-gaming property the
// finding asked for explicitly: once an IP has accrued failures, flooding
// the table with unrelated IPs past its cap must not evict that IP's entry
// and reset its counter. If it could, an attacker would flood the table with
// throwaway source IPs specifically to un-limit the IP they are actually
// brute-forcing.
func TestRateLimitSurvivesTableAtCapacity(t *testing.T) {
	a, _ := newAuth(t)

	old := maxTrackedIPs
	maxTrackedIPs = 10
	t.Cleanup(func() { maxTrackedIPs = old })

	const target = "9.9.9.9"
	for i := 0; i < maxAttempts; i++ {
		a.recordFailure(target)
	}
	if !a.limited(target) {
		t.Fatal("target should already be rate limited before the flood")
	}

	// Flood far more distinct IPs than the cap allows, all after the target
	// was already tracked.
	for i := 0; i < 1000; i++ {
		a.recordFailure(fmt.Sprintf("10.1.%d.%d", i/256, i%256))
	}

	a.mu.Lock()
	n := len(a.attempts)
	a.mu.Unlock()
	if n > maxTrackedIPs {
		t.Fatalf("tracked IPs = %d, want <= %d (cap)", n, maxTrackedIPs)
	}
	if !a.limited(target) {
		t.Error("flooding the table with other IPs undid rate limiting for an already-tracked IP")
	}
}

// TestLoginStillRateLimitsAfterTableAtCapacity exercises the same property
// end-to-end through Login rather than reaching into the unexported map.
// Every Login call here pays real argon2id cost (that is the point of the
// timing-safe design in Login/VerifyPassword), so the flood is kept small —
// a few multiples of a tiny maxTrackedIPs — rather than the thousands used
// in the cheaper, map-only tests above.
func TestLoginStillRateLimitsAfterTableAtCapacity(t *testing.T) {
	a, _ := newAuth(t)
	ctx := context.Background()
	_, _ = a.Bootstrap(ctx)

	old := maxTrackedIPs
	maxTrackedIPs = 5
	t.Cleanup(func() { maxTrackedIPs = old })

	const target = "9.9.9.9"
	for i := 0; i < maxAttempts; i++ {
		if _, _, err := a.Login(ctx, target, "admin", "bad"); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("attempt %d error = %v, want ErrBadCredentials", i, err)
		}
	}

	for i := 0; i < 4*maxTrackedIPs; i++ {
		_, _, _ = a.Login(ctx, fmt.Sprintf("10.2.0.%d", i), "admin", "bad")
	}

	if _, _, err := a.Login(ctx, target, "admin", "bad"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("target Login() error = %v, want ErrRateLimited even after the table filled up", err)
	}
}

func TestSessionTokenIsNotStoredInPlaintext(t *testing.T) {
	a, st := newAuth(t)
	ctx := context.Background()
	pw, _ := a.Bootstrap(ctx)
	token, _, _ := a.Login(ctx, "1.2.3.4", "admin", pw)

	// The raw token must not resolve directly as a stored hash.
	if _, err := st.SessionAdmin(ctx, token); err == nil {
		t.Error("the raw session token is stored verbatim; it must be hashed")
	}
}
