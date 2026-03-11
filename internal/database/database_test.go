package database

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestCleanupMalformedTrafficStats(t *testing.T) {
	conn, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "stats.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Exec(`CREATE TABLE traffic_stats (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id TEXT,
		client_name TEXT,
		device_id TEXT
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	if _, err := conn.Exec(
		`INSERT INTO traffic_stats (user_id, client_name, device_id) VALUES (?, ?, ?)`,
		`498f6764eaa94ec5b1688d364e473c88"\`,
		`VidHub"\`,
		`85057A58-09E5-4ED2-A428-7BFDD2873F6E"\`,
	); err != nil {
		t.Fatalf("insert row: %v", err)
	}

	if err := cleanupMalformedTrafficStats(conn); err != nil {
		t.Fatalf("cleanupMalformedTrafficStats: %v", err)
	}

	var userID, clientName, deviceID string
	if err := conn.QueryRow(`SELECT user_id, client_name, device_id FROM traffic_stats WHERE id = 1`).Scan(&userID, &clientName, &deviceID); err != nil {
		t.Fatalf("query row: %v", err)
	}

	if userID != "498f6764eaa94ec5b1688d364e473c88" {
		t.Fatalf("user_id = %q", userID)
	}
	if clientName != "VidHub" {
		t.Fatalf("client_name = %q", clientName)
	}
	if deviceID != "85057A58-09E5-4ED2-A428-7BFDD2873F6E" {
		t.Fatalf("device_id = %q", deviceID)
	}
}
