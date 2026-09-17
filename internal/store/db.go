// Package store kapselt die SQLite-Datenbank. Zeiten liegen als Unix-Sekunden in UTC.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

var ErrNotFound = errors.New("nicht gefunden")

type Store struct {
	w   *sql.DB
	r   *sql.DB
	now func() time.Time
}

// uri-sonderzeichen im pfad wuerden sonst als query oder fragment gelesen
var uriPath = strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23")

// Open oeffnet die Datenbank und spielt fehlende Migrationen ein. now ist die Uhr fuer
// alles, was "jetzt" braucht, in Produktion time.Now.
func Open(ctx context.Context, file string, now func() time.Time) (*Store, error) {
	dsn := "file:" + uriPath.Replace(filepath.ToSlash(file)) +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"

	// sqlite kennt nur einen schreiber. mit einer verbindung stehen schreibzugriffe im pool
	// an statt in SQLITE_BUSY zu laufen, immediate holt die sperre schon beim begin.
	w, err := sql.Open("sqlite", dsn+"&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	if err := migrate(ctx, w, now); err != nil {
		w.Close()
		return nil, err
	}

	r, err := sql.Open("sqlite", dsn+"&_pragma=query_only(1)")
	if err != nil {
		w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(4)
	if err := r.PingContext(ctx); err != nil {
		w.Close()
		r.Close()
		return nil, err
	}
	return &Store{w: w, r: r, now: now}, nil
}

func (s *Store) Close() error {
	return errors.Join(s.r.Close(), s.w.Close())
}

func migrate(ctx context.Context, db *sql.DB, now func() time.Time) error {
	_, err := db.ExecContext(ctx, `create table if not exists schema_migrations (
		version    text    primary key,
		applied_at integer not null
	)`)
	if err != nil {
		return fmt.Errorf("schema_migrations: %w", err)
	}

	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	for _, name := range files {
		version := strings.TrimSuffix(path.Base(name), ".sql")
		if err := applyMigration(ctx, db, name, version, now()); err != nil {
			return fmt.Errorf("migration %s: %w", version, err)
		}
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, name, version string, now time.Time) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// die pruefung liegt in der transaktion, zwei gleichzeitig startende prozesse
	// spielen dieselbe migration so nicht doppelt ein
	var done bool
	err = tx.QueryRowContext(ctx, `select exists (select 1 from schema_migrations where version = ?)`, version).Scan(&done)
	if err != nil || done {
		return err
	}

	body, err := migrations.ReadFile(name)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, string(body)); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `insert into schema_migrations (version, applied_at) values (?, ?)`, version, now.Unix())
	if err != nil {
		return err
	}
	return tx.Commit()
}

func fromUnix(sec int64) time.Time {
	return time.Unix(sec, 0).UTC()
}

type scanner interface {
	Scan(dest ...any) error
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Ping prueft, ob die Datenbank lesbar ist, fuer /healthz.
func (s *Store) Ping(ctx context.Context) error {
	var n int
	return s.r.QueryRowContext(ctx, `select 1`).Scan(&n)
}

// Backup schreibt eine konsistente Kopie nach file. Die Datei darf noch nicht existieren.
func (s *Store) Backup(ctx context.Context, file string) error {
	_, err := s.w.ExecContext(ctx, `vacuum into ?`, file)
	return err
}
