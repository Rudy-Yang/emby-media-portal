package stats

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"emby-media-portal/internal/database"
)

var (
	testDBOnce sync.Once
	testDB     *sql.DB
	testDBErr  error
)

func initStatsTestDB(t *testing.T) *sql.DB {
	t.Helper()

	testDBOnce.Do(func() {
		dir, err := os.MkdirTemp("", "emby-media-portal-stats-test-")
		if err != nil {
			testDBErr = err
			return
		}
		testDB, testDBErr = database.Init(filepath.Join(dir, "stats.sqlite"))
	})

	if testDBErr != nil {
		t.Fatalf("init test db: %v", testDBErr)
	}

	for _, query := range []string{
		"DELETE FROM traffic_stats",
		"DELETE FROM users",
	} {
		if _, err := testDB.Exec(query); err != nil {
			t.Fatalf("reset test db with %q: %v", query, err)
		}
	}

	return testDB
}

func seedTrafficRecord(t *testing.T, db *sql.DB, userID, clientName, requestPath, trafficKind string) {
	t.Helper()

	if _, err := db.Exec(
		`INSERT INTO traffic_stats (
			user_id, client_name, request_path, traffic_kind, bytes_in, bytes_out, timestamp
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		userID,
		clientName,
		requestPath,
		trafficKind,
		128,
		256,
		time.Now().UTC(),
	); err != nil {
		t.Fatalf("seed traffic record: %v", err)
	}
}

func TestListTrafficEntriesSearchesUnknownUsers(t *testing.T) {
	db := initStatsTestDB(t)

	if _, err := db.Exec(`INSERT INTO users (id, name) VALUES (?, ?)`, "user-1", "Alice"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	seedTrafficRecord(t, db, "", "Mystery Client", "/Items/unknown-playback", "unknown")
	seedTrafficRecord(t, db, "user-1", "Known Client", "/Items/known-playback", "user")
	seedTrafficRecord(t, db, "", "Public Client", "/System/Info/Public", "public")

	page, err := ListTrafficEntries(time.Now().Add(-time.Hour), 1, 20, TrafficEntryFilters{
		Search: "未知用户",
	})
	if err != nil {
		t.Fatalf("ListTrafficEntries() error = %v", err)
	}

	if page.Total != 1 {
		t.Fatalf("ListTrafficEntries() total = %d, want 1", page.Total)
	}
	if len(page.Items) != 1 {
		t.Fatalf("ListTrafficEntries() items = %d, want 1", len(page.Items))
	}
	if page.Items[0].UserName != "未知用户" {
		t.Fatalf("ListTrafficEntries() user name = %q, want 未知用户", page.Items[0].UserName)
	}
	if page.Items[0].RequestPath != "/Items/unknown-playback" {
		t.Fatalf("ListTrafficEntries() request path = %q, want /Items/unknown-playback", page.Items[0].RequestPath)
	}
}

func TestDeleteTrafficEntriesByFilterDeletesOnlyMatchingUnknownUsers(t *testing.T) {
	db := initStatsTestDB(t)

	if _, err := db.Exec(`INSERT INTO users (id, name) VALUES (?, ?)`, "user-2", "Bob"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	seedTrafficRecord(t, db, "", "Mystery Client", "/Videos/1/stream", "unknown")
	seedTrafficRecord(t, db, "user-2", "Known Client", "/Videos/2/stream", "user")

	deleted, err := DeleteTrafficEntriesByFilter(time.Now().Add(-time.Hour), TrafficEntryFilters{
		Search: "未知用户",
	})
	if err != nil {
		t.Fatalf("DeleteTrafficEntriesByFilter() error = %v", err)
	}
	if deleted != 1 {
		t.Fatalf("DeleteTrafficEntriesByFilter() deleted = %d, want 1", deleted)
	}

	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM traffic_stats`).Scan(&total); err != nil {
		t.Fatalf("count traffic stats: %v", err)
	}
	if total != 1 {
		t.Fatalf("remaining traffic records = %d, want 1", total)
	}

	var remainingPath string
	if err := db.QueryRow(`SELECT request_path FROM traffic_stats LIMIT 1`).Scan(&remainingPath); err != nil {
		t.Fatalf("load remaining request path: %v", err)
	}
	if remainingPath != "/Videos/2/stream" {
		t.Fatalf("remaining request path = %q, want /Videos/2/stream", remainingPath)
	}
}
