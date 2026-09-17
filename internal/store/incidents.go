package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrIncidentOpen = errors.New("vorfall schon offen")

type Incident struct {
	ID         int64
	TargetID   int64
	TargetName string
	StartedAt  time.Time
	EndedAt    time.Time // zero, solange der Vorfall offen ist
	Cause      string
	MailedDown bool
	MailedUp   bool
}

func (i Incident) Open() bool {
	return i.EndedAt.IsZero()
}

const incidentSelect = `select i.id, i.target_id, t.name, i.started_at, i.ended_at, i.cause,
	i.mailed_down, i.mailed_up
	from incidents i join targets t on t.id = i.target_id`

func scanIncident(sc scanner) (Incident, error) {
	var i Incident
	var started int64
	var ended sql.NullInt64
	err := sc.Scan(&i.ID, &i.TargetID, &i.TargetName, &started, &ended, &i.Cause, &i.MailedDown, &i.MailedUp)
	i.StartedAt = fromUnix(started)
	if ended.Valid {
		i.EndedAt = fromUnix(ended.Int64)
	}
	return i, err
}

// OpenIncident eroeffnet einen Vorfall. Ist fuer das Ziel schon einer offen, kommt
// ErrIncidentOpen zurueck.
func (s *Store) OpenIncident(ctx context.Context, targetID int64, at time.Time, cause string) (int64, error) {
	res, err := s.w.ExecContext(ctx, `insert into incidents (target_id, started_at, cause)
		select ?1, ?2, ?3
		where not exists (select 1 from incidents where target_id = ?1 and ended_at is null)`,
		targetID, at.Unix(), cause)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, ErrIncidentOpen
	}
	return res.LastInsertId()
}

func (s *Store) CloseIncident(ctx context.Context, id int64, at time.Time) error {
	return affected(s.w.ExecContext(ctx, `update incidents set ended_at = ?
		where id = ? and ended_at is null`, at.Unix(), id))
}

// ClosePausedIncident beendet den offenen Vorfall eines pausierten Ziels, falls es einen gibt.
// Beide Mails gelten dabei als erledigt, der Nachversand schickt also weder die Wiederkehr
// noch eine liegengebliebene Ausfall-Mail.
func (s *Store) ClosePausedIncident(ctx context.Context, targetID int64) error {
	_, err := s.w.ExecContext(ctx, `update incidents set ended_at = ?, mailed_down = 1, mailed_up = 1
		where target_id = ? and ended_at is null`, s.now().Unix(), targetID)
	return err
}

// LastIncidentEnd liefert das Ende des zuletzt beendeten Vorfalls, zero ohne einen.
func (s *Store) LastIncidentEnd(ctx context.Context, targetID int64) (time.Time, error) {
	var end sql.NullInt64
	err := s.r.QueryRowContext(ctx, `select max(ended_at) from incidents where target_id = ?`, targetID).Scan(&end)
	if err != nil || !end.Valid {
		return time.Time{}, err
	}
	return fromUnix(end.Int64), nil
}

// OpenIncidentFor liefert den offenen Vorfall eines Ziels oder ErrNotFound.
func (s *Store) OpenIncidentFor(ctx context.Context, targetID int64) (Incident, error) {
	i, err := scanIncident(s.r.QueryRowContext(ctx, incidentSelect+`
		where i.target_id = ? and i.ended_at is null`, targetID))
	if errors.Is(err, sql.ErrNoRows) {
		return i, ErrNotFound
	}
	return i, err
}

// RecentIncidents liefert Vorfaelle, die seit since offen waren, neueste zuerst. Offene
// Vorfaelle sind immer dabei, auch wenn sie vor since begonnen haben.
func (s *Store) RecentIncidents(ctx context.Context, limit int, since time.Time, publicOnly bool) ([]Incident, error) {
	return s.incidents(ctx, incidentSelect+`
		where (i.ended_at is null or i.ended_at >= ?1) and (not ?2 or t.public = 1)
		order by i.started_at desc, i.id desc limit ?3`, since.Unix(), publicOnly, limit)
}

func (s *Store) TargetIncidents(ctx context.Context, targetID int64, limit int, since time.Time) ([]Incident, error) {
	return s.incidents(ctx, incidentSelect+`
		where i.target_id = ?1 and (i.ended_at is null or i.ended_at >= ?2)
		order by i.started_at desc, i.id desc limit ?3`, targetID, since.Unix(), limit)
}

// MarkMailed haelt fest, dass die Ausfall- (up=false) oder Wiederkehr-Mail (up=true) raus ist.
func (s *Store) MarkMailed(ctx context.Context, id int64, up bool) error {
	query := `update incidents set mailed_down = 1 where id = ?`
	if up {
		query = `update incidents set mailed_up = 1 where id = ? and ended_at is not null`
	}
	return affected(s.w.ExecContext(ctx, query, id))
}

// UnmailedIncidents liefert Vorfaelle mit ausstehender Mail, deren Ereignis nach since liegt.
// Die Grenze verhindert, dass nach einer laengeren SMTP-Stoerung alte Meldungen nachkommen.
func (s *Store) UnmailedIncidents(ctx context.Context, since time.Time) ([]Incident, error) {
	return s.incidents(ctx, incidentSelect+`
		where (i.mailed_down = 0 and i.started_at >= ?1)
		   or (i.mailed_up = 0 and i.ended_at >= ?1)
		order by i.started_at, i.id`, since.Unix())
}

func (s *Store) incidents(ctx context.Context, query string, args ...any) ([]Incident, error) {
	rows, err := s.r.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Incident
	for rows.Next() {
		i, err := scanIncident(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}
