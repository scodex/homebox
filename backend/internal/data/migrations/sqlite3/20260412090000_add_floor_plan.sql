-- +goose Up
ALTER TABLE locations ADD COLUMN floor_plan_path TEXT DEFAULT '';
ALTER TABLE locations ADD COLUMN floor_plan_mime_type TEXT DEFAULT '';

ALTER TABLE items ADD COLUMN floor_plan_x REAL DEFAULT 0;
ALTER TABLE items ADD COLUMN floor_plan_y REAL DEFAULT 0;

-- Sub-locations also need coordinates on the parent's floor plan
ALTER TABLE locations ADD COLUMN floor_plan_x REAL DEFAULT 0;
ALTER TABLE locations ADD COLUMN floor_plan_y REAL DEFAULT 0;
