package api_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/drzero42/nexorious/internal/db/migrations"
	"github.com/drzero42/nexorious/internal/services/steamprogress"
	"github.com/drzero42/nexorious/internal/usergame"
	"github.com/uptrace/bun"
)

func TestSteamProgressPersistenceIsolationAndAPI(t *testing.T) {
	truncateAllTables(t)
	ctx := context.Background()
	e := newTestEcho(t, testDB, testCfg())
	u, token := setupUserGamesUser(t, testDB, e, "steam-progress")
	_, foreignToken := setupUserGamesUser(t, testDB, e, "steam-foreign")
	game := insertTestGame(t, testDB, "Steam progress")
	insertTestUserGame(t, testDB, "steam-progress-game", u, int(game))
	credentials := "encrypted-account-a"
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO user_sync_configs (id,user_id,storefront,frequency,storefront_credentials) VALUES ('steam-config',?,'steam','daily',?)`, []any{u, credentials}},
		{`INSERT INTO external_games (id,user_id,storefront,external_id,title,is_available,is_subscription) VALUES ('steam-external',?,'steam','730','Steam progress',true,false)`, []any{u}},
	} {
		if _, err := testDB.NewRaw(q.sql, q.args...).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	rec := postJSONAuth(t, e, "/api/user-games/steam-progress-game/platforms", map[string]any{"platform": "pc-windows", "storefront": "steam", "hours_played": 0, "achievements_unlocked": 0}, token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("platform: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := testDB.NewRaw(`UPDATE user_game_platforms SET external_game_id='steam-external' WHERE user_game_id='steam-progress-game'`).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	p := steamprogress.Progress{AccountID: "account-a", ObservedAt: at, HoursPlayed: 2, AchievementsObservedAt: &at,
		Achievements: []steamprogress.Achievement{{ID: "A", Title: "Winner", Unlocked: true, UnlockedAt: &at}, {ID: "B", Title: "Other"}}}
	save := func(p steamprogress.Progress) error {
		return steamprogress.Save(ctx, testDB, u, "steam-config", "steam-external", &credentials, p)
	}
	if err := save(p); err != nil {
		t.Fatal(err)
	}
	if err := save(p); err != nil {
		t.Fatal(err)
	}
	missing := p
	missing.ObservedAt = at.Add(time.Minute)
	missing.HoursPlayed = 3
	missing.Achievements = nil
	missing.AchievementsObservedAt = nil
	if err := save(missing); err != nil {
		t.Fatal(err)
	}
	stale := p
	stale.ObservedAt = at.Add(-time.Hour)
	stale.HoursPlayed = 100
	if err := save(stale); err != nil {
		t.Fatal(err)
	}
	loaded, err := steamprogress.LoadCurrent(ctx, testDB, u, "steam-external")
	if err != nil || loaded.HoursPlayed != 3 || len(loaded.Achievements) != 2 || !loaded.AchievementsObservedAt.Equal(at) {
		t.Fatalf("lost snapshot: %+v %v", loaded, err)
	}
	unlocked, total := loaded.Counts()
	platform, store, external := "pc-windows", "steam", "steam-external"
	_, err = usergame.Acquire(ctx, testDB, usergame.AcquireParams{UserID: u, GameID: int32(game), Mode: usergame.ModeUpsert, Platforms: []usergame.PlatformInput{{
		Platform: &platform, Storefront: &store, ExternalGameID: &external, SyncFromSource: true, ObservedAt: &loaded.ObservedAt, SourceAccountID: &loaded.AccountID, SourceCredentialTag: &loaded.CredentialTag, HoursPlayed: &loaded.HoursPlayed, AchievementsUnlocked: unlocked, AchievementsTotal: total,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	var hours float64
	var count int
	if err := testDB.NewRaw(`SELECT hours_played,achievements_unlocked FROM user_game_platforms WHERE user_game_id='steam-progress-game'`).Scan(ctx, &hours, &count); err != nil {
		t.Fatal(err)
	}
	if hours != 0 || count != 0 {
		t.Fatalf("manual zeros overwritten: %v/%v", hours, count)
	}
	path := "/api/user-games/steam-progress-game/steam-progress"
	rec = getAuth(t, e, path+"?limit=1", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Source       string                      `json:"source"`
		Completion   float64                     `json:"completion_percent"`
		HasMore      bool                        `json:"has_more"`
		Achievements []steamprogress.Achievement `json:"achievements"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Source != "steam" || response.Completion != 50 || !response.HasMore || len(response.Achievements) != 1 || response.Achievements[0].Title != "Winner" {
		t.Fatalf("response: %s", rec.Body.String())
	}
	if rec = getAuth(t, e, path, foreignToken); rec.Code != 404 {
		t.Fatalf("foreign read: %d", rec.Code)
	}
	if rec = getAuth(t, e, path+"?limit=101", token); rec.Code != 400 {
		t.Fatalf("unbounded read: %d", rec.Code)
	}
	if err := steamprogress.Save(ctx, testDB, "other-owner", "steam-config", "steam-external", &credentials, p); err == nil {
		t.Fatal("foreign write accepted")
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			candidate := missing
			candidate.ObservedAt = at.Add(time.Duration(i+2) * time.Minute)
			candidate.HoursPlayed = float64(i + 4)
			if err := save(candidate); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	loaded, err = steamprogress.LoadCurrent(ctx, testDB, u, "steam-external")
	if err != nil || loaded.HoursPlayed != 11 || len(loaded.Achievements) != 2 {
		t.Fatalf("concurrent snapshot: %+v %v", loaded, err)
	}
	if _, err := testDB.NewRaw(`UPDATE user_sync_configs SET storefront_credentials='encrypted-account-b' WHERE id='steam-config'`).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := save(missing); err == nil {
		t.Fatal("old credentials accepted")
	}
	if _, err := steamprogress.LoadCurrent(ctx, testDB, u, "steam-external"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old account visible: %v", err)
	}
	rec = getAuth(t, e, path, token)
	if rec.Code != 200 || !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("unavailable response: %d %s", rec.Code, rec.Body.String())
	}
	// An old staged result must fail the final, transactional account check.
	_, err = usergame.Acquire(ctx, testDB, usergame.AcquireParams{UserID: u, GameID: int32(game), Mode: usergame.ModeUpsert, Platforms: []usergame.PlatformInput{{
		Platform: &platform, Storefront: &store, SyncFromSource: true, SourceAccountID: &loaded.AccountID, SourceCredentialTag: &loaded.CredentialTag, HoursPlayed: &loaded.HoursPlayed,
	}}})
	if err == nil {
		t.Fatal("staged old-account projection accepted")
	}
	credentialsB := "encrypted-account-b"
	newAt := at.Add(10 * time.Minute)
	accountB := steamprogress.Progress{AccountID: "account-b", ObservedAt: newAt, HoursPlayed: 1, AchievementsObservedAt: &newAt, Achievements: []steamprogress.Achievement{{ID: "A", Title: "Winner", Unlocked: false}}}
	if err := steamprogress.Save(ctx, testDB, u, "steam-config", "steam-external", &credentialsB, accountB); err != nil {
		t.Fatal(err)
	}
	current, err := steamprogress.LoadCurrent(ctx, testDB, u, "steam-external")
	if err != nil {
		t.Fatal(err)
	}
	unlocked, total = current.Counts()
	_, err = usergame.Acquire(ctx, testDB, usergame.AcquireParams{UserID: u, GameID: int32(game), Mode: usergame.ModeUpsert, Platforms: []usergame.PlatformInput{{
		Platform: &platform, Storefront: &store, SyncFromSource: true, SourceAccountID: &current.AccountID, SourceCredentialTag: &current.CredentialTag, ObservedAt: &current.ObservedAt, HoursPlayed: &current.HoursPlayed, AchievementsUnlocked: unlocked, AchievementsTotal: total,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	var account string
	if err := testDB.NewRaw(`SELECT o.hours_played,o.achievements_unlocked,o.source_account_id FROM progress_observations o JOIN user_game_platforms p ON p.id=o.user_game_platform_id WHERE p.user_game_id='steam-progress-game'`).Scan(ctx, &hours, &count, &account); err != nil {
		t.Fatal(err)
	}
	if hours != 1 || count != 0 || account != "account-b" {
		t.Fatalf("mixed account totals: %v/%v/%s", hours, count, account)
	}
}

func TestSteamProgressMigrationRoundTrip(t *testing.T) {
	ctx := context.Background()
	up, err := migrations.FS.ReadFile("20261029000002_steam_progress.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := migrations.FS.ReadFile("20261029000002_steam_progress.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("rollback fixture")
	err = testDB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.ExecContext(ctx, string(down)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(up)); err != nil {
			return err
		}
		var exists bool
		if err := tx.NewRaw(`SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name='progress_observations' AND column_name='source_account_id')`).Scan(ctx, &exists); err != nil {
			return err
		}
		if !exists {
			t.Error("account migration missing")
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
}
