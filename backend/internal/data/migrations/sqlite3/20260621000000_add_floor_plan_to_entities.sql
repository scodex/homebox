-- +goose Up
ALTER TABLE entities ADD COLUMN floor_plan_path TEXT DEFAULT '';
ALTER TABLE entities ADD COLUMN floor_plan_mime_type TEXT DEFAULT '';
ALTER TABLE entities ADD COLUMN floor_plan_x REAL DEFAULT 0;
ALTER TABLE entities ADD COLUMN floor_plan_y REAL DEFAULT 0;

-- +goose Down
ALTER TABLE entities DROP COLUMN floor_plan_path;
ALTER TABLE entities DROP COLUMN floor_plan_mime_type;
ALTER TABLE entities DROP COLUMN floor_plan_x;
ALTER TABLE entities DROP COLUMN floor_plan_y;
