-- +goose Up
ALTER TABLE sites ADD COLUMN parent_domain TEXT;
CREATE INDEX IF NOT EXISTS idx_sites_parent_domain ON sites(parent_domain);

-- +goose Down
-- intentionally empty
