CREATE TABLE steam_progress_snapshots (
    external_game_id text NOT NULL REFERENCES external_games(id) ON DELETE CASCADE,
    account_id text NOT NULL,
    credential_tag text NOT NULL,
    observed_at timestamptz NOT NULL,
    hours_played double precision NOT NULL CHECK (hours_played >= 0),
    achievements jsonb,
    achievements_observed_at timestamptz,
    PRIMARY KEY (external_game_id, account_id),
    CHECK (achievements IS NULL OR jsonb_typeof(achievements) = 'array'),
    CHECK ((achievements IS NULL) = (achievements_observed_at IS NULL))
);
ALTER TABLE progress_observations ADD COLUMN source_account_id text;
