package steam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/drzero42/nexorious/internal/observability"
)

// Client is an HTTP client for the Steam Web API.
type Client struct {
	http                *http.Client
	limiter             *rate.Limiter
	achievementsLimiter *rate.Limiter
	ownedGamesBase      string // default "https://api.steampowered.com"
	appDetailsBase      string // default "https://store.steampowered.com"
}

// NewClient creates a new Steam API client.
func NewClient() *Client {
	return &Client{
		http:                &http.Client{Transport: observability.HTTPTransport(), Timeout: 30 * time.Second},
		limiter:             rate.NewLimiter(rate.Every(200*time.Millisecond), 1),
		achievementsLimiter: rate.NewLimiter(rate.Every(200*time.Millisecond), 1),
		ownedGamesBase:      "https://api.steampowered.com",
		appDetailsBase:      "https://store.steampowered.com",
	}
}

// NewClientForTests creates a Steam API client with custom HTTP client, rate limiter,
// and base URLs. Only for use in tests.
func NewClientForTests(httpClient *http.Client, limiter *rate.Limiter, ownedGamesBase, appDetailsBase string) *Client {
	return &Client{
		http:                httpClient,
		limiter:             limiter,
		achievementsLimiter: rate.NewLimiter(rate.Inf, 1),
		ownedGamesBase:      ownedGamesBase,
		appDetailsBase:      appDetailsBase,
	}
}

// SteamPlayerSummary holds basic player info from the Steam API.
type SteamPlayerSummary struct {
	PersonaName              string
	CommunityVisibilityState int
}

// OwnedGame is a game entry from the user's Steam library.
type OwnedGame struct {
	AppID         int
	Title         string
	PlaytimeHours float64
}

// Platforms represents per-OS availability from the Steam store appdetails endpoint.
type Platforms struct {
	Windows bool
	Mac     bool
	Linux   bool
}

// GetPlayerSummaries fetches the player summary for the given steamID.
// Returns nil, nil if no player was found for that steamID.
func (c *Client) GetPlayerSummaries(ctx context.Context, apiKey, steamID string) (*SteamPlayerSummary, error) {
	url := fmt.Sprintf(
		"%s/ISteamUser/GetPlayerSummaries/v0002/?key=%s&steamids=%s&format=json",
		c.ownedGamesBase, apiKey, steamID,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errors.New("steam: invalid request")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errors.New("steam: network request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("steam rate limited")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("steam HTTP %d", resp.StatusCode)
	}

	var body struct {
		Response struct {
			Players []struct {
				PersonaName              string `json:"personaname"`
				CommunityVisibilityState int    `json:"communityvisibilitystate"`
			} `json:"players"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("steam decode error: %w", err)
	}
	if len(body.Response.Players) == 0 {
		return nil, nil
	}
	p := body.Response.Players[0]
	return &SteamPlayerSummary{
		PersonaName:              p.PersonaName,
		CommunityVisibilityState: p.CommunityVisibilityState,
	}, nil
}

// GetOwnedGames fetches the full Steam library for the given steamID.
// playtime_forever from the API is in minutes; this method converts it to hours.
func (c *Client) GetOwnedGames(ctx context.Context, apiKey, steamID string) ([]OwnedGame, error) {

	var body struct {
		Response struct {
			Games []struct {
				AppID           int    `json:"appid"`
				Name            string `json:"name"`
				PlaytimeForever *int   `json:"playtime_forever"`
			} `json:"games"`
			GameCount *int `json:"game_count"`
		} `json:"response"`
	}
	if err := c.webJSON(ctx, "/IPlayerService/GetOwnedGames/v0001/", url.Values{"key": {apiKey}, "steamid": {steamID}, "include_appinfo": {"true"}, "format": {"json"}}, &body); err != nil {
		return nil, err
	}

	if body.Response.Games == nil && (body.Response.GameCount == nil || *body.Response.GameCount != 0) {
		return nil, ErrProgressUnavailable
	}
	if body.Response.GameCount != nil && *body.Response.GameCount != len(body.Response.Games) {
		return nil, ErrProgressUnavailable
	}
	seen := make(map[int]bool)
	games := make([]OwnedGame, 0, len(body.Response.Games))
	for _, g := range body.Response.Games {
		if g.AppID <= 0 || seen[g.AppID] || g.PlaytimeForever == nil || *g.PlaytimeForever < 0 {
			return nil, ErrProgressUnavailable
		}
		seen[g.AppID] = true
		games = append(games, OwnedGame{
			AppID:         g.AppID,
			Title:         g.Name,
			PlaytimeHours: float64(*g.PlaytimeForever) / 60.0,
		})
	}
	return games, nil
}

// ErrRateLimited is returned by GetAppDetailsPlatforms when Steam responds with
// HTTP 429 and a brief retry does not help. The caller should back off globally
// before retrying the same request.
var ErrRateLimited = errors.New("steam: rate limited (429)")

// ErrAPIKeyRejected is returned by GetOwnedGames when the Steam API responds
// with HTTP 401 or 403, indicating the API key is invalid or revoked.
var ErrAPIKeyRejected = errors.New("steam: API key rejected")

// GetAppDetailsPlatforms fetches platform availability for the given appID.
// Returns (Platforms{}, nil) when success=false (removed/delisted app) — caller decides fallback.
// Returns (Platforms{}, ErrRateLimited) on 429 after a brief retry.
// Returns (Platforms{}, error) for network errors, decode errors, or missing key.
func (c *Client) GetAppDetailsPlatforms(ctx context.Context, appID int) (Platforms, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return Platforms{}, err
	}
	url := fmt.Sprintf("%s/api/appdetails?appids=%d&filters=platforms", c.appDetailsBase, appID)

	for attempt := 0; attempt <= 1; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return Platforms{}, fmt.Errorf("steam appdetails: build request: %w", err)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return Platforms{}, ctx.Err()
			}
			if attempt == 0 {
				if sleepErr := steamSleepCtx(ctx, 2*time.Second); sleepErr != nil {
					return Platforms{}, sleepErr
				}
				continue
			}
			return Platforms{}, fmt.Errorf("steam appdetails network error: %w", err)
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			_ = resp.Body.Close()
			if attempt == 0 {
				d := steamRetryAfterDelay(resp.Header.Get("Retry-After"))
				slog.DebugContext(ctx, "steam appdetails: 429 rate limited, waiting before retry", "appid", appID, "wait", d)
				if sleepErr := steamSleepCtx(ctx, d); sleepErr != nil {
					return Platforms{}, sleepErr
				}
				continue
			}
			return Platforms{}, ErrRateLimited
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return Platforms{}, fmt.Errorf("steam appdetails HTTP %d for appid %d", resp.StatusCode, appID)
		}

		var body map[string]struct {
			Success bool `json:"success"`
			Data    struct {
				Platforms struct {
					Windows bool `json:"windows"`
					Mac     bool `json:"mac"`
					Linux   bool `json:"linux"`
				} `json:"platforms"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			_ = resp.Body.Close()
			return Platforms{}, fmt.Errorf("steam appdetails decode error: %w", err)
		}
		_ = resp.Body.Close()

		key := fmt.Sprintf("%d", appID)
		entry, ok := body[key]
		if !ok {
			return Platforms{}, fmt.Errorf("steam appdetails: missing key %q in response", key)
		}
		if !entry.Success {
			// Steam has no current store data for this appid (removed/delisted games still
			// present in the user's library). Caller falls back to a default platform.
			slog.DebugContext(ctx, "steam appdetails: success=false (delisted/removed game)", "appid", appID)
			return Platforms{}, nil
		}
		return Platforms{
			Windows: entry.Data.Platforms.Windows,
			Mac:     entry.Data.Platforms.Mac,
			Linux:   entry.Data.Platforms.Linux,
		}, nil
	}
	return Platforms{}, ErrRateLimited
}

// steamRetryAfterDelay returns how long to wait before the one brief 429 retry.
// It honors a Retry-After header (integer seconds); otherwise defaults to 10s.
func steamRetryAfterDelay(header string) time.Duration {
	if header != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && secs >= 0 {
			return time.Duration(min(secs, 60)) * time.Second
		}
	}
	return 10 * time.Second
}

func steamSleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// GetPlayerAchievements fetches per-game achievement state for the given appID.
// Returns (unlocked, total, ok=true, nil) on a success=true response, where
// total = number of achievements and unlocked = number with achieved == 1.
// Returns (0, 0, false, nil) when Steam responds success=false (private profile
// OR the app has no achievements — indistinguishable without parsing the error
// string, and both map to "no badge"). Returns a non-nil error only on a
// transport, HTTP, or decode failure.
func (c *Client) GetPlayerAchievements(ctx context.Context, apiKey, steamID string, appID int) (int, int, bool, error) {
	if err := c.achievementsLimiter.Wait(ctx); err != nil {
		return 0, 0, false, err
	}
	url := fmt.Sprintf(
		"%s/ISteamUserStats/GetPlayerAchievements/v0001/?appid=%d&key=%s&steamid=%s&format=json",
		c.ownedGamesBase, appID, apiKey, steamID,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, 0, false, errors.New("steam achievements: invalid request")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, 0, false, errors.New("steam achievements: network request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	// 400/403 are Steam's response for private profiles on some apps; treat any
	// non-200 as "no data" rather than failing the sync.
	if resp.StatusCode != http.StatusOK {
		return 0, 0, false, nil
	}

	var body struct {
		PlayerStats struct {
			Success      bool `json:"success"`
			Achievements []struct {
				Achieved int `json:"achieved"`
			} `json:"achievements"`
		} `json:"playerstats"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, 0, false, fmt.Errorf("steam achievements decode error: %w", err)
	}
	if !body.PlayerStats.Success {
		return 0, 0, false, nil
	}
	total := len(body.PlayerStats.Achievements)
	unlocked := 0
	for _, a := range body.PlayerStats.Achievements {
		if a.Achieved == 1 {
			unlocked++
		}
	}
	return unlocked, total, true, nil
}
