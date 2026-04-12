-- +goose Up
ALTER TABLE sites ADD COLUMN parent_domain VARCHAR(253) NULL;
CREATE INDEX idx_sites_parent_domain ON sites(parent_domain);

-- +goose Down
-- intentionally empty
