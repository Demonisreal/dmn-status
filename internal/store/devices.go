package store

import (
	"context"
	"crypto/rand"
	"time"
)

// DeviceMax ist deutlich laenger als SessionMax. Das cookie meldet niemanden an, es nimmt den
// browser nur vom globalen login-limit aus, und das soll auch nach monaten ohne login greifen.
const DeviceMax = 90 * 24 * time.Hour

// CreateDevice merkt sich einen browser nach erfolgreichem Login. token gehoert ins Cookie.
func (s *Store) CreateDevice(ctx context.Context) (string, error) {
	token := rand.Text()
	now := s.now().Unix()
	_, err := s.w.ExecContext(ctx, `insert into devices (token_hash, created_at, expires_at) values (?, ?, ?)`,
		hashToken(token), now, now+int64(DeviceMax.Seconds()))
	if err != nil {
		return "", err
	}
	return token, nil
}

func (s *Store) KnownDevice(ctx context.Context, token string) (bool, error) {
	var known bool
	err := s.r.QueryRowContext(ctx, `select exists (select 1 from devices where token_hash = ? and expires_at > ?)`,
		hashToken(token), s.now().Unix()).Scan(&known)
	return known, err
}

func (s *Store) DeleteExpiredDevices(ctx context.Context) (int64, error) {
	res, err := s.w.ExecContext(ctx, `delete from devices where expires_at <= ?`, s.now().Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
