package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
)


func ensureColumn(tx *sql.Tx, table, col, colDef string) error {
	if tx == nil {
		return fmt.Errorf("tx is nil")
	}
	table = strings.TrimSpace(table)
	col = strings.TrimSpace(col)
	if table == "" || col == "" {
		return fmt.Errorf("ensureColumn: table/col required")
	}

	rows, err := tx.Query(fmt.Sprintf("PRAGMA table_info(%s);", table))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		// PRAGMA table_info: cid, name, type, notnull, dflt_value, pk
		var (
			cid       int
			name      string
			ctype     string
			notnull   int
			dflt      sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if strings.EqualFold(name, col) {
			return nil // already exists
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	_, err = tx.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s;", table, col, colDef))
	return err
}






func migrate(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}


	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Users
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS users(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			home_dir TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		);
	`); err != nil {
		return err
	}

	// Sites
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS sites(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			domain TEXT NOT NULL UNIQUE,
			mode TEXT NOT NULL DEFAULT 'php',
			webroot TEXT NOT NULL,
			php_version TEXT NOT NULL DEFAULT '',
			enable_http3 INTEGER NOT NULL DEFAULT 1,
			enabled INTEGER NOT NULL DEFAULT 1,
                        deleted_at TEXT,

			-- TLS / certificate source
			-- tls_mode: 'letsencrypt' | 'custom' | 'off'
			tls_mode TEXT NOT NULL DEFAULT 'letsencrypt',
			tls_cert_path TEXT NOT NULL DEFAULT '',
			tls_key_path  TEXT NOT NULL DEFAULT '',

			-- Optional per-site overrides (normally global cfg)
			acme_webroot_override TEXT NOT NULL DEFAULT '',
			letsencrypt_email_override TEXT NOT NULL DEFAULT '',

			-- Helpful later (UI / renew scheduler). Can be NULL/empty for now.
			cert_issued_at  TEXT,
			cert_expires_at TEXT,
			last_cert_error TEXT NOT NULL DEFAULT '',


			last_render_hash TEXT NOT NULL DEFAULT '',
			last_applied_at TEXT,
			last_apply_status TEXT NOT NULL DEFAULT '',
			last_apply_error TEXT NOT NULL DEFAULT '',

			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),

			FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
		);
	`); err != nil {
		return err
	}

	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_sites_user_id ON sites(user_id);`); err != nil {
		return err
	}


	// ---- site per-site nginx knobs (backfill for existing DBs) ----
	// empty string means "use app defaults" during template render
	if err := ensureColumn(tx, "sites", "client_max_body_size", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureColumn(tx, "sites", "php_time_read", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureColumn(tx, "sites", "php_time_send", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}




	// Proxy targets (for mode=proxy later; supports ip:port and unix:/path.sock)
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS proxy_targets(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			site_id INTEGER NOT NULL,
			target TEXT NOT NULL,              -- e.g. "127.0.0.1:8080" or "unix:/run/app.sock"
			weight INTEGER NOT NULL DEFAULT 100,
			is_backup INTEGER NOT NULL DEFAULT 0,
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			UNIQUE(site_id, target),
			FOREIGN KEY(site_id) REFERENCES sites(id) ON DELETE CASCADE
		);
	`); err != nil {
		return err
	}

	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_proxy_targets_site_id ON proxy_targets(site_id);`); err != nil {
		return err
	}



	// Apply runs (audit-ish)
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS apply_runs(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			site_id INTEGER,
			action TEXT NOT NULL,
			status TEXT NOT NULL,
			message TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			FOREIGN KEY(site_id) REFERENCES sites(id) ON DELETE SET NULL
		);
	`); err != nil {
		return err
	}





	// Panel users (NGM UI/API login)
	if _, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS panel_users(
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'admin',
			enabled INTEGER NOT NULL DEFAULT 1,
			last_login_at TEXT,
			created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
			updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		);
	`); err != nil {
		return err
	}

	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_panel_users_username ON panel_users(username);`); err != nil {
		return err
	}




	return tx.Commit()
}
