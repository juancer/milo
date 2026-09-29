package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// WifiNetwork is a Wi-Fi network the device may join.
type WifiNetwork struct {
	ID            int64
	SSID          string
	SecretName    string // name of the encrypted password in `secrets`; "" for open networks
	Priority      int
	Enabled       bool
	LastConnected *time.Time
}

// ListWifiNetworks returns networks by priority (highest first).
func (s *Store) ListWifiNetworks(ctx context.Context) ([]WifiNetwork, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, ssid, secret_name, priority, enabled, last_connected
		FROM wifi_networks ORDER BY priority DESC, ssid`)
	if err != nil {
		return nil, fmt.Errorf("store: list wifi: %w", err)
	}
	defer rows.Close()
	var out []WifiNetwork
	for rows.Next() {
		var n WifiNetwork
		var last sql.NullString
		if err := rows.Scan(&n.ID, &n.SSID, &n.SecretName, &n.Priority, &n.Enabled, &last); err != nil {
			return nil, err
		}
		if last.Valid {
			t := parseTime(last.String)
			n.LastConnected = &t
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// GetWifiNetwork returns one network or ErrNotFound.
func (s *Store) GetWifiNetwork(ctx context.Context, id int64) (WifiNetwork, error) {
	all, err := s.ListWifiNetworks(ctx)
	if err != nil {
		return WifiNetwork{}, err
	}
	for _, n := range all {
		if n.ID == id {
			return n, nil
		}
	}
	return WifiNetwork{}, ErrNotFound
}

// CreateWifiNetwork adds a network.
func (s *Store) CreateWifiNetwork(ctx context.Context, n WifiNetwork) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO wifi_networks (ssid, secret_name, priority, enabled) VALUES (?, ?, ?, ?)`,
		n.SSID, n.SecretName, n.Priority, n.Enabled)
	if err != nil {
		return 0, fmt.Errorf("store: create wifi: %w", err)
	}
	return res.LastInsertId()
}

// UpdateWifiNetwork changes a network's settings.
func (s *Store) UpdateWifiNetwork(ctx context.Context, n WifiNetwork) error {
	res, err := s.db.ExecContext(ctx, `UPDATE wifi_networks SET ssid = ?, secret_name = ?, priority = ?, enabled = ? WHERE id = ?`,
		n.SSID, n.SecretName, n.Priority, n.Enabled, n.ID)
	if err != nil {
		return fmt.Errorf("store: update wifi: %w", err)
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteWifiNetwork removes a network.
func (s *Store) DeleteWifiNetwork(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM wifi_networks WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkWifiConnected records that the device joined the network with this SSID.
func (s *Store) MarkWifiConnected(ctx context.Context, ssid string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE wifi_networks SET last_connected = ? WHERE ssid = ?`, ts(now), ssid)
	return err
}
