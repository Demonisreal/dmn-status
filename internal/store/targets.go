package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Demonisreal/dmn-status/internal/check"
)

const targetCols = `id, name, kind, address, expect_status, keyword, connect_to, interval_s,
	timeout_ms, fail_threshold, public, paused, sort, created_at, updated_at`

func scanTarget(sc scanner) (check.Target, error) {
	var t check.Target
	var created, updated int64
	err := sc.Scan(&t.ID, &t.Name, &t.Kind, &t.Address, &t.ExpectStatus, &t.Keyword, &t.ConnectTo,
		&t.IntervalS, &t.TimeoutMs, &t.FailThreshold, &t.Public, &t.Paused, &t.Sort, &created, &updated)
	t.CreatedAt, t.UpdatedAt = fromUnix(created), fromUnix(updated)
	return t, err
}

// CreateTarget legt ein Ziel an und gibt die neue ID zurueck. ID, CreatedAt und UpdatedAt
// in t werden ignoriert.
func (s *Store) CreateTarget(ctx context.Context, t check.Target) (int64, error) {
	now := s.now().Unix()
	res, err := s.w.ExecContext(ctx, `insert into targets (name, kind, address, expect_status, keyword,
		connect_to, interval_s, timeout_ms, fail_threshold, public, paused, sort, created_at, updated_at)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.Name, t.Kind, t.Address, t.ExpectStatus, t.Keyword, t.ConnectTo, t.IntervalS, t.TimeoutMs,
		t.FailThreshold, t.Public, t.Paused, t.Sort, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateTarget(ctx context.Context, t check.Target) error {
	return affected(s.w.ExecContext(ctx, `update targets set name = ?, kind = ?, address = ?,
		expect_status = ?, keyword = ?, connect_to = ?, interval_s = ?, timeout_ms = ?,
		fail_threshold = ?, public = ?, paused = ?, sort = ?, updated_at = ?
		where id = ?`,
		t.Name, t.Kind, t.Address, t.ExpectStatus, t.Keyword, t.ConnectTo, t.IntervalS, t.TimeoutMs,
		t.FailThreshold, t.Public, t.Paused, t.Sort, s.now().Unix(), t.ID))
}

// DeleteTarget loescht das Ziel samt Checks, Stundenwerten und Vorfaellen.
func (s *Store) DeleteTarget(ctx context.Context, id int64) error {
	return affected(s.w.ExecContext(ctx, `delete from targets where id = ?`, id))
}

func (s *Store) SetPaused(ctx context.Context, id int64, paused bool) error {
	return affected(s.w.ExecContext(ctx, `update targets set paused = ?, updated_at = ? where id = ?`,
		paused, s.now().Unix(), id))
}

func (s *Store) Target(ctx context.Context, id int64) (check.Target, error) {
	t, err := scanTarget(s.r.QueryRowContext(ctx, `select `+targetCols+` from targets where id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// Targets liefert alle Ziele in Anzeigereihenfolge.
func (s *Store) Targets(ctx context.Context) ([]check.Target, error) {
	return s.targets(ctx, `select `+targetCols+` from targets order by sort, id`)
}

func (s *Store) PublicTargets(ctx context.Context) ([]check.Target, error) {
	return s.targets(ctx, `select `+targetCols+` from targets where public = 1 order by sort, id`)
}

func (s *Store) targets(ctx context.Context, query string) ([]check.Target, error) {
	rows, err := s.r.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []check.Target
	for rows.Next() {
		t, err := scanTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
