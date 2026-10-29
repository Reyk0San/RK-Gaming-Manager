package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/drzero42/nexorious/internal/auth"
	"github.com/drzero42/nexorious/internal/services/steamprogress"
	"github.com/labstack/echo/v5"
)

// HandleSteamProgress serves bounded pages of provider observations. Effective
// manual counts remain on the existing platform API; this endpoint never writes.
func (h *UserGamesHandler) HandleSteamProgress(c *echo.Context) error {
	userID := auth.UserIDFromContext(c)
	if userID == "" {
		return echo.NewHTTPError(http.StatusUnauthorized, "unauthorized")
	}
	ctx := c.Request().Context()
	var externalID string
	err := h.db.NewRaw(`SELECT p.external_game_id FROM user_game_platforms p
  JOIN user_games ug ON ug.id=p.user_game_id
  WHERE ug.id=? AND ug.user_id=? AND p.storefront='steam' AND p.external_game_id IS NOT NULL
  ORDER BY p.id LIMIT 1`, c.Param("id"), userID).Scan(ctx, &externalID)
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "Steam game not found")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "database error")
	}
	offset, limit := 0, 50
	for key, target := range map[string]*int{"offset": &offset, "limit": &limit} {
		if raw := c.QueryParam(key); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 0 || (key == "limit" && (value == 0 || value > 100)) {
				return echo.NewHTTPError(http.StatusBadRequest, "invalid pagination")
			}
			*target = value
		}
	}
	p, err := steamprogress.LoadCurrent(ctx, h.db, userID, externalID)
	if errors.Is(err, sql.ErrNoRows) {
		return c.JSON(http.StatusOK, map[string]any{"source": "steam", "available": false, "achievements": []any{}})
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "database error")
	}
	unlocked, total := p.Counts()
	var percent *float64
	if total != nil && *total > 0 {
		n := 100 * float64(*unlocked) / float64(*total)
		percent = &n
	}
	start := min(offset, len(p.Achievements))
	end := min(start+limit, len(p.Achievements))
	page := p.Achievements[start:end]
	if page == nil {
		page = []steamprogress.Achievement{}
	}
	return c.JSON(http.StatusOK, map[string]any{
		"source": "steam", "account_id": p.AccountID, "available": true,
		"observed_at": p.ObservedAt, "achievements_observed_at": p.AchievementsObservedAt,
		"hours_played": p.HoursPlayed, "achievements_unlocked": unlocked, "achievements_total": total,
		"completion_percent": percent, "achievements": page, "offset": offset, "limit": limit, "has_more": end < len(p.Achievements),
	})
}
