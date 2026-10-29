// Package progressadapter defines the future per-platform progress boundary.
// It is deliberately independent of library acquisition and is not registered
// with existing sync workers. Providers return observations, never DB writes.
package progressadapter

import (
	"context"
	"time"
)

type Provider string

const (
	Steam             Provider = "steam"
	PlayStation       Provider = "playstation"
	Xbox              Provider = "xbox"
	RetroAchievements Provider = "retroachievements"
)

// Ref identifies an external game on a canonical Nexorious platform slug.
// AccountID is an internal account reference, never a credential.
type Ref struct {
	AccountID string
	GameID    string
	Platform  string
}

// Metrics uses nil for unknown/unavailable and pointers for known zero values.
// Playtime is a cumulative total in seconds, not an increment to add on retry.
// CompletionPercent is provider-defined and must not imply a manual play status.
type Metrics struct {
	PlaytimeSeconds      *int64
	AchievementsUnlocked *int
	AchievementsTotal    *int
	CompletionPercent    *float64
}

// Achievement identity is scoped to provider/account/game/platform/set/mode.
// Mode preserves distinctions such as RetroAchievements softcore/hardcore.
type Achievement struct {
	ID          string
	SetID       string
	Mode        string
	Description string
	Icon        string
	IconGray    string
	Name        string
	Unlocked    *bool
	UnlockedAt  *time.Time
	Points      *int
}

type Snapshot struct {
	Ref          Ref
	ObservedAt   time.Time
	Metrics      Metrics
	Achievements []Achievement
}

// Adapter is optional alongside storefrontadapter.Adapter. Fetch must respect
// cancellation, return errors for failed fetches (never an empty successful
// library), and leave missing metrics nil. Credentials belong in the existing
// encrypted service configuration, not in snapshots or logs.
type Adapter interface {
	Provider() Provider
	Fetch(ctx context.Context, ref Ref) (Snapshot, error)
}

// Resolve selects each manually supplied field first, including explicit zero.
// It neither mutates its inputs nor sums provider totals. The caller must select
// a source/account and reject stale snapshots before resolving; persistence must
// atomically read manual overrides and write observations in one transaction.
// Clearing an override is an explicit user action outside this function.
func Resolve(manual, observed Metrics) Metrics {
	return Metrics{
		PlaytimeSeconds:      prefer(manual.PlaytimeSeconds, observed.PlaytimeSeconds),
		AchievementsUnlocked: prefer(manual.AchievementsUnlocked, observed.AchievementsUnlocked),
		AchievementsTotal:    prefer(manual.AchievementsTotal, observed.AchievementsTotal),
		CompletionPercent:    prefer(manual.CompletionPercent, observed.CompletionPercent),
	}
}

// Return a copy so consumers cannot change a manual value through the result.
func prefer[T any](manual, observed *T) *T {
	value := manual
	if value == nil {
		value = observed
	}
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
