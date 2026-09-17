package store

import (
	"context"
	"slices"
	"time"
)

const hour = 3600

func hourStart(t time.Time) int64 {
	u := t.Unix()
	return u - u%hour
}

type stats struct {
	total, ok int
	lat       []int // nur erfolgreiche checks, ein timeout ist keine antwortzeit
}

func (st *stats) add(ok bool, latency int) {
	st.total++
	if ok {
		st.ok++
		st.lat = append(st.lat, latency)
	}
}

// latency liefert Mittel, p95 (nearest rank) und Maximum.
func (st *stats) latency() (avg, p95, maxMs int) {
	n := len(st.lat)
	if n == 0 {
		return 0, 0, 0
	}
	slices.Sort(st.lat)
	sum := 0
	for _, l := range st.lat {
		sum += l
	}
	return sum / n, st.lat[(95*n+99)/100-1], st.lat[n-1]
}

// Rollup verdichtet die beiden letzten abgeschlossenen Stunden in hourly. Die vorletzte ist
// dabei, weil ein Check mit dem Zeitstempel seines Starts erst nach dem Stundenwechsel
// geschrieben wird und weil ein ausgefallener Lauf sonst eine Luecke hinterlaesst.
func (s *Store) Rollup(ctx context.Context) error {
	cur := hourStart(s.now())
	from := cur - 2*hour

	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `select target_id, at, ok, latency_ms from checks
		where at >= ? and at < ? order by target_id, at`, from, cur)
	if err != nil {
		return err
	}
	type key struct{ target, hour int64 }
	var keys []key
	buckets := map[key]*stats{}
	for rows.Next() {
		var id, at int64
		var ok bool
		var latency int
		if err := rows.Scan(&id, &at, &ok, &latency); err != nil {
			rows.Close()
			return err
		}
		k := key{id, at - at%hour}
		if buckets[k] == nil {
			buckets[k] = &stats{}
			keys = append(keys, k)
		}
		buckets[k].add(ok, latency)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// der writer hat nur eine verbindung, rows muss vor dem ersten insert zu sein
	for _, k := range keys {
		st := buckets[k]
		avg, p95, maxMs := st.latency()
		_, err := tx.ExecContext(ctx, `insert or replace into hourly
			(target_id, hour, total, ok_count, lat_avg, lat_p95, lat_max) values (?, ?, ?, ?, ?, ?, ?)`,
			k.target, k.hour, st.total, st.ok, avg, p95, maxMs)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Uptime enthaelt Anteile zwischen 0 und 1, -1 heisst keine Daten im Zeitraum.
type Uptime struct {
	Day, Week, Month float64
}

// Uptime rechnet aus hourly plus den rohen Checks der laufenden Stunde, sonst haengt die
// Zahl bis zu einer Stunde hinterher. Die laufende Stunde zaehlt als eine der 24, 168 oder
// 720 Stunden.
func (s *Store) Uptime(ctx context.Context, targetID int64) (Uptime, error) {
	cur := hourStart(s.now())
	var dayT, dayOK, weekT, weekOK, monthT, monthOK int
	err := s.r.QueryRowContext(ctx, `select
		coalesce(sum(case when hour >= ?3 then total end), 0),
		coalesce(sum(case when hour >= ?3 then ok_count end), 0),
		coalesce(sum(case when hour >= ?4 then total end), 0),
		coalesce(sum(case when hour >= ?4 then ok_count end), 0),
		coalesce(sum(total), 0),
		coalesce(sum(ok_count), 0)
		from hourly where target_id = ?1 and hour < ?2 and hour >= ?5`,
		targetID, cur, cur-23*hour, cur-167*hour, cur-719*hour,
	).Scan(&dayT, &dayOK, &weekT, &weekOK, &monthT, &monthOK)
	if err != nil {
		return Uptime{}, err
	}

	total, ok, err := s.rawHour(ctx, targetID, cur)
	if err != nil {
		return Uptime{}, err
	}
	return Uptime{
		Day:   ratio(dayOK+ok, dayT+total),
		Week:  ratio(weekOK+ok, weekT+total),
		Month: ratio(monthOK+ok, monthT+total),
	}, nil
}

func (s *Store) rawHour(ctx context.Context, targetID, start int64) (total, ok int, err error) {
	err = s.r.QueryRowContext(ctx, `select count(*), coalesce(sum(ok), 0) from checks
		where target_id = ? and at >= ?`, targetID, start).Scan(&total, &ok)
	return total, ok, err
}

func ratio(ok, total int) float64 {
	if total == 0 {
		return -1
	}
	return float64(ok) / float64(total)
}

// Hour ist eine Zelle der Stundenleiste. Total 0 heisst, in der Stunde lief kein Check.
type Hour struct {
	Start     time.Time
	Total, OK int
}

// Hours liefert die letzten n Stunden, aelteste zuerst. Die letzte Zelle ist die laufende
// Stunde aus den rohen Checks.
func (s *Store) Hours(ctx context.Context, targetID int64, n int) ([]Hour, error) {
	cur := hourStart(s.now())
	first := cur - int64(n-1)*hour
	cells := make([]Hour, n)
	for i := range cells {
		cells[i].Start = fromUnix(first + int64(i)*hour)
	}

	rows, err := s.r.QueryContext(ctx, `select hour, total, ok_count from hourly
		where target_id = ? and hour >= ? and hour < ?`, targetID, first, cur)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var h int64
		var total, ok int
		if err := rows.Scan(&h, &total, &ok); err != nil {
			return nil, err
		}
		i := (h - first) / hour
		cells[i].Total, cells[i].OK = total, ok
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	last := &cells[n-1]
	last.Total, last.OK, err = s.rawHour(ctx, targetID, cur)
	return cells, err
}

type Point struct {
	At            time.Time
	Avg, P95, Max int
}

// Latency liefert die Antwortzeiten der erfolgreichen Checks. Bis 24 Stunden aus den rohen
// Checks in 10-Minuten-Schritten, darueber aus hourly. Schritte ohne Daten fehlen, die
// Luecke soll im Diagramm sichtbar bleiben.
func (s *Store) Latency(ctx context.Context, targetID int64, span time.Duration) ([]Point, error) {
	since := s.now().Add(-span).Unix()
	if span > 24*time.Hour {
		return s.hourlyLatency(ctx, targetID, since-since%hour)
	}

	rows, err := s.r.QueryContext(ctx, `select at, latency_ms from checks
		where target_id = ? and ok = 1 and at >= ? order by at`, targetID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	const step = 600
	var out []Point
	var st stats
	var bucket int64 = -1
	flush := func() {
		avg, p95, maxMs := st.latency()
		out = append(out, Point{At: fromUnix(bucket), Avg: avg, P95: p95, Max: maxMs})
		st = stats{}
	}
	for rows.Next() {
		var at int64
		var latency int
		if err := rows.Scan(&at, &latency); err != nil {
			return nil, err
		}
		if b := at - at%step; b != bucket {
			if bucket >= 0 {
				flush()
			}
			bucket = b
		}
		st.add(true, latency)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if bucket >= 0 {
		flush()
	}
	return out, nil
}

func (s *Store) hourlyLatency(ctx context.Context, targetID, since int64) ([]Point, error) {
	rows, err := s.r.QueryContext(ctx, `select hour, lat_avg, lat_p95, lat_max from hourly
		where target_id = ? and hour >= ? and ok_count > 0 order by hour`, targetID, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Point
	for rows.Next() {
		var p Point
		var h int64
		if err := rows.Scan(&h, &p.Avg, &p.P95, &p.Max); err != nil {
			return nil, err
		}
		p.At = fromUnix(h)
		out = append(out, p)
	}
	return out, rows.Err()
}
