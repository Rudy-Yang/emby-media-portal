package database

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

var (
	db   *sql.DB
	once sync.Once
)

func Init(dbPath string) (*sql.DB, error) {
	var initErr error
	once.Do(func() {
		// Ensure directory exists
		dir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			initErr = err
			return
		}

		var err error
		db, err = sql.Open("sqlite", dbPath)
		if err != nil {
			initErr = err
			return
		}

		// SQLite is sensitive to concurrent writers through database/sql's pool.
		// Keep a single shared connection and let busy_timeout absorb short lock bursts.
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)

		if initErr = configureSQLite(db); initErr != nil {
			_ = db.Close()
			db = nil
			return
		}

		// Create tables
		initErr = createTables()
	})

	return db, initErr
}

func Get() *sql.DB {
	return db
}

func createTables() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			name TEXT,
			upload_limit INTEGER DEFAULT 0,
			download_limit INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS servers (
			id TEXT PRIMARY KEY,
			name TEXT,
			url TEXT,
			total_limit INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS client_rules (
			id TEXT PRIMARY KEY,
			name TEXT,
			match_type TEXT NOT NULL,
			match_value TEXT NOT NULL,
			upload_limit INTEGER DEFAULT 0,
			download_limit INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS traffic_stats (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id TEXT,
			client_id TEXT,
			client_name TEXT,
			device_id TEXT,
			device_name TEXT,
			user_agent TEXT,
			client_ip TEXT,
			server_id TEXT,
			request_path TEXT,
			traffic_kind TEXT DEFAULT '',
			bytes_in INTEGER DEFAULT 0,
			bytes_out INTEGER DEFAULT 0,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS ip_geo_cache (
			ip TEXT PRIMARY KEY,
			country_code TEXT,
			country_name TEXT,
			region_name TEXT,
			city_name TEXT,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
	}

	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}

	migrations := []string{
		`ALTER TABLE traffic_stats ADD COLUMN client_id TEXT`,
		`ALTER TABLE traffic_stats ADD COLUMN client_name TEXT`,
		`ALTER TABLE traffic_stats ADD COLUMN device_id TEXT`,
		`ALTER TABLE traffic_stats ADD COLUMN device_name TEXT`,
		`ALTER TABLE traffic_stats ADD COLUMN user_agent TEXT`,
		`ALTER TABLE traffic_stats ADD COLUMN client_ip TEXT`,
		`ALTER TABLE traffic_stats ADD COLUMN request_path TEXT`,
		`ALTER TABLE traffic_stats ADD COLUMN traffic_kind TEXT DEFAULT ''`,
	}

	for _, q := range migrations {
		if _, err := db.Exec(q); err != nil && !isDuplicateColumnError(err) {
			return err
		}
	}

	indexQueries := []string{
		`CREATE INDEX IF NOT EXISTS idx_client_rules_match ON client_rules(match_type, match_value)`,
		`CREATE INDEX IF NOT EXISTS idx_traffic_stats_user ON traffic_stats(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_traffic_stats_client ON traffic_stats(client_id)`,
		`CREATE INDEX IF NOT EXISTS idx_traffic_stats_client_ip ON traffic_stats(client_ip)`,
		`CREATE INDEX IF NOT EXISTS idx_traffic_stats_timestamp ON traffic_stats(timestamp)`,
	}

	for _, q := range indexQueries {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}

	return nil
}

func configureSQLite(db *sql.DB) error {
	pragmas := []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = NORMAL`,
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA foreign_keys = ON`,
	}

	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			return err
		}
	}

	return nil
}

func isDuplicateColumnError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "duplicate column name")
}
