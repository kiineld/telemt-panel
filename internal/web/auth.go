// Package web will host the panel's HTTP server; this file adds its
// authentication primitives: argon2id password hashing, session issuance
// backed by hashed tokens, and per-IP login rate limiting.
package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/kiineld/telemt-panel/internal/store"
)

var (
	ErrBadCredentials = errors.New("auth: invalid username or password")
	ErrRateLimited    = errors.New("auth: too many failed attempts, try again later")
)

const (
	SessionTTL     = 7 * 24 * time.Hour
	MinPasswordLen = 10

	maxAttempts   = 5
	attemptWindow = 15 * time.Minute
)

// argon2id parameters. Memory dominates cost; 64 MiB keeps login well under
// 100ms on a small VPS while staying expensive to brute force.
const (
	argonTime    = 1
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16

	// Bounds enforced when parsing an encoded hash string. HashPassword
	// always produces values well inside these limits; the limits exist so
	// that VerifyPassword can never be made to do something dangerous by a
	// malformed or attacker-influenced encoded string (e.g. a corrupted DB
	// row). golang.org/x/crypto/argon2 panics outright if time or threads is
	// zero, and an m= value that still fits in uint32 can demand a
	// multi-terabyte memory allocation, effectively a denial of service.
	maxArgonMemoryKiB = 2 * 1024 * 1024 // 2 GiB ceiling, far above our own 64 MiB cost
	maxArgonTimeCost  = 100
	maxArgonThreads   = 64
	maxArgonKeyLen    = 1024
	maxEncodedLen     = 1024
)

// HashPassword derives an argon2id key from plain and returns it as a PHC
// ("$argon2id$...") encoded string suitable for storage.
func HashPassword(plain string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(plain), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether plain matches the PHC-encoded argon2id hash
// in encoded. It never panics: encoded may be empty, truncated, non-base64,
// or (since it round-trips through the database) carry cost parameters no
// legitimate call to HashPassword would ever produce, and every one of those
// cases must fail closed rather than crash the process.
func VerifyPassword(encoded, plain string) (ok bool) {
	if len(encoded) == 0 || len(encoded) > maxEncodedLen {
		return false
	}
	// Defense in depth: bounds-check every parameter below so argon2.IDKey
	// is never called with something that would make it panic or attempt an
	// enormous allocation, but also recover as a last-resort safety net in
	// case some other unforeseen input makes the argon2 library misbehave.
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()

	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}

	var memory, timeCost uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &timeCost, &threads); err != nil {
		return false
	}
	if timeCost < 1 || timeCost > maxArgonTimeCost {
		return false
	}
	if threads < 1 || threads > maxArgonThreads {
		return false
	}
	if memory == 0 || memory > maxArgonMemoryKiB {
		return false
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 || len(want) > maxArgonKeyLen {
		return false
	}

	got := argon2.IDKey([]byte(plain), salt, timeCost, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// Auth wires login, session and rate-limiting logic on top of the store.
type Auth struct {
	store *store.Store

	mu       sync.Mutex
	attempts map[string]*attemptRecord
}

type attemptRecord struct {
	count int
	first time.Time
}

func NewAuth(st *store.Store) *Auth {
	return &Auth{store: st, attempts: map[string]*attemptRecord{}}
}

// Bootstrap creates the "admin" account on first boot and returns the
// generated password. It is idempotent: once an admin exists, later calls
// return ("", nil) rather than reset the password.
func (a *Auth) Bootstrap(ctx context.Context) (string, error) {
	n, err := a.store.AdminCount(ctx)
	if err != nil {
		return "", err
	}
	if n > 0 {
		return "", nil
	}

	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("auth: generate password: %w", err)
	}
	pw := base64.RawURLEncoding.EncodeToString(raw)

	hash, err := HashPassword(pw)
	if err != nil {
		return "", err
	}
	if _, err := a.store.CreateAdmin(ctx, "admin", hash); err != nil {
		return "", err
	}
	return pw, nil
}

// Login verifies username/plain against the store and, on success, creates a
// new session and returns its token. ip is used for per-IP rate limiting.
//
// An unknown username and a wrong password are made indistinguishable: both
// return ErrBadCredentials, and the unknown-username path spends the same
// argon2 work a real verification would, via a well-formed dummy hash with
// the same cost parameters as HashPassword, so the two cases cost the same
// time as well.
func (a *Auth) Login(ctx context.Context, ip, username, plain string) (string, store.Admin, error) {
	if a.limited(ip) {
		return "", store.Admin{}, ErrRateLimited
	}

	adm, err := a.store.AdminByUsername(ctx, username)
	if err != nil {
		_ = VerifyPassword(dummyHash, plain)
		a.recordFailure(ip)
		return "", store.Admin{}, ErrBadCredentials
	}

	if !VerifyPassword(adm.PasswordHash, plain) {
		a.recordFailure(ip)
		return "", store.Admin{}, ErrBadCredentials
	}

	token, err := newToken()
	if err != nil {
		return "", store.Admin{}, err
	}
	if err := a.store.CreateSession(ctx, hashToken(token), adm.ID, time.Now().Add(SessionTTL)); err != nil {
		return "", store.Admin{}, err
	}

	a.clearFailures(ip)
	return token, adm, nil
}

// dummyHash is a well-formed argon2id PHC string with exactly the cost
// parameters HashPassword uses (m=argonMemory, t=argonTime, p=argonThreads,
// a argonKeyLen-byte key). It exists purely so the unknown-username path in
// Login can spend the same argon2 work as a real verification would.
//
// It must parse all the way through to the argon2.IDKey call: if it were
// truncated or otherwise malformed, VerifyPassword would bail out early
// (that is the whole point of hardening it against malformed input), and the
// timing side channel this is meant to close would stay open. Built from the
// same constants as HashPassword so it can never drift out of sync with the
// real cost.
var dummyHash = fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
	argon2.Version, argonMemory, argonTime, argonThreads,
	base64.RawStdEncoding.EncodeToString(make([]byte, argonSaltLen)),
	base64.RawStdEncoding.EncodeToString(make([]byte, argonKeyLen)))

func (a *Auth) Logout(ctx context.Context, token string) error {
	return a.store.DeleteSession(ctx, hashToken(token))
}

func (a *Auth) Session(ctx context.Context, token string) (store.Admin, error) {
	return a.store.SessionAdmin(ctx, hashToken(token))
}

func (a *Auth) ChangePassword(ctx context.Context, id int64, plain string) error {
	if len(plain) < MinPasswordLen {
		return fmt.Errorf("auth: password must be at least %d characters", MinPasswordLen)
	}
	hash, err := HashPassword(plain)
	if err != nil {
		return err
	}
	return a.store.SetAdminPassword(ctx, id, hash)
}

func (a *Auth) limited(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	r, ok := a.attempts[ip]
	if !ok {
		return false
	}
	if time.Since(r.first) > attemptWindow {
		delete(a.attempts, ip)
		return false
	}
	return r.count >= maxAttempts
}

func (a *Auth) recordFailure(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	r, ok := a.attempts[ip]
	if !ok || time.Since(r.first) > attemptWindow {
		a.attempts[ip] = &attemptRecord{count: 1, first: time.Now()}
		return
	}
	r.count++
}

func (a *Auth) clearFailures(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.attempts, ip)
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// hashToken is what gets stored, so a database leak does not hand out live
// sessions: only the SHA-256 digest of a token is ever persisted, so knowing
// a row's stored value does not let you present it back as a valid token.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
