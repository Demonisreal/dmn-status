package store

import (
	"context"
	"time"
)

// abgeschlossene vorfaelle bleiben genauso lange wie die stundenwerte, damit fuer denselben
// zeitraum verfuegbarkeit und vorfaelle zusammenpassen
const hourlyKeep = 400 * 24 * time.Hour

// Prune loescht rohe Checks aelter als days Tage, Stundenwerte und abgeschlossene Vorfaelle
// aelter als 400 Tage. Ein offener Vorfall bleibt immer stehen.
func (s *Store) Prune(ctx context.Context, days int) error {
	now := s.now()
	checksBefore := now.Add(-time.Duration(days) * 24 * time.Hour).Unix()
	if _, err := s.w.ExecContext(ctx, `delete from checks where at < ?`, checksBefore); err != nil {
		return err
	}
	keepBefore := now.Add(-hourlyKeep).Unix()
	if _, err := s.w.ExecContext(ctx, `delete from hourly where hour < ?`, keepBefore); err != nil {
		return err
	}
	_, err := s.w.ExecContext(ctx, `delete from incidents where ended_at is not null and ended_at < ?`, keepBefore)
	return err
}
