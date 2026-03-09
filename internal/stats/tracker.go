package stats

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"strings"

	"emby-media-portal/internal/database"
)

var ErrDatabaseNotAvailable = errors.New("database not available")

const unknownUserKey = "__unknown__"
const unknownClientKey = "__unknown_client__"
const publicRequestKey = "__public__"

// TrafficRecord represents a single traffic record
type TrafficRecord struct {
	UserID     string
	ClientID   string
	ClientName string
	DeviceID   string
	DeviceName string
	ServerID   string
	RequestPath string
	TrafficKind string
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

type TrafficEntry struct {
	ID          int64  `json:"id"`
	Timestamp   string `json:"timestamp"`
	UserID      string `json:"user_id"`
	UserName    string `json:"user_name"`
	ClientID    string `json:"client_id"`
	ClientName  string `json:"client_name"`
	DeviceID    string `json:"device_id"`
	DeviceName  string `json:"device_name"`
	ServerID    string `json:"server_id"`
	RequestPath string `json:"request_path"`
	TrafficKind string `json:"traffic_kind"`
	BytesIn     int64  `json:"bytes_in"`
	BytesOut    int64  `json:"bytes_out"`
}

// Tracker tracks traffic statistics
type Tracker struct {
	pendingRecords []TrafficRecord
	activeTransfers map[string]TrafficRecord
	mu             sync.Mutex
	flushInterval  time.Duration
	stopCh         chan struct{}
	nextTransferID uint64
}

var defaultTracker *Tracker

// NewTracker creates a new stats tracker
func NewTracker(flushInterval time.Duration) *Tracker {
	t := &Tracker{
		pendingRecords: make([]TrafficRecord, 0),
		activeTransfers: make(map[string]TrafficRecord),
		flushInterval:  flushInterval,
		stopCh:         make(chan struct{}),
	}
	defaultTracker = t

	go t.runFlusher()
	return t
}

// Record records traffic for a request
func (t *Tracker) Record(userID, clientID, clientName, deviceID, deviceName, serverID, requestPath, trafficKind string, bytesIn, bytesOut int64) {
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
		RequestPath: requestPath,
		TrafficKind: trafficKind,
		BytesIn:    bytesIn,
		BytesOut:   bytesOut,
	})
	t.mu.Unlock()
}

func (t *Tracker) StartTransfer(userID, clientID, clientName, deviceID, deviceName, serverID, requestPath, trafficKind string, bytesIn int64) string {
	if t == nil {
		return ""
	}

	id := fmt.Sprintf("%d-%d", time.Now().UnixNano(), atomic.AddUint64(&t.nextTransferID, 1))
	t.mu.Lock()
	t.activeTransfers[id] = TrafficRecord{
		UserID:      userID,
		ClientID:    clientID,
		ClientName:  clientName,
		DeviceID:    deviceID,
		DeviceName:  deviceName,
		ServerID:    serverID,
		RequestPath: requestPath,
		TrafficKind: trafficKind,
		BytesIn:     bytesIn,
		BytesOut:    0,
	}
	t.mu.Unlock()
	return id
}

func (t *Tracker) AddTransferProgress(id string, bytesIn, bytesOut int64) {
	if t == nil || id == "" {
		return
	}
	t.mu.Lock()
	record, ok := t.activeTransfers[id]
	if ok {
		record.BytesIn += bytesIn
		record.BytesOut += bytesOut
		t.activeTransfers[id] = record
	}
	t.mu.Unlock()
}

func (t *Tracker) FinishTransfer(id string) {
	if t == nil || id == "" {
		return
	}
	t.mu.Lock()
	delete(t.activeTransfers, id)
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
		 (user_id, client_id, client_name, device_id, device_name, server_id, request_path, traffic_kind, bytes_in, bytes_out)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		tx.Rollback()
		return
	}
	defer stmt.Close()

	for _, r := range records {
		stmt.Exec(r.UserID, r.ClientID, r.ClientName, r.DeviceID, r.DeviceName, r.ServerID, r.RequestPath, r.TrafficKind, r.BytesIn, r.BytesOut)
	}

	tx.Commit()
}

func (t *Tracker) snapshotLiveRecords() []TrafficRecord {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	records := make([]TrafficRecord, 0, len(t.pendingRecords)+len(t.activeTransfers))
	records = append(records, t.pendingRecords...)
	for _, record := range t.activeTransfers {
		records = append(records, record)
	}
	return records
}

func addLiveTraffic(statsList []Stats, groupBy func(TrafficRecord) (string, func(*Stats))) []Stats {
	if defaultTracker == nil {
		return statsList
	}

	liveRecords := defaultTracker.snapshotLiveRecords()
	if len(liveRecords) == 0 {
		return statsList
	}

	indexByKey := make(map[string]int, len(statsList))
	for i, item := range statsList {
		key, _ := groupBy(TrafficRecord{
			UserID:      item.UserID,
			ClientID:    item.ClientID,
			ClientName:  item.ClientName,
			DeviceID:    item.DeviceID,
			DeviceName:  item.DeviceName,
			TrafficKind: "",
		})
		indexByKey[key] = i
	}

	for _, record := range liveRecords {
		key, applyMeta := groupBy(record)
		if key == "" {
			continue
		}
		idx, ok := indexByKey[key]
		if !ok {
			statsList = append(statsList, Stats{})
			idx = len(statsList) - 1
			indexByKey[key] = idx
		}
		applyMeta(&statsList[idx])
		statsList[idx].TotalBytesIn += record.BytesIn
		statsList[idx].TotalBytesOut += record.BytesOut
		statsList[idx].RequestCount++
	}

	return statsList
}

func normalizeUserKey(record TrafficRecord) string {
	if strings.TrimSpace(record.TrafficKind) == "public" {
		return publicRequestKey
	}
	if strings.TrimSpace(record.UserID) == "" {
		return unknownUserKey
	}
	return record.UserID
}

func userDisplayName(record TrafficRecord) string {
	switch normalizeUserKey(record) {
	case publicRequestKey:
		return "公共请求"
	case unknownUserKey:
		return "未知用户"
	default:
		return record.UserID
	}
}

func normalizeClientKey(record TrafficRecord) string {
	if strings.TrimSpace(record.ClientID) == "" && strings.TrimSpace(record.ClientName) == "" {
		return unknownClientKey
	}
	if strings.TrimSpace(record.ClientName) != "" {
		return "name:" + strings.ToLower(record.ClientName)
	}
	return record.ClientID
}

func clientDisplayName(record TrafficRecord) string {
	if normalizeClientKey(record) == unknownClientKey {
		return "未知客户端"
	}
	if strings.TrimSpace(record.ClientName) != "" {
		return record.ClientName
	}
	if strings.TrimSpace(record.DeviceName) != "" {
		return record.DeviceName
	}
	return record.ClientID
}

// GetClientStats gets aggregated stats for a client.
func GetClientStats(clientID string, since time.Time) (*Stats, error) {
	db := database.Get()
	if db == nil {
		return nil, ErrDatabaseNotAvailable
	}

	stats := &Stats{ClientID: clientID}
	query := `SELECT COALESCE(MAX(client_name), ''), COALESCE(MAX(device_id), ''), COALESCE(MAX(device_name), ''),
		        COALESCE(SUM(bytes_in), 0), COALESCE(SUM(bytes_out), 0), COUNT(*)
		 FROM traffic_stats
		 WHERE client_id = ? AND timestamp >= ?`
	args := []any{clientID, since}
	if clientID == unknownClientKey {
		query = `SELECT '未知客户端', '', '',
		        COALESCE(SUM(bytes_in), 0), COALESCE(SUM(bytes_out), 0), COUNT(*)
		 FROM traffic_stats
		 WHERE COALESCE(TRIM(client_id), '') = '' AND COALESCE(TRIM(client_name), '') = '' AND timestamp >= ?`
		args = []any{since}
	}

	err := db.QueryRow(query, args...).Scan(&stats.ClientName, &stats.DeviceID, &stats.DeviceName, &stats.TotalBytesIn, &stats.TotalBytesOut, &stats.RequestCount)
	if err != nil {
		return nil, err
	}

	for _, record := range defaultTracker.snapshotLiveRecords() {
		switch {
		case clientID == unknownClientKey && strings.TrimSpace(record.ClientID) == "" && strings.TrimSpace(record.ClientName) == "":
			stats.ClientName = "未知客户端"
			stats.TotalBytesIn += record.BytesIn
			stats.TotalBytesOut += record.BytesOut
			stats.RequestCount++
		case clientID != unknownClientKey && normalizeClientKey(record) == clientID:
			stats.ClientID = clientID
			stats.ClientName = clientDisplayName(record)
			stats.DeviceID = record.DeviceID
			stats.TotalBytesIn += record.BytesIn
			stats.TotalBytesOut += record.BytesOut
			stats.RequestCount++
		}
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
		`SELECT
		        CASE
		            WHEN COALESCE(TRIM(client_id), '') = '' AND COALESCE(TRIM(client_name), '') = '' THEN ?
		            WHEN COALESCE(client_name, '') <> '' THEN 'name:' || LOWER(client_name)
		            ELSE client_id
		        END AS client_key,
		        CASE
		            WHEN COALESCE(TRIM(client_id), '') = '' AND COALESCE(TRIM(client_name), '') = '' THEN '未知客户端'
		            ELSE COALESCE(MAX(NULLIF(client_name, '')), MAX(NULLIF(device_name, '')), client_id)
		        END AS client_name,
		        '' AS device_id,
		        CASE
		            WHEN COALESCE(TRIM(client_id), '') = '' AND COALESCE(TRIM(client_name), '') = '' THEN ''
		            WHEN COUNT(DISTINCT NULLIF(device_id, '')) > 1 THEN '多设备'
		            ELSE ''
		        END AS device_name,
		        COALESCE(SUM(bytes_in), 0), COALESCE(SUM(bytes_out), 0), COUNT(*)
		 FROM traffic_stats
		 WHERE timestamp >= ?
		 GROUP BY client_key
		 ORDER BY COALESCE(SUM(bytes_out), 0) DESC`,
		unknownClientKey, since,
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

	statsList = addLiveTraffic(statsList, func(record TrafficRecord) (string, func(*Stats)) {
		key := normalizeClientKey(record)
		return key, func(s *Stats) {
			s.ClientID = key
			s.ClientName = clientDisplayName(record)
			if s.DeviceID == "" {
				s.DeviceID = record.DeviceID
			}
			if s.DeviceName == "" {
				s.DeviceName = record.DeviceName
			}
		}
	})

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
	query := `SELECT COALESCE(MAX(u.name), ''), COALESCE(SUM(t.bytes_in), 0), COALESCE(SUM(t.bytes_out), 0), COUNT(*)
		 FROM traffic_stats t
		 LEFT JOIN users u ON u.id = t.user_id
		 WHERE t.user_id = ? AND t.timestamp >= ?`
	args := []any{userID, since}
	if userID == unknownUserKey {
		query = `SELECT '未知用户', COALESCE(SUM(t.bytes_in), 0), COALESCE(SUM(t.bytes_out), 0), COUNT(*)
		 FROM traffic_stats t
		 WHERE COALESCE(TRIM(t.user_id), '') = '' AND COALESCE(t.traffic_kind, '') <> 'public' AND t.timestamp >= ?`
		args = []any{since}
	} else if userID == publicRequestKey {
		query = `SELECT '公共请求', COALESCE(SUM(t.bytes_in), 0), COALESCE(SUM(t.bytes_out), 0), COUNT(*)
		 FROM traffic_stats t
		 WHERE (
		   COALESCE(t.traffic_kind, '') = 'public'
		   OR (
		     COALESCE(TRIM(t.user_id), '') = ''
		     AND COALESCE(TRIM(t.request_path), '') <> ''
		     AND (
		       t.request_path LIKE '%/System/Info/Public'
		       OR t.request_path LIKE '%/Users/AuthenticateByName'
		       OR t.request_path LIKE '%/Sessions%'
		       OR t.request_path LIKE '%/Images/%'
		       OR t.request_path LIKE '%/web/%'
		     )
		   )
		 ) AND t.timestamp >= ?`
		args = []any{since}
	}

	err := db.QueryRow(query, args...).Scan(&stats.UserName, &stats.TotalBytesIn, &stats.TotalBytesOut, &stats.RequestCount)

	if err != nil {
		return nil, err
	}

	for _, record := range defaultTracker.snapshotLiveRecords() {
		switch {
		case userID == unknownUserKey && normalizeUserKey(record) == unknownUserKey:
			stats.UserName = "未知用户"
			stats.TotalBytesIn += record.BytesIn
			stats.TotalBytesOut += record.BytesOut
			stats.RequestCount++
		case userID == publicRequestKey && normalizeUserKey(record) == publicRequestKey:
			stats.UserName = "公共请求"
			stats.TotalBytesIn += record.BytesIn
			stats.TotalBytesOut += record.BytesOut
			stats.RequestCount++
		case userID != unknownUserKey && userID != publicRequestKey && normalizeUserKey(record) == userID:
			stats.UserID = userID
			if stats.UserName == "" {
				stats.UserName = userID
			}
			stats.TotalBytesIn += record.BytesIn
			stats.TotalBytesOut += record.BytesOut
			stats.RequestCount++
		}
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
		`SELECT
		     CASE
		         WHEN (
		           COALESCE(t.traffic_kind, '') = 'public'
		           OR (
		             COALESCE(TRIM(t.user_id), '') = ''
		             AND COALESCE(TRIM(t.request_path), '') <> ''
		             AND (
		               t.request_path LIKE '%/System/Info/Public'
		               OR t.request_path LIKE '%/Users/AuthenticateByName'
		               OR t.request_path LIKE '%/Sessions%'
		               OR t.request_path LIKE '%/Images/%'
		               OR t.request_path LIKE '%/web/%'
		             )
		           )
		         ) THEN ?
		         WHEN COALESCE(TRIM(t.user_id), '') = '' THEN ?
		         ELSE t.user_id
		     END AS grouped_user_id,
		     CASE
		         WHEN (
		           COALESCE(t.traffic_kind, '') = 'public'
		           OR (
		             COALESCE(TRIM(t.user_id), '') = ''
		             AND COALESCE(TRIM(t.request_path), '') <> ''
		             AND (
		               t.request_path LIKE '%/System/Info/Public'
		               OR t.request_path LIKE '%/Users/AuthenticateByName'
		               OR t.request_path LIKE '%/Sessions%'
		               OR t.request_path LIKE '%/Images/%'
		               OR t.request_path LIKE '%/web/%'
		             )
		           )
		         ) THEN '公共请求'
		         WHEN COALESCE(TRIM(t.user_id), '') = '' THEN '未知用户'
		         ELSE COALESCE(MAX(NULLIF(u.name, '')), t.user_id)
		     END AS grouped_user_name,
		     COALESCE(SUM(t.bytes_in), 0),
		     COALESCE(SUM(t.bytes_out), 0),
		     COUNT(*)
		 FROM traffic_stats t
		 LEFT JOIN users u ON u.id = t.user_id
		 WHERE t.timestamp >= ?
		 GROUP BY grouped_user_id
		 ORDER BY COALESCE(SUM(t.bytes_out), 0) DESC`,
			publicRequestKey, unknownUserKey, since,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var statsList []Stats
	for rows.Next() {
		var s Stats
		if err := rows.Scan(&s.UserID, &s.UserName, &s.TotalBytesIn, &s.TotalBytesOut, &s.RequestCount); err != nil {
			return nil, err
		}
		statsList = append(statsList, s)
	}

	statsList = addLiveTraffic(statsList, func(record TrafficRecord) (string, func(*Stats)) {
		key := normalizeUserKey(record)
		return key, func(s *Stats) {
			s.UserID = key
			if strings.TrimSpace(s.UserName) == "" {
				s.UserName = userDisplayName(record)
			}
		}
	})

	return statsList, nil
}

// GetTrafficSummary gets overall traffic totals regardless of user or client grouping.
func GetTrafficSummary(since time.Time) (*Stats, error) {
	db := database.Get()
	if db == nil {
		return nil, ErrDatabaseNotAvailable
	}

	summary := &Stats{}
	err := db.QueryRow(
		`SELECT COALESCE(SUM(bytes_in), 0), COALESCE(SUM(bytes_out), 0), COUNT(*)
		 FROM traffic_stats
		 WHERE timestamp >= ?`,
		since,
	).Scan(&summary.TotalBytesIn, &summary.TotalBytesOut, &summary.RequestCount)
	if err != nil {
		return nil, err
	}

	for _, record := range defaultTracker.snapshotLiveRecords() {
		summary.TotalBytesIn += record.BytesIn
		summary.TotalBytesOut += record.BytesOut
		summary.RequestCount++
	}

	return summary, nil
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

func ListTrafficEntries(since time.Time, limit int) ([]TrafficEntry, error) {
	db := database.Get()
	if db == nil {
		return nil, ErrDatabaseNotAvailable
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}

	rows, err := db.Query(
		`SELECT
		     t.id,
		     t.timestamp,
		     t.user_id,
		     CASE
		         WHEN (
		           COALESCE(t.traffic_kind, '') = 'public'
		           OR (
		             COALESCE(TRIM(t.user_id), '') = ''
		             AND COALESCE(TRIM(t.request_path), '') <> ''
		             AND (
		               t.request_path LIKE '%/System/Info/Public'
		               OR t.request_path LIKE '%/Users/AuthenticateByName'
		               OR t.request_path LIKE '%/Sessions%'
		               OR t.request_path LIKE '%/Images/%'
		               OR t.request_path LIKE '%/web/%'
		             )
		           )
		         ) THEN '公共请求'
		         WHEN COALESCE(TRIM(t.user_id), '') = '' THEN '未知用户'
		         ELSE COALESCE(NULLIF(u.name, ''), t.user_id)
		     END AS display_user_name,
		     COALESCE(t.client_id, ''),
		     COALESCE(t.client_name, ''),
		     COALESCE(t.device_id, ''),
		     COALESCE(t.device_name, ''),
		     COALESCE(t.server_id, ''),
		     COALESCE(t.request_path, ''),
		     COALESCE(t.traffic_kind, ''),
		     COALESCE(t.bytes_in, 0),
		     COALESCE(t.bytes_out, 0)
		 FROM traffic_stats t
		 LEFT JOIN users u ON u.id = t.user_id
		 WHERE t.timestamp >= ?
		 ORDER BY t.timestamp DESC, t.id DESC
		 LIMIT ?`,
		since, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []TrafficEntry
	for rows.Next() {
		var entry TrafficEntry
		if err := rows.Scan(
			&entry.ID,
			&entry.Timestamp,
			&entry.UserID,
			&entry.UserName,
			&entry.ClientID,
			&entry.ClientName,
			&entry.DeviceID,
			&entry.DeviceName,
			&entry.ServerID,
			&entry.RequestPath,
			&entry.TrafficKind,
			&entry.BytesIn,
			&entry.BytesOut,
		); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}

	return entries, nil
}

func DeleteTrafficEntry(id int64) error {
	db := database.Get()
	if db == nil {
		return ErrDatabaseNotAvailable
	}
	_, err := db.Exec("DELETE FROM traffic_stats WHERE id = ?", id)
	return err
}

func ResetTrafficStats() error {
	db := database.Get()
	if db == nil {
		return ErrDatabaseNotAvailable
	}
	_, err := db.Exec("DELETE FROM traffic_stats")
	return err
}
