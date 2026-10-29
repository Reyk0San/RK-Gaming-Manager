package usergame

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/drzero42/nexorious/internal/services/steamprogress"
	"github.com/uptrace/bun"
)

// writeProgress runs only while holding the parent user_game lock. Historical
// sync has one selected source per platform; these are cumulative totals, never
// deltas. Nil means no new measurement, including on private/partial responses.
func writeProgress(ctx context.Context, tx bun.IDB, id string, in PlatformInput, merge bool) error {
	if in.SyncFromSource && in.SourceCredentialTag != nil {
		var credentials *string
		if err := tx.NewRaw(`SELECT c.storefront_credentials FROM user_sync_configs c
            JOIN user_games ug ON ug.user_id=c.user_id JOIN user_game_platforms p ON p.user_game_id=ug.id
            WHERE p.id=? AND c.storefront='steam' FOR SHARE OF c`, id).Scan(ctx, &credentials); err != nil {
			return err
		}
		if steamprogress.CredentialTag(credentials) != *in.SourceCredentialTag {
			return fmt.Errorf("Steam account changed: %w", ErrValidation)
		}
	}
	if err := validateProgress(in); err != nil {
		return err
	}
	if !in.SyncFromSource && in.OwnershipStatus != nil {
		if _, err := tx.NewRaw(`UPDATE user_game_platforms SET ownership_manual=true WHERE id=?`, id).Exec(ctx); err != nil {
			return err
		}
	}
	if in.ExternalGameID != nil {
		var owned bool
		err := tx.NewRaw(`SELECT EXISTS (SELECT 1 FROM user_game_platforms p
            JOIN user_games ug ON ug.id = p.user_game_id
            JOIN external_games e ON e.user_id = ug.user_id
            WHERE p.id = ? AND e.id = ?)`, id, *in.ExternalGameID).Scan(ctx, &owned)
		if err != nil {
			return err
		}
		if !owned {
			return ErrNotFound
		}
	}
	restoring := in.Progress != nil
	if restoring {
		// Imports restore both layers, never a foreign platform identity.
		// On an existing platform the known manual fields take priority.
		state := in.Progress
		in.Progress = nil
		in.SyncFromSource = true
		in.ObservedAt = state.ObservedAt
		in.HoursPlayed = state.Observed.HoursPlayed
		in.AchievementsUnlocked = state.Observed.AchievementsUnlocked
		in.AchievementsTotal = state.Observed.AchievementsTotal
		if err := writeProgress(ctx, tx, id, in, false); err != nil {
			return err
		}
		in.SyncFromSource = false
		in.HoursPlayed = state.Manual.HoursPlayed
		in.AchievementsUnlocked = state.Manual.AchievementsUnlocked
		in.AchievementsTotal = state.Manual.AchievementsTotal
	}
	table := "progress_manual_overrides"
	if in.SyncFromSource {
		table = "progress_observations"
	}
	// Imports merge into existing manual data conservatively. Explicit PATCH
	// writes, including zero, replace the named manual fields.
	hoursExpr := "COALESCE(EXCLUDED.hours_played, progress_manual_overrides.hours_played)"
	if merge {
		hoursExpr = "GREATEST(EXCLUDED.hours_played, progress_manual_overrides.hours_played)"
	}
	if in.SyncFromSource {
		hoursExpr = "GREATEST(EXCLUDED.hours_played, progress_observations.hours_played)"
	}
	accountColumn, accountValue, accountUpdate := "", "", ""
	args := []any{id, in.HoursPlayed, in.AchievementsUnlocked, in.AchievementsTotal}
	if in.SyncFromSource && in.SourceAccountID != nil {
		accountColumn = ", source_account_id"
		accountValue = ", ?"
		accountUpdate = ", source_account_id=EXCLUDED.source_account_id"
		hoursExpr = "CASE WHEN progress_observations.source_account_id IS NOT DISTINCT FROM EXCLUDED.source_account_id THEN GREATEST(EXCLUDED.hours_played, progress_observations.hours_played) ELSE EXCLUDED.hours_played END"
	}
	timestamp := "updated_at"
	if in.SyncFromSource {
		timestamp = "observed_at"
	}
	observedAt := time.Now().UTC()
	if in.ObservedAt != nil {
		observedAt = in.ObservedAt.UTC()
	}
	guard := ""
	if in.SyncFromSource {
		guard = " WHERE EXCLUDED.observed_at >= progress_observations.observed_at"
	}
	unlockedExpr := fmt.Sprintf("COALESCE(EXCLUDED.achievements_unlocked, %s.achievements_unlocked)", table)
	totalExpr := fmt.Sprintf("COALESCE(EXCLUDED.achievements_total, %s.achievements_total)", table)
	if restoring && merge {
		hoursExpr = "COALESCE(progress_manual_overrides.hours_played, EXCLUDED.hours_played)"
		unlockedExpr = "COALESCE(progress_manual_overrides.achievements_unlocked, EXCLUDED.achievements_unlocked)"
		totalExpr = "COALESCE(progress_manual_overrides.achievements_total, EXCLUDED.achievements_total)"
	}
	if in.SyncFromSource && in.SourceAccountID != nil {
		unlockedExpr = "CASE WHEN progress_observations.source_account_id IS NOT DISTINCT FROM EXCLUDED.source_account_id THEN " + unlockedExpr + " ELSE EXCLUDED.achievements_unlocked END"
		totalExpr = "CASE WHEN progress_observations.source_account_id IS NOT DISTINCT FROM EXCLUDED.source_account_id THEN " + totalExpr + " ELSE EXCLUDED.achievements_total END"
	}
	args = append(args, observedAt)
	if accountColumn != "" {
		args = append(args, in.SourceAccountID)
	}
	query := fmt.Sprintf(`INSERT INTO %s
        (user_game_platform_id, hours_played, achievements_unlocked, achievements_total, %s%s)
        VALUES (?, ?, ?, ?, ?%s)
        ON CONFLICT (user_game_platform_id) DO UPDATE SET
        hours_played = %s,
        achievements_unlocked = %s,
        achievements_total = %s,
        %s = EXCLUDED.%s%s%s`, table, timestamp, accountColumn, accountValue, hoursExpr, unlockedExpr, totalExpr, timestamp, timestamp, accountUpdate, guard)
	if _, err := tx.NewRaw(query, args...).Exec(ctx); err != nil {
		return fmt.Errorf("write progress: %w", err)
	}
	if !in.SyncFromSource {
		// Clearing an override is explicit; omitted/null PATCH metrics do not clear.
		for _, field := range []struct {
			name  string
			clear bool
		}{
			{"hours_played", in.ClearHoursOverride},
			{"achievements_unlocked", in.ClearAchievementsUnlockedOverride},
			{"achievements_total", in.ClearAchievementsTotalOverride},
		} {
			if field.clear {
				if _, err := tx.NewRaw("UPDATE progress_manual_overrides SET "+field.name+" = NULL WHERE user_game_platform_id = ?", id).Exec(ctx); err != nil {
					return err
				}
			}
		}
	}
	_, err := tx.NewRaw(`UPDATE user_game_platforms p SET
        hours_played = COALESCE(m.hours_played, o.hours_played),
        achievements_unlocked = COALESCE(m.achievements_unlocked, o.achievements_unlocked),
        achievements_total = COALESCE(m.achievements_total, o.achievements_total)
        FROM (SELECT ?::text AS id) k
        LEFT JOIN progress_manual_overrides m ON m.user_game_platform_id = k.id
        LEFT JOIN progress_observations o ON o.user_game_platform_id = k.id
        WHERE p.id = k.id`, id).Exec(ctx)
	return err
}

// Preserve untracked platform values conservatively (e.g. older data imports).
// Call before changing the projection, with the parent row locked.
func preserveUntrackedProgress(ctx context.Context, tx bun.IDB, id string) error {
	_, err := tx.NewRaw(`INSERT INTO progress_manual_overrides
        (user_game_platform_id, hours_played, achievements_unlocked, achievements_total)
        SELECT id, hours_played, achievements_unlocked, achievements_total
        FROM user_game_platforms p WHERE id = ?
        AND NOT EXISTS (SELECT 1 FROM progress_observations o WHERE o.user_game_platform_id = p.id)
        ON CONFLICT (user_game_platform_id) DO NOTHING`, id).Exec(ctx)
	return err
}

func validateProgress(in PlatformInput) error {
	metrics := []ProgressMetrics{{HoursPlayed: in.HoursPlayed, AchievementsUnlocked: in.AchievementsUnlocked, AchievementsTotal: in.AchievementsTotal}}
	if in.Progress != nil {
		metrics = append(metrics, in.Progress.Manual, in.Progress.Observed)
	}
	for _, m := range metrics {
		if m.HoursPlayed != nil && (math.IsNaN(*m.HoursPlayed) || math.IsInf(*m.HoursPlayed, 0) || *m.HoursPlayed < 0) {
			return fmt.Errorf("hours_played must be finite and non-negative: %w", ErrValidation)
		}
		if m.AchievementsUnlocked != nil && *m.AchievementsUnlocked < 0 {
			return fmt.Errorf("achievements_unlocked must be non-negative: %w", ErrValidation)
		}
		if m.AchievementsTotal != nil && *m.AchievementsTotal < 0 {
			return fmt.Errorf("achievements_total must be non-negative: %w", ErrValidation)
		}
	}
	return nil
}
