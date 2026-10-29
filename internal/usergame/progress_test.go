package usergame

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/drzero42/nexorious/internal/db/migrations"
	"github.com/uptrace/bun"
)

func progressFixture(t *testing.T) (string, string, string) {
	t.Helper()
	truncateAllTables(t)
	u := seedUser(t)
	ug := seedUserGame(t, u, 9900)
	res, err := AddPlatform(context.Background(), testDB, AddPlatformParams{UserID: u, UserGameID: ug,
		Platform: PlatformInput{Platform: strptr("pc-windows"), Storefront: strptr("steam"), HoursPlayed: fptr(0)}})
	if err != nil {
		t.Fatal(err)
	}
	return u, ug, res.PlatformID
}

func syncProgress(t *testing.T, u string, h float64, n int, at time.Time) {
	t.Helper()
	_, err := Acquire(context.Background(), testDB, AcquireParams{UserID: u, GameID: 9900, Mode: ModeUpsert,
		Platforms: []PlatformInput{{Platform: strptr("pc-windows"), Storefront: strptr("steam"),
			SyncFromSource: true, HoursPlayed: fptr(h), AchievementsUnlocked: &n, ObservedAt: &at}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestProgressManualZeroRepeatedMissingAndStaleSync(t *testing.T) {
	u, ug, id := progressFixture(t)
	zero := 0
	if err := UpdatePlatform(context.Background(), testDB, UpdatePlatformParams{UserID: u, UserGameID: ug, PlatformID: id,
		Fields: PlatformInput{AchievementsUnlocked: &zero}}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	syncProgress(t, u, 15, 7, at)
	syncProgress(t, u, 15, 7, at)
	syncProgress(t, u, 99, 99, at.Add(-time.Hour))
	_, err := Acquire(context.Background(), testDB, AcquireParams{UserID: u, GameID: 9900, Mode: ModeUpsert,
		Platforms: []PlatformInput{{Platform: strptr("pc-windows"), Storefront: strptr("steam"), SyncFromSource: true}}})
	if err != nil {
		t.Fatal(err)
	}
	var h float64
	var n int
	if err := testDB.NewRaw(`SELECT hours_played, achievements_unlocked FROM user_game_platforms WHERE id=?`, id).Scan(context.Background(), &h, &n); err != nil {
		t.Fatal(err)
	}
	if h != 0 || n != 0 {
		t.Fatalf("manual zero overwritten: %v / %v", h, n)
	}
	if err := testDB.NewRaw(`SELECT hours_played, achievements_unlocked FROM progress_observations WHERE user_game_platform_id=?`, id).Scan(context.Background(), &h, &n); err != nil {
		t.Fatal(err)
	}
	if h != 15 || n != 7 {
		t.Fatalf("observations lost or stale accepted: %v / %v", h, n)
	}
}

func TestProgressIsolationAndConcurrentManualEdit(t *testing.T) {
	u, ug, id := progressFixture(t)
	other := seedUser(t)
	if err := UpdatePlatform(context.Background(), testDB, UpdatePlatformParams{UserID: other, UserGameID: ug, PlatformID: id, Fields: PlatformInput{HoursPlayed: fptr(99)}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user: %v", err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				errs <- UpdatePlatform(context.Background(), testDB, UpdatePlatformParams{UserID: u, UserGameID: ug, PlatformID: id, Fields: PlatformInput{HoursPlayed: fptr(2)}})
			} else {
				_, err := Acquire(context.Background(), testDB, AcquireParams{UserID: u, GameID: 9900, Mode: ModeUpsert, Platforms: []PlatformInput{{Platform: strptr("pc-windows"), Storefront: strptr("steam"), SyncFromSource: true, HoursPlayed: fptr(300)}}})
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var h float64
	if err := testDB.NewRaw(`SELECT hours_played FROM user_game_platforms WHERE id=?`, id).Scan(context.Background(), &h); err != nil {
		t.Fatal(err)
	}
	if h != 2 {
		t.Fatalf("concurrent sync overwrote manual edit: %v", h)
	}
}

func TestProgressMigrationPreservesAmbiguousValuesAndRollback(t *testing.T) {
	u, ug, id := progressFixture(t)
	ctx := context.Background()
	rollback := errors.New("rollback test fixture")
	err := testDB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		down, err := migrations.FS.ReadFile("20261029000001_progress_provenance.down.sql")
		if err != nil {
			return err
		}
		up, err := migrations.FS.ReadFile("20261029000001_progress_provenance.up.sql")
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, string(down)); err != nil {
			return err
		}
		if _, err = tx.NewRaw(`UPDATE user_game_platforms SET sync_from_source=true,hours_played=0,achievements_unlocked=0,achievements_total=NULL WHERE id=?`, id).Exec(ctx); err != nil {
			return err
		}

		if _, err = tx.NewRaw(`INSERT INTO external_games (id,user_id,storefront,external_id,title) VALUES ('migration-source',?,'steam','migrate','Migration')`, u).Exec(ctx); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO external_game_platforms (id,external_game_id,platform,hours_played,achievements_unlocked,achievements_total) VALUES ('migration-observation','migration-source','pc-windows',100,8,10)`); err != nil {
			return err
		}
		if _, err = tx.NewRaw(`UPDATE user_game_platforms SET external_game_id='migration-source' WHERE id=?`, id).Exec(ctx); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, string(up)); err != nil {
			return err
		}
		var h float64
		var n int
		var total *int
		var manual bool
		if err = tx.NewRaw(`SELECT hours_played,achievements_unlocked,achievements_total FROM progress_manual_overrides WHERE user_game_platform_id=?`, id).Scan(ctx, &h, &n, &total); err != nil {
			return err
		}
		if h != 0 || n != 0 || total != nil {
			t.Fatalf("migration changed values: %v %v %v", h, n, total)
		}
		if err = tx.NewRaw(`SELECT play_status_manual FROM user_games WHERE id=?`, ug).Scan(ctx, &manual); err != nil {
			return err
		}
		if !manual {
			t.Fatal("ambiguous status not protected")
		}
		var observed float64
		if err = tx.NewRaw(`SELECT hours_played FROM progress_observations WHERE user_game_platform_id=?`, id).Scan(ctx, &observed); err != nil {
			return err
		}
		if observed != 100 {
			t.Fatalf("external observation lost: %v", observed)
		}

		if _, err = tx.ExecContext(ctx, string(down)); err != nil {
			return err
		}
		if err = tx.NewRaw(`SELECT hours_played,achievements_unlocked FROM user_game_platforms WHERE id=?`, id).Scan(ctx, &h, &n); err != nil {
			return err
		}
		if h != 0 || n != 0 {
			t.Fatal("rollback lost effective values")
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
}

func TestProgressFreshImportRestoresBothLayers(t *testing.T) {
	u, _, _ := progressFixture(t)
	seedGame(t, 9901, "Imported")
	unlocked := 7
	state := &ProgressState{Manual: ProgressMetrics{HoursPlayed: fptr(0)}, Observed: ProgressMetrics{HoursPlayed: fptr(20), AchievementsUnlocked: &unlocked}}
	res, err := Acquire(context.Background(), testDB, AcquireParams{UserID: u, GameID: 9901, Mode: ModeImport,
		Platforms: []PlatformInput{{Platform: strptr("pc-windows"), Storefront: strptr("steam"), HoursPlayed: fptr(0), Progress: state}}})
	if err != nil {
		t.Fatal(err)
	}
	var manual, observed, effective float64
	err = testDB.NewRaw(`SELECT m.hours_played,o.hours_played,p.hours_played FROM user_game_platforms p
        JOIN progress_manual_overrides m ON m.user_game_platform_id=p.id JOIN progress_observations o ON o.user_game_platform_id=p.id WHERE p.user_game_id=?`, res.UserGameID).Scan(context.Background(), &manual, &observed, &effective)
	if err != nil {
		t.Fatal(err)
	}
	if manual != 0 || observed != 20 || effective != 0 {
		t.Fatalf("bad imported layers: %v %v %v", manual, observed, effective)
	}
}

func TestProgressExplicitNotStartedSurvivesSync(t *testing.T) {
	u, ug, _ := progressFixture(t)
	if err := RecordProgress(context.Background(), testDB, ProgressParams{UserID: u, UserGameID: ug, PlayStatus: "not_started"}); err != nil {
		t.Fatal(err)
	}
	syncProgress(t, u, 40, 5, time.Now())
	if status := fetchUG(t, ug).PlayStatus; status == nil || *status != "not_started" {
		t.Fatalf("explicit status overwritten: %v", status)
	}
}

func TestProgressClearIsExplicitAndFieldScoped(t *testing.T) {
	u, ug, id := progressFixture(t)
	syncProgress(t, u, 12, 3, time.Now())
	if err := UpdatePlatform(context.Background(), testDB, UpdatePlatformParams{UserID: u, UserGameID: ug, PlatformID: id, Fields: PlatformInput{ClearHoursOverride: true}}); err != nil {
		t.Fatal(err)
	}
	var h float64
	if err := testDB.NewRaw(`SELECT hours_played FROM user_game_platforms WHERE id=?`, id).Scan(context.Background(), &h); err != nil {
		t.Fatal(err)
	}
	if h != 12 {
		t.Fatalf("clear did not restore source: %v", h)
	}
}

func TestProgressRejectsForeignExternalLinkAndPlatform(t *testing.T) {
	u, ug, id := progressFixture(t)
	other := seedUser(t)
	otherGame := seedUserGame(t, other, 9900)
	_, err := testDB.NewRaw(`INSERT INTO external_games (id,user_id,storefront,external_id,title) VALUES ('foreign-source',?,'steam','1','Foreign')`, other).Exec(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = UpdatePlatform(context.Background(), testDB, UpdatePlatformParams{UserID: u, UserGameID: ug, PlatformID: id, Fields: PlatformInput{ExternalGameID: strptr("foreign-source"), HoursPlayed: fptr(99)}})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign external link accepted: %v", err)
	}
	err = UpdatePlatform(context.Background(), testDB, UpdatePlatformParams{UserID: other, UserGameID: otherGame, PlatformID: id, Fields: PlatformInput{HoursPlayed: fptr(99)}})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign platform accepted: %v", err)
	}
	var h float64
	if err := testDB.NewRaw(`SELECT hours_played FROM user_game_platforms WHERE id=?`, id).Scan(context.Background(), &h); err != nil {
		t.Fatal(err)
	}
	if h != 0 {
		t.Fatal("rejected mutation was not rolled back")
	}
}

func TestProgressNullableStorefrontRepeatedSync(t *testing.T) {
	u, _, _ := progressFixture(t)
	for i := 0; i < 2; i++ {
		_, err := Acquire(context.Background(), testDB, AcquireParams{UserID: u, GameID: 9900, Mode: ModeUpsert,
			Platforms: []PlatformInput{{Platform: strptr("pc-windows"), SyncFromSource: true, HoursPlayed: fptr(4)}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := testDB.NewRaw(`SELECT count(*) FROM user_game_platforms WHERE storefront IS NULL`).Scan(context.Background(), &n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("duplicate nullable storefront: %v", n)
	}
}

func TestProgressReimportDoesNotTurnObservedValueIntoManual(t *testing.T) {
	u, _, _ := progressFixture(t)
	seedGame(t, 9902, "Observed import")
	input := AcquireParams{UserID: u, GameID: 9902, Mode: ModeImport, Platforms: []PlatformInput{{
		Platform: strptr("pc-windows"), Storefront: strptr("steam"), HoursPlayed: fptr(20),
		Progress: &ProgressState{Observed: ProgressMetrics{HoursPlayed: fptr(20)}},
	}}}
	for i := 0; i < 2; i++ {
		if _, err := Acquire(context.Background(), testDB, input); err != nil {
			t.Fatal(err)
		}
	}
	input.Mode = ModeUpsert
	input.Platforms[0].Progress = nil
	input.Platforms[0].SyncFromSource = true
	input.Platforms[0].HoursPlayed = fptr(30)
	res, err := Acquire(context.Background(), testDB, input)
	if err != nil {
		t.Fatal(err)
	}
	var h float64
	if err := testDB.NewRaw(`SELECT hours_played FROM user_game_platforms WHERE user_game_id=?`, res.UserGameID).Scan(context.Background(), &h); err != nil {
		t.Fatal(err)
	}
	if h != 30 {
		t.Fatalf("reimport froze observation as manual: %v", h)
	}
}

func TestProgressManualOwnershipSurvivesSync(t *testing.T) {
	u, ug, id := progressFixture(t)
	if err := UpdatePlatform(context.Background(), testDB, UpdatePlatformParams{UserID: u, UserGameID: ug, PlatformID: id, Fields: PlatformInput{OwnershipStatus: strptr("no_longer_owned")}}); err != nil {
		t.Fatal(err)
	}
	syncProgress(t, u, 99, 2, time.Now())
	var ownership string
	if err := testDB.NewRaw(`SELECT ownership_status FROM user_game_platforms WHERE id=?`, id).Scan(context.Background(), &ownership); err != nil {
		t.Fatal(err)
	}
	if ownership != "no_longer_owned" {
		t.Fatalf("manual ownership overwritten: %v", ownership)
	}
}
