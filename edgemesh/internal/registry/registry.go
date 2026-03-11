package registry

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Device struct {
	DeviceID  string            `json:"device_id"`
	Name      string            `json:"name"`
	Protocol  string            `json:"protocol"`
	Status    string            `json:"status"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	LastSeen  time.Time         `json:"last_seen"`
}

type Registry struct {
	db     *sql.DB
	dbPath string
}

func New(dbPath string) (*Registry, error) {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open registry db: %w", err)
	}
	// SQLite supports only one writer at a time. Limit connections to
	// serialize writes and prevent SQLITE_BUSY under concurrent goroutines.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping registry db: %w", err)
	}
	r := &Registry{db: db, dbPath: dbPath}
	if err := r.migrate(); err != nil {
		return nil, err
	}
	return r, nil
}

// DBPath returns the filesystem path to the SQLite database file.
func (r *Registry) DBPath() string { return r.dbPath }

// DeadLetterCount returns the number of messages in the dead letter queue.
func (r *Registry) DeadLetterCount() (int, error) {
	var count int
	err := r.db.QueryRow(`SELECT COUNT(*) FROM dead_letters`).Scan(&count)
	return count, err
}

// LatestMessageCount returns the number of stored latest messages.
func (r *Registry) LatestMessageCount() (int, error) {
	var count int
	err := r.db.QueryRow(`SELECT COUNT(*) FROM latest_messages`).Scan(&count)
	return count, err
}

// DeviceCountByStatus returns device counts grouped by status.
func (r *Registry) DeviceCountByStatus() (map[string]int, error) {
	rows, err := r.db.Query(`SELECT protocol, status, COUNT(*) FROM devices GROUP BY protocol, status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var proto, status string
		var cnt int
		if err := rows.Scan(&proto, &status, &cnt); err != nil {
			return nil, err
		}
		counts[proto+"/"+status] = cnt
	}
	return counts, rows.Err()
}

func (r *Registry) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS devices (
			device_id  TEXT PRIMARY KEY,
			name       TEXT NOT NULL,
			protocol   TEXT NOT NULL,
			status     TEXT NOT NULL DEFAULT 'active',
			metadata   TEXT NOT NULL DEFAULT '{}',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			last_seen  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS latest_messages (
			device_id  TEXT PRIMARY KEY,
			data       BLOB NOT NULL,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS dead_letters (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			subject    TEXT NOT NULL,
			data       BLOB NOT NULL,
			error      TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
	}
	for _, s := range stmts {
		if _, err := r.db.Exec(s); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

func (r *Registry) Register(d Device) error {
	meta, err := json.Marshal(d.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	_, err = r.db.Exec(
		`INSERT INTO devices (device_id, name, protocol, status, metadata, last_seen)
		 VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(device_id) DO UPDATE SET
		   name = excluded.name,
		   protocol = excluded.protocol,
		   status = excluded.status,
		   metadata = excluded.metadata,
		   last_seen = CURRENT_TIMESTAMP`,
		d.DeviceID, d.Name, d.Protocol, d.Status, string(meta),
	)
	if err != nil {
		return fmt.Errorf("register device %s: %w", d.DeviceID, err)
	}
	return nil
}

func (r *Registry) GetByID(deviceID string) (*Device, error) {
	row := r.db.QueryRow(
		`SELECT device_id, name, protocol, status, metadata, created_at, last_seen FROM devices WHERE device_id = ?`,
		deviceID,
	)
	return scanDevice(row)
}

func (r *Registry) GetByProtocol(protocol string) ([]Device, error) {
	rows, err := r.db.Query(
		`SELECT device_id, name, protocol, status, metadata, created_at, last_seen FROM devices WHERE protocol = ?`,
		protocol,
	)
	if err != nil {
		return nil, fmt.Errorf("query by protocol %s: %w", protocol, err)
	}
	defer rows.Close()

	var devices []Device
	for rows.Next() {
		d, err := scanDeviceRow(rows)
		if err != nil {
			return nil, err
		}
		devices = append(devices, *d)
	}
	return devices, rows.Err()
}

func (r *Registry) UpdateStatus(deviceID, status string) error {
	res, err := r.db.Exec(`UPDATE devices SET status = ? WHERE device_id = ?`, status, deviceID)
	if err != nil {
		return fmt.Errorf("update status for %s: %w", deviceID, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("device %s not found", deviceID)
	}
	return nil
}

// UpdateLastSeen bumps the last_seen timestamp for a device.
func (r *Registry) UpdateLastSeen(deviceID string) error {
	_, err := r.db.Exec(`UPDATE devices SET last_seen = CURRENT_TIMESTAMP WHERE device_id = ?`, deviceID)
	return err
}

// MarkInactiveDevices sets status='inactive' for devices that have not been
// seen within the given timeout duration.
func (r *Registry) MarkInactiveDevices(timeout time.Duration) (int64, error) {
	cutoff := time.Now().Add(-timeout).UTC().Format("2006-01-02 15:04:05")
	res, err := r.db.Exec(
		`UPDATE devices SET status = 'inactive' WHERE status = 'active' AND last_seen < ?`,
		cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("mark inactive devices: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// DeviceCount returns the total number of registered devices.
func (r *Registry) DeviceCount() (int, error) {
	var count int
	err := r.db.QueryRow(`SELECT COUNT(*) FROM devices`).Scan(&count)
	return count, err
}

// DeviceCountByProtocol returns device counts grouped by protocol.
func (r *Registry) DeviceCountByProtocol() (map[string]int, error) {
	rows, err := r.db.Query(`SELECT protocol, COUNT(*) FROM devices GROUP BY protocol`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var proto string
		var cnt int
		if err := rows.Scan(&proto, &cnt); err != nil {
			return nil, err
		}
		counts[proto] = cnt
	}
	return counts, rows.Err()
}

// ── Latest Messages (Fix 3) ────────────────────────────

// UpsertLatestMessage stores the latest serialized message for a device.
func (r *Registry) UpsertLatestMessage(deviceID string, data []byte) error {
	_, err := r.db.Exec(
		`INSERT INTO latest_messages (device_id, data, updated_at)
		 VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(device_id) DO UPDATE SET
		   data = excluded.data,
		   updated_at = CURRENT_TIMESTAMP`,
		deviceID, data,
	)
	return err
}

// GetLatestMessage retrieves the latest serialized message for a device.
func (r *Registry) GetLatestMessage(deviceID string) ([]byte, error) {
	var data []byte
	err := r.db.QueryRow(
		`SELECT data FROM latest_messages WHERE device_id = ?`,
		deviceID,
	).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return data, err
}

// ── Dead Letters (Fix 12) ──────────────────────────────

// InsertDeadLetter records a message that could not be published after retries.
func (r *Registry) InsertDeadLetter(subject string, data []byte, errMsg string) error {
	_, err := r.db.Exec(
		`INSERT INTO dead_letters (subject, data, error) VALUES (?, ?, ?)`,
		subject, data, errMsg,
	)
	return err
}

func (r *Registry) Close() error {
	return r.db.Close()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanDevice(s scanner) (*Device, error) {
	var d Device
	var meta string
	if err := s.Scan(&d.DeviceID, &d.Name, &d.Protocol, &d.Status, &meta, &d.CreatedAt, &d.LastSeen); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scan device: %w", err)
	}
	if err := json.Unmarshal([]byte(meta), &d.Metadata); err != nil {
		return nil, fmt.Errorf("unmarshal device metadata: %w", err)
	}
	return &d, nil
}

func scanDeviceRow(rows *sql.Rows) (*Device, error) {
	var d Device
	var meta string
	if err := rows.Scan(&d.DeviceID, &d.Name, &d.Protocol, &d.Status, &meta, &d.CreatedAt, &d.LastSeen); err != nil {
		return nil, fmt.Errorf("scan device row: %w", err)
	}
	if err := json.Unmarshal([]byte(meta), &d.Metadata); err != nil {
		return nil, fmt.Errorf("unmarshal device metadata: %w", err)
	}
	return &d, nil
}
