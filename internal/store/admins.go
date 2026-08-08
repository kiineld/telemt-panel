package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type Admin struct {
	ID                 int64
	Username           string
	PasswordHash       string
	MustChangePassword bool
}

func (s *Store) AdminCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count admins: %w", err)
	}
	return n, nil
}

func (s *Store) CreateAdmin(ctx context.Context, username, hash string) (Admin, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO admins (username, password_hash, must_change_password) VALUES (?,?,1)`,
		username, hash)
	if err != nil {
		return Admin{}, fmt.Errorf("store: create admin: %w", err)
	}
	id, _ := res.LastInsertId()
	return Admin{ID: id, Username: username, PasswordHash: hash, MustChangePassword: true}, nil
}

func (s *Store) AdminByUsername(ctx context.Context, username string) (Admin, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, must_change_password FROM admins WHERE username = ?`,
		username)
	a, err := scanAdmin(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Admin{}, fmt.Errorf("%w: admin %s", ErrNotFound, username)
	}
	return a, err
}

func (s *Store) SetAdminPassword(ctx context.Context, id int64, hash string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE admins SET password_hash = ?, must_change_password = 0 WHERE id = ?`, hash, id)
	if err != nil {
		return fmt.Errorf("store: set admin password: %w", err)
	}
	return nil
}

func (s *Store) CreateSession(ctx context.Context, tokenHash string, adminID int64, expires time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO sessions (token_hash, admin_id, expires_at) VALUES (?,?,?)`,
		tokenHash, adminID, expires.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("store: create session: %w", err)
	}
	return nil
}

// SessionAdmin resolves a session token hash to its admin, treating an expired
// session as absent.
func (s *Store) SessionAdmin(ctx context.Context, tokenHash string) (Admin, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT a.id, a.username, a.password_hash, a.must_change_password
		FROM sessions s JOIN admins a ON a.id = s.admin_id
		WHERE s.token_hash = ? AND s.expires_at > ?`,
		tokenHash, time.Now().UTC().Format(time.RFC3339Nano))
	a, err := scanAdmin(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Admin{}, fmt.Errorf("%w: session", ErrNotFound)
	}
	return a, err
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	if err != nil {
		return fmt.Errorf("store: delete session: %w", err)
	}
	return nil
}

func scanAdmin(sc scanner) (Admin, error) {
	var (
		a    Admin
		must int
	)
	if err := sc.Scan(&a.ID, &a.Username, &a.PasswordHash, &must); err != nil {
		return Admin{}, err
	}
	a.MustChangePassword = must != 0
	return a, nil
}
