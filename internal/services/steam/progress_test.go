package steam_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drzero42/nexorious/internal/services/steam"
	"golang.org/x/time/rate"
)

func TestDetailedAchievements(t *testing.T) {
	cases := []struct {
		name, player, schema string
		valid                bool
	}{
		{"complete", `{"success":true,"steamID":"123","achievements":[{"apiname":"A","achieved":1,"unlocktime":1700000000},{"apiname":"B","achieved":0}]}`, `{"availableGameStats":{"achievements":[{"name":"A","displayName":"Winner","description":"Win once","icon":"https://cdn.akamai.steamstatic.com/icon.jpg"},{"name":"B","displayName":"Hidden"}]}}`, true},
		{"empty_confirmed", `{"success":true,"achievements":[]}`, `{"availableGameStats":{"achievements":[]}}`, true},
		{"private", `{"success":false}`, `{}`, false},
		{"missing_list", `{"success":true}`, `{}`, false},
		{"missing_schema", `{"success":true,"achievements":[]}`, `{}`, false},
		{"partial", `{"success":true,"achievements":[{"apiname":"A","achieved":1}]}`, `{"availableGameStats":{"achievements":[{"name":"A"},{"name":"B"}]}}`, false},
		{"missing_state", `{"success":true,"achievements":[{"apiname":"A"}]}`, `{"availableGameStats":{"achievements":[{"name":"A"}]}}`, false},
		{"invalid_state", `{"success":true,"achievements":[{"apiname":"A","achieved":2}]}`, `{"availableGameStats":{"achievements":[{"name":"A"}]}}`, false},
		{"wrong_account", `{"success":true,"steamID":"456","achievements":[]}`, `{"availableGameStats":{"achievements":[]}}`, false},
		{"duplicate", `{"success":true,"achievements":[{"apiname":"A","achieved":1},{"apiname":"A","achieved":0}]}`, `{"availableGameStats":{"achievements":[{"name":"A"},{"name":"B"}]}}`, false},
		{"different_ids", `{"success":true,"achievements":[{"apiname":"A","achieved":1}]}`, `{"availableGameStats":{"achievements":[{"name":"B"}]}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "GetPlayerAchievements") {
					fmt.Fprintf(w, `{"playerstats":%s}`, tc.player)
				} else {
					fmt.Fprintf(w, `{"game":%s}`, tc.schema)
				}
			}))
			defer srv.Close()
			c := steam.NewClientForTests(srv.Client(), rate.NewLimiter(rate.Inf, 1), srv.URL, srv.URL)
			got, err := c.GetAchievements(context.Background(), "secret", "123", 1)
			if tc.valid {
				if err != nil || got == nil {
					t.Fatalf("expected known list, got %v %v", got, err)
				}
			} else if !errors.Is(err, steam.ErrProgressUnavailable) {
				t.Fatalf("expected unavailable, got %v", err)
			}
			if tc.name == "complete" {
				if len(got) != 2 || got[0].Title != "Winner" || got[0].Description != "Win once" || got[0].UnlockedAt == nil || got[0].UnlockedAt.Unix() != 1700000000 || got[1].Unlocked || got[1].UnlockedAt != nil {
					t.Fatalf("bad details: %+v", got)
				}
			}
		})
	}
}

func TestSteamRetryAndCancellation(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) <= 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			return
		}
		if strings.Contains(r.URL.Path, "GetPlayerAchievements") {
			fmt.Fprint(w, `{"playerstats":{"success":true,"achievements":[]}}`)
		} else {
			fmt.Fprint(w, `{"game":{"availableGameStats":{"achievements":[]}}}`)
		}
	}))
	defer srv.Close()
	c := steam.NewClientForTests(srv.Client(), rate.NewLimiter(rate.Inf, 1), srv.URL, srv.URL)
	if _, err := c.GetAchievements(context.Background(), "secret", "123", 1); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 4 {
		t.Fatalf("requests=%d", hits.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.GetAchievements(ctx, "secret", "123", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

type brokenSteamTransport struct{}

func (brokenSteamTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("transport secret detail")
}
func TestSteamErrorsDoNotExposeCredentials(t *testing.T) {
	c := steam.NewClientForTests(&http.Client{Transport: brokenSteamTransport{}}, rate.NewLimiter(rate.Inf, 1), "https://example.com", "https://example.com")
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, err := c.GetAchievements(ctx, "VERY_SECRET_KEY", "123", 1)
	if err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "https://") || strings.Contains(err.Error(), "secret detail") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestOwnedGamesRejectsIncompleteResponses(t *testing.T) {
	for _, body := range []string{`{}`, `{"response":{}}`, `{"response":{"game_count":2,"games":[]}}`, `{"response":{"games":[{"appid":1,"name":"missing time"}]}}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		c := steam.NewClientForTests(srv.Client(), rate.NewLimiter(rate.Inf, 1), srv.URL, srv.URL)
		if _, err := c.GetOwnedGames(context.Background(), "secret", "123"); err == nil {
			t.Errorf("accepted incomplete response %s", body)
		}
		srv.Close()
	}
}

func TestSteamRateLimitRetriesAreBounded(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(429)
	}))
	defer srv.Close()
	c := steam.NewClientForTests(srv.Client(), rate.NewLimiter(rate.Inf, 1), srv.URL, srv.URL)
	if _, err := c.GetAchievements(context.Background(), "secret", "123", 1); err == nil {
		t.Fatal("expected exhausted retries")
	}
	if hits.Load() != 3 {
		t.Fatalf("unbounded requests: %d", hits.Load())
	}
}
