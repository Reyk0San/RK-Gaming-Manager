-- Effective values in legacy columns remain intact. Keep a full backup before
-- rollback: separate observations and manual provenance cannot survive a downgrade.
DROP TABLE progress_observations;
DROP TABLE progress_manual_overrides;
ALTER TABLE user_games DROP COLUMN play_status_manual;
ALTER TABLE user_game_platforms DROP COLUMN ownership_manual;
