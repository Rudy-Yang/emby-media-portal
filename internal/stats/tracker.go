package stats

import (
	"errors"
	"sync"
	"time"

	"emby-media-portal/internal/database"
)

var ErrDatabaseNotAvailable = errors.New("database not available")

// TrafficRecord represents a single traffic record
type TrafficRecord struct {
	UserID     string
	ClientID   string
	ClientName string
	DeviceID   string
	DeviceName string
	ServerID   string
	BytesIn    int64
	BytesOut   int64
}

// Stats represents aggregated statistics
type Stats struct {
	UserID        string `json:"user_id,omitempty"`
	UserName      string `json:"user_name,omitempty"`
	ClientID      string `json:"client_id,omitempty"`
	ClientName    string `json:"client_name,omitempty"`
	DeviceID      string `json:"device_id,omitempty"`
	DeviceName    string `json:"device_name,omitempty"`
	ServerID      string `json:"server_id,omitempty"`
	TotalBytesIn  int64  `json:"total_bytes_in"`
	TotalBytesOut int64  `json:"total_bytes_out"`
	RequestCount  int64  `json:"request_count"`
}

// Tracker tracks traffic statistics
type Tracker struct {
	pendingRecords []TrafficRecord
	mu             sync.Mutex
	flushInterval  time.Duration
	stopCh         chan struct{}
}

// NewTracker creates a new stats tracker
func NewTracker(flushInterval time.Duration) *Tracker {
	t := &Tracker{
		pendingRecords: make([]TrafficRecord, 0),
		flushInterval:  flushInterval,
		stopCh:         make(chan struct{}),
	}

	go t.runFlusher()
	return t
}

// Record records traffic for a request
func (t *Tracker) Record(userID, clientID, clientName, deviceID, deviceName, serverID string, bytesIn, bytesOut int64) {
	if bytesIn == 0 && bytesOut == 0 {
		return
	}

	t.mu.Lock()
	t.pendingRecords = append(t.pendingRecords, TrafficRecord{
		UserID:     userID,
		ClientID:   clientID,
		ClientName: clientName,
		DeviceID:   deviceID,
		DeviceName: deviceName,
		ServerID:   serverID,
		BytesIn:    bytesIn,
		BytesOut:   bytesOut,
	})
	t.mu.Unlock()
}

func (t *Tracker) runFlusher() {
	ticker := time.NewTicker(t.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			t.flush()
		case <-t.stopCh:
			t.flush()
			return
		}
	}
}

func (t *Tracker) flush() {
	t.mu.Lock()
	records := t.pendingRecords
	t.pendingRecords = make([]TrafficRecord, 0)
	t.mu.Unlock()

	if len(records) == 0 {
		return
	}

	db := database.Get()
	if db == nil {
		return
	}

	tx, err := db.Begin()
	if err != nil {
		return
	}

	stmt, err := tx.Prepare(
		`INSERT INTO traffic_stats
		 (user_id, client_id, client_name, device_id, device_name, server_id, bytes_in, bytes_out)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		tx.Rollback()
		return
	}
	defer stmt.Close()

	for _, r := range records {
		stmt.Exec(r.UserID, r.ClientID, r.ClientName, r.DeviceID, r.DeviceName, r.ServerID, r.BytesIn, r.BytesOut)
	}

	tx.Commit()
}

// GetClientStats gets aggregated stats for a client.
func GetClientStats(clientID string, since time.Time) (*Stats, error) {
	db := database.Get()
	if db == nil {
		return nil, ErrDatabaseNotAvailable
	}

	stats := &Stats{ClientID: clientID}
	err := db.QueryRow(
		`SELECT COALESCE(MAX(client_name), ''), COALESCE(MAX(device_id), ''), COALESCE(MAX(device_name), ''),
		        COALESCE(SUM(bytes_in), 0), COALESCE(SUM(bytes_out), 0), COUNT(*)
		 FROM traffic_stats
		 WHERE client_id = ? AND timestamp >= ?`,
		clientID, since,
	).Scan(&stats.ClientName, &stats.DeviceID, &stats.DeviceName, &stats.TotalBytesIn, &stats.TotalBytesOut, &stats.RequestCount)
	if err != nil {
		return nil, err
	}

	return stats, nil
}

// GetAllClientStats gets aggregated stats for all clients.
func GetAllClientStats(since time.Time) ([]Stats, error) {
	db := database.Get()
	if db == nil {
		return nil, ErrDatabaseNotAvailable
	}

	rows, err := db.Query(
		`SELECT client_id, COALESCE(MAX(client_name), ''), COALESCE(MAX(device_id), ''), COALESCE(MAX(device_name), ''),
		        COALESCE(SUM(bytes_in), 0), COALESCE(SUM(bytes_out), 0), COUNT(*)
		 FROM traffic_stats
		 WHERE timestamp >= ? AND client_id <> ''
		 GROUP BY client_id
		 ORDER BY COALESCE(SUM(bytes_out), 0) DESC`,
		since,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var statsList []Stats
	for rows.Next() {
		var s Stats
		if err := rows.Scan(&s.ClientID, &s.ClientName, &s.DeviceID, &s.DeviceName, &s.TotalBytesIn, &s.TotalBytesOut, &s.RequestCount); err != nil {
			return nil, err
		}
		statsList = append(statsList, s)
	}

	return statsList, nil
}

// Stop stops the tracker
func (t *Tracker) Stop() {
	close(t.stopCh)
}

// GetUserStats gets aggregated stats for a user
func GetUserStats(userID string, since time.Time) (*Stats, error) {
	db := database.Get()
	if db == nil {
		return nil, ErrDatabaseNotAvailable
	}

	stats := &Stats{UserID: userID}
	err := db.QueryRow(
		`SELECT COALESCE(SUM(bytes_in), 0), COALESCE(SUM(bytes_out), 0), COUNT(*)
		 FROM traffic_stats
		 WHERE user_id = ? AND timestamp >= ?`,
		userID, since,
	).Scan(&stats.TotalBytesIn, &stats.TotalBytesOut, &stats.RequestCount)

	if err != nil {
		return nil, err
	}

	return stats, nil
}

// GetAllUserStats gets aggregated stats for all users
func GetAllUserStats(since time.Time) ([]Stats, error) {
	db := database.Get()
	if db == nil {
		return nil, ErrDatabaseNotAvailable
	}

	rows, err := db.Query(
		`SELECT user_id, COALESCE(SUM(bytes_in), 0), COALESCE(SUM(bytes_out), 0), COUNT(*)
		 FROM traffic_stats
		 WHERE timestamp >= ?
		 GROUP BY user_id`,
		since,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var statsList []Stats
	for rows.Next() {
		var s Stats
		if err := rows.Scan(&s.UserID, &s.TotalBytesIn, &s.TotalBytesOut, &s.RequestCount); err != nil {
			return nil, err
		}
		statsList = append(statsList, s)
	}

	return statsList, nil
}

// GetServerStats gets aggregated stats for a server
func GetServerStats(serverID string, since time.Time) (*Stats, error) {
	db := database.Get()
	if db == nil {
		return nil, ErrDatabaseNotAvailable
	}

	stats := &Stats{ServerID: serverID}
	err := db.QueryRow(
		`SELECT COALESCE(SUM(bytes_in), 0), COALESCE(SUM(bytes_out), 0), COUNT(*)
		 FROM traffic_stats
		 WHERE server_id = ? AND timestamp >= ?`,
		serverID, since,
	).Scan(&stats.TotalBytesIn, &stats.TotalBytesOut, &stats.RequestCount)

	if err != nil {
		return nil, err
	}

	return stats, nil
}

// CleanOldStats removes statistics older than the specified duration
func CleanOldStats(olderThan time.Duration) error {
	db := database.Get()
	if db == nil {
		return ErrDatabaseNotAvailable
	}

	cutoff := time.Now().Add(-olderThan)
	_, err := db.Exec("DELETE FROM traffic_stats WHERE timestamp < ?", cutoff)
	return err
}
