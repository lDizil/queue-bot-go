package db

import (
	"context"
	"encoding/json"
	"errors"

	"queuebot/settings"

	"github.com/jackc/pgx/v5"
)

var _ settings.Store = (*DBRepository)(nil)

func (db *DBRepository) LoadSettings(ctx context.Context) (settings.Values, bool, error) {
	var data []byte
	if err := db.pool.QueryRow(ctx, "SELECT value FROM runtime_settings WHERE id = true").Scan(&data); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return settings.Values{}, false, nil
		}
		return settings.Values{}, false, err
	}
	var values settings.Values
	if err := json.Unmarshal(data, &values); err != nil {
		return values, false, err
	}
	return values, true, nil
}

func (db *DBRepository) SaveSettings(ctx context.Context, values settings.Values) error {
	data, err := json.Marshal(values)
	if err != nil {
		return err
	}
	_, err = db.pool.Exec(ctx, "INSERT INTO runtime_settings (id, value) VALUES (true, $1) ON CONFLICT (id) DO UPDATE SET value = EXCLUDED.value", data)
	return err
}
