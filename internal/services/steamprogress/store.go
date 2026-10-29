package steamprogress

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/uptrace/bun"
)

// CredentialTag separates account reconnections without storing credentials in
// observations. It is internal only and never returned by the progress API.
func CredentialTag(credentials *string) string {
	if credentials == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(*credentials))
	return hex.EncodeToString(sum[:])
}

// Save locks the account configuration so a fetch made before a credential
// change cannot publish observations for the new account. Retried snapshots are
// cumulative, and absent achievements leave the previous successful result intact.
func Save(ctx context.Context, db *bun.DB, userID, configID, externalID string, credentials *string, p Progress) error {
	if p.AccountID == "" || p.ObservedAt.IsZero() || p.HoursPlayed < 0 {
		return errors.New("invalid Steam snapshot")
	}
	var achievements any
	if p.Achievements != nil {
		b, err := json.Marshal(p.Achievements)
		if err != nil {
			return err
		}
		achievements = json.RawMessage(b)
		if p.AchievementsObservedAt == nil {
			return errors.New("Steam achievements missing observation date")
		}
	}
	return db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var current *string
		if err := tx.NewRaw(`SELECT storefront_credentials FROM user_sync_configs
   WHERE id=? AND user_id=? AND storefront='steam' FOR SHARE`, configID, userID).Scan(ctx, &current); err != nil {
			return err
		}
		if CredentialTag(current) != CredentialTag(credentials) {
			return errors.New("Steam account changed during sync")
		}
		_, err := tx.NewRaw(`INSERT INTO steam_progress_snapshots
   (external_game_id,account_id,credential_tag,observed_at,hours_played,achievements,achievements_observed_at)
   SELECT id,?,?,?,?,?,? FROM external_games WHERE id=? AND user_id=? AND storefront='steam'
   ON CONFLICT (external_game_id,account_id) DO UPDATE SET
   credential_tag=EXCLUDED.credential_tag,
   observed_at=EXCLUDED.observed_at,
   hours_played=GREATEST(steam_progress_snapshots.hours_played,EXCLUDED.hours_played),
   achievements=COALESCE(EXCLUDED.achievements,steam_progress_snapshots.achievements),
   achievements_observed_at=COALESCE(EXCLUDED.achievements_observed_at,steam_progress_snapshots.achievements_observed_at)
   WHERE EXCLUDED.observed_at >= steam_progress_snapshots.observed_at`,
			p.AccountID, CredentialTag(credentials), p.ObservedAt, p.HoursPlayed, achievements, p.AchievementsObservedAt, externalID, userID).Exec(ctx)
		if err != nil {
			return err
		}
		return nil
	})
}

// LoadCurrent only reads the configuration belonging to this external game's
// owner. No Steam ID supplied by a client can select another user's snapshot.
func LoadCurrent(ctx context.Context, db bun.IDB, userID, externalID string) (*Progress, error) {
	var credentials *string
	if err := db.NewRaw(`SELECT storefront_credentials FROM user_sync_configs WHERE user_id=? AND storefront='steam'`, userID).Scan(ctx, &credentials); err != nil {
		return nil, err
	}
	p := Progress{CredentialTag: CredentialTag(credentials)}
	var raw json.RawMessage
	err := db.NewRaw(`SELECT s.account_id,s.observed_at,s.hours_played,s.achievements,s.achievements_observed_at
   FROM steam_progress_snapshots s JOIN external_games e ON e.id=s.external_game_id
   WHERE e.id=? AND e.user_id=? AND e.storefront='steam' AND s.credential_tag=?
   ORDER BY s.observed_at DESC LIMIT 1`, externalID, userID, CredentialTag(credentials)).Scan(ctx, &p.AccountID, &p.ObservedAt, &p.HoursPlayed, &raw, &p.AchievementsObservedAt)
	if err != nil {
		return nil, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p.Achievements); err != nil {
			return nil, err
		}
	}
	return &p, nil
}
