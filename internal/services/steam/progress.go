package steam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/drzero42/nexorious/internal/services/steamprogress"
)

type Achievement = steamprogress.Achievement
type Progress = steamprogress.Progress

var ErrProgressUnavailable = errors.New("steam: progress unavailable or incomplete")

// webJSON bounds response size, rate, retry count and request duration. Errors
// deliberately omit the URL: it contains the API key. Do not wrap url.Error.
func (c *Client) webJSON(ctx context.Context, path string, values url.Values, dst any) error {
	endpoint := c.ownedGamesBase + path + "?" + values.Encode()
	for attempt := 0; attempt < 3; attempt++ {
		if err := c.achievementsLimiter.Wait(ctx); err != nil {
			return err
		}
		reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
		if err != nil {
			cancel()
			return errors.New("steam: invalid request")
		}
		resp, err := c.http.Do(req)
		delay := time.Duration(1<<attempt) * time.Second
		if err == nil {
			status := resp.StatusCode
			if status == http.StatusOK {
				err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(dst)
				_ = resp.Body.Close()
				cancel()
				if err != nil {
					return errors.New("steam: invalid JSON response")
				}
				return nil
			}
			_ = resp.Body.Close()
			if status == http.StatusTooManyRequests {
				delay = min(steamRetryAfterDelay(resp.Header.Get("Retry-After")), time.Minute)
			}
			if status == http.StatusUnauthorized || status == http.StatusForbidden {
				cancel()
				return ErrAPIKeyRejected
			}
			if status != http.StatusTooManyRequests && status < 500 {
				cancel()
				return ErrProgressUnavailable
			}
		}
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt == 2 {
			return errors.New("steam: API unavailable after retries")
		}
		if err := steamSleepCtx(ctx, delay); err != nil {
			return err
		}
	}
	return ErrProgressUnavailable
}

// GetAchievements joins the complete schema with the complete player state.
// Steam does not paginate these endpoints. Reject mismatches instead of turning
// missing state into locked achievements or overwriting a previously known list.
func (c *Client) GetAchievements(ctx context.Context, apiKey, steamID string, appID int) ([]Achievement, error) {
	values := url.Values{"key": {apiKey}, "appid": {strconv.Itoa(appID)}, "steamid": {steamID}, "l": {"english"}, "format": {"json"}}
	var player struct {
		Stats struct {
			Success      bool   `json:"success"`
			SteamID      string `json:"steamID"`
			Achievements []struct {
				ID         string `json:"apiname"`
				Achieved   *int   `json:"achieved"`
				UnlockTime int64  `json:"unlocktime"`
			} `json:"achievements"`
		} `json:"playerstats"`
	}
	if err := c.webJSON(ctx, "/ISteamUserStats/GetPlayerAchievements/v0001/", values, &player); err != nil {
		return nil, err
	}
	if !player.Stats.Success || player.Stats.Achievements == nil || (player.Stats.SteamID != "" && player.Stats.SteamID != steamID) {
		return nil, ErrProgressUnavailable
	}
	var schema struct {
		Game struct {
			Stats *struct {
				Achievements []struct {
					ID          string `json:"name"`
					Title       string `json:"displayName"`
					Description string `json:"description"`
					Icon        string `json:"icon"`
					IconGray    string `json:"icongray"`
				} `json:"achievements"`
			} `json:"availableGameStats"`
		} `json:"game"`
	}
	if err := c.webJSON(ctx, "/ISteamUserStats/GetSchemaForGame/v2/", values, &schema); err != nil {
		return nil, err
	}
	if schema.Game.Stats == nil || schema.Game.Stats.Achievements == nil || len(schema.Game.Stats.Achievements) != len(player.Stats.Achievements) {
		return nil, ErrProgressUnavailable
	}
	states := make(map[string]Achievement)
	for _, a := range player.Stats.Achievements {
		if a.ID == "" || a.Achieved == nil || (*a.Achieved != 0 && *a.Achieved != 1) || a.UnlockTime < 0 {
			return nil, ErrProgressUnavailable
		}
		if _, exists := states[a.ID]; exists {
			return nil, ErrProgressUnavailable
		}
		item := Achievement{ID: a.ID, Unlocked: *a.Achieved == 1}
		if item.Unlocked && a.UnlockTime > 0 {
			at := time.Unix(a.UnlockTime, 0).UTC()
			item.UnlockedAt = &at
		}
		states[a.ID] = item
	}
	result := make([]Achievement, 0, len(states))
	for _, a := range schema.Game.Stats.Achievements {
		item, ok := states[a.ID]
		if !ok {
			return nil, fmt.Errorf("%w: schema mismatch", ErrProgressUnavailable)
		}
		delete(states, a.ID)
		item.Title, item.Description, item.Icon, item.IconGray = a.Title, a.Description, safeSteamIcon(a.Icon), safeSteamIcon(a.IconGray)
		result = append(result, item)
	}
	return result, nil
}

func safeSteamIcon(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return ""
	}
	switch u.Hostname() {
	case "cdn.steamstatic.com", "cdn.akamai.steamstatic.com", "cdn.cloudflare.steamstatic.com", "steamcdn-a.akamaihd.net", "shared.akamai.steamstatic.com", "steamcommunity-a.akamaihd.net":
		return raw
	}
	return ""
}
