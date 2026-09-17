package store

import (
	"context"
	"database/sql"
	"errors"
)

// Admin liefert Benutzername und Passwort-Hash oder ErrNotFound, solange keiner angelegt ist.
func (s *Store) Admin(ctx context.Context) (username, hash string, err error) {
	err = s.r.QueryRowContext(ctx, `select username, pw_hash from admin where id = 1`).Scan(&username, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	return username, hash, err
}

// SetAdmin setzt Name und Hash und meldet dabei alle Sitzungen und Geraete ab, ein neues
// Passwort soll ein altes Cookie nicht weiterleben lassen.
func (s *Store) SetAdmin(ctx context.Context, username, hash string) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `insert into admin (id, username, pw_hash, updated_at) values (1, ?1, ?2, ?3)
		on conflict (id) do update set username = ?1, pw_hash = ?2, updated_at = ?3`,
		username, hash, s.now().Unix())
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from sessions`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from devices`); err != nil {
		return err
	}
	return tx.Commit()
}
