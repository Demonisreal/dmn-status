package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

const (
	SessionIdle = 12 * time.Hour
	SessionMax  = 7 * 24 * time.Hour
)

type Session struct {
	CSRF      string
	CreatedAt time.Time
	ExpiresAt time.Time
	LastSeen  time.Time
}

// in der datenbank steht nur der hash, ein geleaktes backup enthaelt so keine gueltigen cookies
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateSession legt eine Sitzung an. token gehoert ins Cookie und wird nirgends gespeichert.
func (s *Store) CreateSession(ctx context.Context) (token string, sess Session, err error) {
	token = rand.Text()
	now := s.now().Truncate(time.Second).UTC()
	sess = Session{
		CSRF:      rand.Text(),
		CreatedAt: now,
		ExpiresAt: now.Add(SessionIdle),
		LastSeen:  now,
	}
	_, err = s.w.ExecContext(ctx, `insert into sessions (token_hash, csrf, created_at, expires_at, last_seen)
		values (?, ?, ?, ?, ?)`,
		hashToken(token), sess.CSRF, now.Unix(), sess.ExpiresAt.Unix(), now.Unix())
	if err != nil {
		return "", Session{}, err
	}
	return token, sess, nil
}

// Session liefert eine gueltige Sitzung oder ErrNotFound.
func (s *Store) Session(ctx context.Context, token string) (Session, error) {
	var sess Session
	var created, expires, seen int64
	err := s.r.QueryRowContext(ctx, `select csrf, created_at, expires_at, last_seen from sessions
		where token_hash = ? and expires_at > ?`, hashToken(token), s.now().Unix(),
	).Scan(&sess.CSRF, &created, &expires, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return sess, ErrNotFound
	}
	sess.CreatedAt, sess.ExpiresAt, sess.LastSeen = fromUnix(created), fromUnix(expires), fromUnix(seen)
	return sess, err
}

// TouchSession schiebt den Ablauf um SessionIdle nach hinten, hoechstens bis SessionMax nach
// dem Login.
func (s *Store) TouchSession(ctx context.Context, token string) error {
	return affected(s.w.ExecContext(ctx, `update sessions
		set last_seen = ?1, expires_at = min(?1 + ?2, created_at + ?3)
		where token_hash = ?4 and expires_at > ?1`,
		s.now().Unix(), int64(SessionIdle.Seconds()), int64(SessionMax.Seconds()), hashToken(token)))
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.w.ExecContext(ctx, `delete from sessions where token_hash = ?`, hashToken(token))
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	res, err := s.w.ExecContext(ctx, `delete from sessions where expires_at <= ?`, s.now().Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
