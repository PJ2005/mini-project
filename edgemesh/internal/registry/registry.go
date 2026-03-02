package registry

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Device struct {
	DeviceID  string            `json:"device_id"`
	Name      string            `json:"name"`
	Protocol  string            `json:"protocol"`
	Status    string            `json:"status"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

type Registry struct {
	db *sql.DB
}

func New(dbPath string) (*Registry, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open registry db: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping registry db: %w", err)
	}
	r := &Registry{db: db}
	if err := r.migrate(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Registry) migrate() error {
	_, err := r.db.Exec(`
		CREATE TABLE IF NOT EXISTS devices (
			device_id  TEXT PRIMARY KEY,
			name       TEXT NOT NULL,
			protocol   TEXT NOT NULL,
			status     TEXT NOT NULL DEFAULT 'active',
			metadata   TEXT NOT NULL DEFAULT '{}',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("migrate devices table: %w", err)
	}
	return nil
}

func (r *Registry) Register(d Device) error {
	meta, err := json.Marshal(d.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	_, err = r.db.Exec(
		`INSERT INTO devices (device_id, name, protocol, status, metadata)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(device_id) DO UPDATE SET
		   name = excluded.name,
		   protocol = excluded.protocol,
		   status = excluded.status,
		   metadata = excluded.metadata`,
		d.DeviceID, d.Name, d.Protocol, d.Status, string(meta),
	)
	if err != nil {
		return fmt.Errorf("register device %s: %w", d.DeviceID, err)
	}
	return nil
}

func (r *Registry) GetByID(deviceID string) (*Device, error) {
	row := r.db.QueryRow(
		`SELECT device_id, name, protocol, status, metadata, created_at FROM devices WHERE device_id = ?`,
		deviceID,
	)
	return scanDevice(row)
}

func (r *Registry) GetByProtocol(protocol string) ([]Device, error) {
	rows, err := r.db.Query(
		`SELECT device_id, name, protocol, status, metadata, created_at FROM devices WHERE protocol = ?`,
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

func (r *Registry) Close() error {
	return r.db.Close()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanDevice(s scanner) (*Device, error) {
	var d Device
	var meta string
	if err := s.Scan(&d.DeviceID, &d.Name, &d.Protocol, &d.Status, &meta, &d.CreatedAt); err != nil {
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
	if err := rows.Scan(&d.DeviceID, &d.Name, &d.Protocol, &d.Status, &meta, &d.CreatedAt); err != nil {
		return nil, fmt.Errorf("scan device row: %w", err)
	}
	if err := json.Unmarshal([]byte(meta), &d.Metadata); err != nil {
		return nil, fmt.Errorf("unmarshal device metadata: %w", err)
	}
	return &d, nil
}
