package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/Demonisreal/dmn-status/internal/check"
)

type Check struct {
	At time.Time
	check.Result
}

func (s *Store) InsertCheck(ctx context.Context, targetID int64, at time.Time, r check.Result) error {
	var version sql.NullString
	if r.Version != "" {
		version = sql.NullString{String: r.Version, Valid: true}
	}
	_, err := s.w.ExecContext(ctx, `insert into checks
		(target_id, at, ok, latency_ms, status_code, error, players, max_players, version)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		targetID, at.Unix(), r.OK, r.LatencyMs, r.StatusCode, r.Err, r.Players, r.MaxPlayers, version)
	return err
}

// LastChecks liefert die letzten n Checks eines Ziels, neuester zuerst.
func (s *Store) LastChecks(ctx context.Context, targetID int64, n int) ([]Check, error) {
	return s.checks(ctx, `select at, ok, latency_ms, status_code, error, players, max_players, version
		from checks where target_id = ? order by at desc, id desc limit ?`, targetID, n)
}

// HourChecks liefert die Checks der laufenden Stunde, aeltester zuerst. Die sind noch
// nicht in hourly verdichtet.
func (s *Store) HourChecks(ctx context.Context, targetID int64) ([]Check, error) {
	return s.checks(ctx, `select at, ok, latency_ms, status_code, error, players, max_players, version
		from checks where target_id = ? and at >= ? order by at, id`, targetID, hourStart(s.now()))
}

func (s *Store) checks(ctx context.Context, query string, args ...any) ([]Check, error) {
	rows, err := s.r.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Check
	for rows.Next() {
		var c Check
		var at int64
		var version sql.NullString
		err := rows.Scan(&at, &c.OK, &c.LatencyMs, &c.StatusCode, &c.Err, &c.Players, &c.MaxPlayers, &version)
		if err != nil {
			return nil, err
		}
		c.At = fromUnix(at)
		c.Version = version.String
		out = append(out, c)
	}
	return out, rows.Err()
}
