package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// GetSetting returns the JSON value stored under key, or ErrNotFound.
func (s *Store) GetSetting(ctx context.Context, key string) (string, time.Time, error) {
	var v string
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT value, updated_at FROM settings WHERE key = ?`, key).Scan(&v, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, ErrNotFound
	}
	if err != nil {
		return "", time.Time{}, err
	}
	return v, time.UnixMilli(at), nil
}

// SetSetting stores a JSON value under key.
func (s *Store) SetSetting(ctx context.Context, key, value string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, value, at.UnixMilli())
	return err
}

func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
	return err
}

// EmergencyKey is the record of a second restic key created through the API.
// Only the key ID and a label are kept, never the password.
type EmergencyKey struct {
	KeyID     string
	Label     string
	CreatedAt time.Time
}

func (s *Store) AddEmergencyKey(ctx context.Context, keyID, label string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO emergency_keys (key_id, label, created_at) VALUES (?, ?, ?)`,
		keyID, label, at.UnixMilli())
	return err
}

func (s *Store) EmergencyKeys(ctx context.Context) (map[string]EmergencyKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key_id, label, created_at FROM emergency_keys`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]EmergencyKey{}
	for rows.Next() {
		var k EmergencyKey
		var at int64
		if err := rows.Scan(&k.KeyID, &k.Label, &at); err != nil {
			return nil, err
		}
		k.CreatedAt = time.UnixMilli(at)
		out[k.KeyID] = k
	}
	return out, rows.Err()
}

func (s *Store) DeleteEmergencyKey(ctx context.Context, keyID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM emergency_keys WHERE key_id = ?`, keyID)
	return err
}
