package tasks_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/drzero42/nexorious/internal/services/steam"
	"github.com/drzero42/nexorious/internal/services/steamprogress"
	"github.com/drzero42/nexorious/internal/worker/tasks"
	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"golang.org/x/time/rate"
)

func TestSteamProgressSimulatedAPIThroughWorkers(t *testing.T) {
	truncateAllTables(t)
	ctx := context.Background()
	userID := uuid.NewString()
	configID := uuid.NewString()
	insertTestUser(t, testDB, userID)
	if _, err := testDB.NewRaw(`INSERT INTO user_sync_configs(id,user_id,storefront,frequency,storefront_credentials) VALUES (?,?,'steam','daily','encrypted')`, configID, userID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	var private atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "GetOwnedGames"):
			fmt.Fprint(w, `{"response":{"game_count":1,"games":[{"appid":730,"name":"Steam test","playtime_forever":90}]}}`)
		case strings.Contains(r.URL.Path, "appdetails"):
			fmt.Fprint(w, `{"730":{"success":true,"data":{"platforms":{"windows":true,"linux":true}}}}`)
		case strings.Contains(r.URL.Path, "GetPlayerAchievements"):
			if private.Load() {
				fmt.Fprint(w, `{"playerstats":{"success":false}}`)
				return
			}
			fmt.Fprint(w, `{"playerstats":{"success":true,"steamID":"123","achievements":[{"apiname":"A","achieved":1,"unlocktime":1700000000}]}}`)
		case strings.Contains(r.URL.Path, "GetSchemaForGame"):
			fmt.Fprint(w, `{"game":{"availableGameStats":{"achievements":[{"name":"A","displayName":"Win","description":"Win once"}]}}}`)
		default:
			t.Errorf("unexpected API %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	client := steam.NewClientForTests(srv.Client(), rate.NewLimiter(rate.Inf, 1), srv.URL, srv.URL)
	dispatch := tasks.DispatchSyncWorker{DB: testDB, Adapter: adapterFactory(steam.NewAdapterForTests(client, "secret", "123", 0))}
	run := func() {
		jobID := uuid.NewString()
		if _, err := testDB.NewRaw(`INSERT INTO jobs(id,user_id,job_type,source,status,priority,total_items) VALUES (?,?,'sync','steam','pending','low',0)`, jobID, userID).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		if err := dispatch.Work(ctx, &river.Job[tasks.DispatchSyncArgs]{Args: tasks.DispatchSyncArgs{JobID: jobID, UserID: userID, Storefront: "steam"}}); err != nil {
			t.Fatal(err)
		}
		var egID, itemID string
		if err := testDB.NewRaw(`SELECT id FROM external_games WHERE user_id=? AND external_id='730'`, userID).Scan(ctx, &egID); err != nil {
			t.Fatal(err)
		}
		if _, err := testDB.NewRaw(`INSERT INTO games(id,title) VALUES (991122,'Steam test') ON CONFLICT DO NOTHING`).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := testDB.NewRaw(`UPDATE external_games SET resolved_igdb_id=991122 WHERE id=?`, egID).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		if err := testDB.NewRaw(`SELECT id FROM job_items WHERE job_id=?`, jobID).Scan(ctx, &itemID); err != nil {
			t.Fatal(err)
		}
		writer := tasks.UserGameWorker{DB: testDB}
		if err := writer.Work(ctx, &river.Job[tasks.UserGameArgs]{Args: tasks.UserGameArgs{JobItemID: itemID}}); err != nil {
			t.Fatal(err)
		}
		p, err := steamprogress.LoadCurrent(ctx, testDB, userID, egID)
		if err != nil || len(p.Achievements) != 1 || p.Achievements[0].Title != "Win" {
			t.Fatalf("snapshot: %+v %v", p, err)
		}
		var hours float64
		var unlocked int
		if err := testDB.NewRaw(`SELECT SUM(p.hours_played),SUM(p.achievements_unlocked) FROM user_game_platforms p JOIN user_games ug ON ug.id=p.user_game_id WHERE ug.user_id=?`, userID).Scan(ctx, &hours, &unlocked); err != nil {
			t.Fatal(err)
		}
		if hours != 1.5 || unlocked != 1 {
			t.Fatalf("expected single app total 1.5h/1 success, got %v/%v", hours, unlocked)
		}
	}
	run()
	// A second run with private achievement data keeps the successful snapshot,
	// and repeated writes do not double the total across the two OS rows.
	private.Store(true)
	run()
}
