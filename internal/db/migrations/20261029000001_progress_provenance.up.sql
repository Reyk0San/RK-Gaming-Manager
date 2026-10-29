-- Preserve ambiguous legacy values, including zero, as manual overrides.
-- The platform FK is the ownership boundary; no independent user/source IDs
-- can be attached to another user's platform.
CREATE TABLE progress_manual_overrides (
    user_game_platform_id text PRIMARY KEY REFERENCES user_game_platforms(id) ON DELETE CASCADE,
    hours_played numeric(10,2),
    achievements_unlocked integer,
    achievements_total integer,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE progress_observations (
    user_game_platform_id text PRIMARY KEY REFERENCES user_game_platforms(id) ON DELETE CASCADE,
    hours_played numeric(10,2),
    achievements_unlocked integer,
    achievements_total integer,
    observed_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO progress_manual_overrides
    (user_game_platform_id, hours_played, achievements_unlocked, achievements_total, updated_at)
SELECT id, hours_played, achievements_unlocked, achievements_total, updated_at
FROM user_game_platforms;
INSERT INTO progress_observations
    (user_game_platform_id, hours_played, achievements_unlocked, achievements_total, observed_at)
SELECT p.id, e.hours_played, e.achievements_unlocked, e.achievements_total, eg.updated_at
FROM user_game_platforms p
JOIN user_games ug ON ug.id = p.user_game_id
JOIN external_games eg ON eg.id = p.external_game_id AND eg.user_id = ug.user_id
JOIN external_game_platforms e ON e.external_game_id = eg.id AND e.platform = p.platform
;
ALTER TABLE user_games ADD COLUMN play_status_manual boolean NOT NULL DEFAULT false;
UPDATE user_games SET play_status_manual = true;
ALTER TABLE user_game_platforms ADD COLUMN ownership_manual boolean NOT NULL DEFAULT false;
UPDATE user_game_platforms SET ownership_manual = true;
