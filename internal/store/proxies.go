package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type State string

const (
	StateCreating   State = "creating"
	StateRunning    State = "running"
	StateStopped    State = "stopped"
	StateError      State = "error"
	StateRecreating State = "recreating"
	StateDeleting   State = "deleting"
)

type Proxy struct {
	ID                string
	Name              string
	Port              int
	TLSDomain         string
	AdTag             string
	Secret            string
	APIToken          string
	ContainerID       string
	State             State
	StateMessage      string
	DataQuotaBytes    *uint64
	ExpirationRFC3339 *string
	MaxTCPConns       *int
	MaxUniqueIPs      *int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

const proxyColumns = `id, name, port, tls_domain, ad_tag, secret, api_token,
	container_id, state, state_message, data_quota_bytes, expiration_rfc3339,
	max_tcp_conns, max_unique_ips, created_at, updated_at`

func (s *Store) CreateProxy(ctx context.Context, p Proxy) error {
	now := time.Now().UTC()
	p.CreatedAt, p.UpdatedAt = now, now

	_, err := s.db.ExecContext(ctx, `INSERT INTO proxies (`+proxyColumns+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Name, p.Port, p.TLSDomain, p.AdTag, p.Secret, p.APIToken,
		p.ContainerID, string(p.State), p.StateMessage,
		nullU64(p.DataQuotaBytes), nullStr(p.ExpirationRFC3339),
		nullInt(p.MaxTCPConns), nullInt(p.MaxUniqueIPs),
		p.CreatedAt.Format(time.RFC3339Nano), p.UpdatedAt.Format(time.RFC3339Nano))

	if isUniqueViolation(err, "proxies.port") {
		return fmt.Errorf("%w: port %d", ErrPortTaken, p.Port)
	}
	if err != nil {
		return fmt.Errorf("store: create proxy: %w", err)
	}
	return nil
}

func (s *Store) GetProxy(ctx context.Context, id string) (Proxy, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+proxyColumns+` FROM proxies WHERE id = ?`, id)
	p, err := scanProxy(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Proxy{}, fmt.Errorf("%w: proxy %s", ErrNotFound, id)
	}
	return p, err
}

func (s *Store) ListProxies(ctx context.Context) ([]Proxy, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+proxyColumns+` FROM proxies ORDER BY port`)
	if err != nil {
		return nil, fmt.Errorf("store: list proxies: %w", err)
	}
	defer rows.Close()

	var out []Proxy
	for rows.Next() {
		p, err := scanProxy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) UpdateProxy(ctx context.Context, p Proxy) error {
	p.UpdatedAt = time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `UPDATE proxies SET
		name=?, port=?, tls_domain=?, ad_tag=?, secret=?, api_token=?,
		container_id=?, state=?, state_message=?, data_quota_bytes=?,
		expiration_rfc3339=?, max_tcp_conns=?, max_unique_ips=?, updated_at=?
		WHERE id=?`,
		p.Name, p.Port, p.TLSDomain, p.AdTag, p.Secret, p.APIToken,
		p.ContainerID, string(p.State), p.StateMessage,
		nullU64(p.DataQuotaBytes), nullStr(p.ExpirationRFC3339),
		nullInt(p.MaxTCPConns), nullInt(p.MaxUniqueIPs),
		p.UpdatedAt.Format(time.RFC3339Nano), p.ID)

	if isUniqueViolation(err, "proxies.port") {
		return fmt.Errorf("%w: port %d", ErrPortTaken, p.Port)
	}
	if err != nil {
		return fmt.Errorf("store: update proxy: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: proxy %s", ErrNotFound, p.ID)
	}
	return nil
}

func (s *Store) DeleteProxy(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM proxies WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete proxy: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: proxy %s", ErrNotFound, id)
	}
	return nil
}

type scanner interface{ Scan(dest ...any) error }

func scanProxy(sc scanner) (Proxy, error) {
	var (
		p                    Proxy
		state                string
		quota, conns, ips    sql.NullInt64
		exp                  sql.NullString
		createdAt, updatedAt string
	)
	err := sc.Scan(&p.ID, &p.Name, &p.Port, &p.TLSDomain, &p.AdTag, &p.Secret,
		&p.APIToken, &p.ContainerID, &state, &p.StateMessage,
		&quota, &exp, &conns, &ips, &createdAt, &updatedAt)
	if err != nil {
		return Proxy{}, err
	}

	p.State = State(state)
	if quota.Valid {
		v := uint64(quota.Int64)
		p.DataQuotaBytes = &v
	}
	if exp.Valid {
		v := exp.String
		p.ExpirationRFC3339 = &v
	}
	if conns.Valid {
		v := int(conns.Int64)
		p.MaxTCPConns = &v
	}
	if ips.Valid {
		v := int(ips.Int64)
		p.MaxUniqueIPs = &v
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return p, nil
}

func nullU64(v *uint64) any {
	if v == nil {
		return nil
	}
	return int64(*v)
}

func nullInt(v *int) any {
	if v == nil {
		return nil
	}
	return int64(*v)
}

func nullStr(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}
