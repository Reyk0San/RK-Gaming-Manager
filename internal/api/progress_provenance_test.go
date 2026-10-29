package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/drzero42/nexorious/internal/usergame"
)

func TestPlatformManualZeroAndExplicitOverrideClear(t *testing.T) {
	truncateAllTables(t)
	e := newTestEcho(t, testDB, testCfg())
	u, token := setupUserGamesUser(t, testDB, e, "progress-api")
	game := insertTestGame(t, testDB, "Progress API")
	insertTestUserGame(t, testDB, "progress-api-game", u, int(game))
	rec := postJSONAuth(t, e, "/api/user-games/progress-api-game/platforms", map[string]any{
		"platform": "pc-windows", "storefront": "steam", "hours_played": 0, "achievements_unlocked": 0,
	}, token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	platform, store := "pc-windows", "steam"
	hours, n := 12.0, 7
	_, err := usergame.Acquire(context.Background(), testDB, usergame.AcquireParams{UserID: u, GameID: int32(game), Mode: usergame.ModeUpsert,
		Platforms: []usergame.PlatformInput{{Platform: &platform, Storefront: &store, SyncFromSource: true, HoursPlayed: &hours, AchievementsUnlocked: &n}}})
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/user-games/progress-api-game/platforms/%s", created.ID)
	// Null/omitted metrics leave overrides intact.
	rec = putJSONAuth(t, e, path, map[string]any{"hours_played": nil}, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("null edit: %d %s", rec.Code, rec.Body.String())
	}
	var h float64
	var count int
	if err := testDB.NewRaw(`SELECT hours_played,achievements_unlocked FROM user_game_platforms WHERE id=?`, created.ID).Scan(context.Background(), &h, &count); err != nil {
		t.Fatal(err)
	}
	if h != 0 || count != 0 {
		t.Fatalf("manual zero lost: %v/%v", h, count)
	}
	rec = putJSONAuth(t, e, path, map[string]any{"clear_hours_override": true}, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body.String())
	}
	if err := testDB.NewRaw(`SELECT hours_played,achievements_unlocked FROM user_game_platforms WHERE id=?`, created.ID).Scan(context.Background(), &h, &count); err != nil {
		t.Fatal(err)
	}
	if h != 12 || count != 0 {
		t.Fatalf("clear must affect hours alone: %v/%v", h, count)
	}
}
