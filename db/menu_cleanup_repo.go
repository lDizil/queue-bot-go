package db

import (
	"context"
	"fmt"
	"time"
)

type MenuCleanup struct {
	Generation  string
	OwnerID     int64
	ChatID      int64
	ThreadID    int
	MessageID   int
	EphemeralID int
	Deadline    time.Time
}

func (r *DBRepository) SaveMenuCleanup(ctx context.Context, menu MenuCleanup) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO menu_cleanup (generation, owner_id, chat_id, thread_id, message_id, ephemeral_id, deadline)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (owner_id, chat_id, message_id, ephemeral_id)
		DO UPDATE SET generation = EXCLUDED.generation, thread_id = EXCLUDED.thread_id, deadline = EXCLUDED.deadline`,
		menu.Generation, menu.OwnerID, menu.ChatID, menu.ThreadID, menu.MessageID, menu.EphemeralID, menu.Deadline)
	if err != nil {
		return fmt.Errorf("save menu cleanup: %w", err)
	}
	return nil
}

func (r *DBRepository) ListMenuCleanup(ctx context.Context) ([]MenuCleanup, error) {
	rows, err := r.pool.Query(ctx, `SELECT generation, owner_id, chat_id, thread_id, message_id, ephemeral_id, deadline FROM menu_cleanup ORDER BY deadline, generation`)
	if err != nil {
		return nil, fmt.Errorf("list menu cleanup: %w", err)
	}
	defer rows.Close()
	var menus []MenuCleanup
	for rows.Next() {
		var menu MenuCleanup
		if err := rows.Scan(&menu.Generation, &menu.OwnerID, &menu.ChatID, &menu.ThreadID, &menu.MessageID, &menu.EphemeralID, &menu.Deadline); err != nil {
			return nil, fmt.Errorf("scan menu cleanup: %w", err)
		}
		menus = append(menus, menu)
	}
	return menus, rows.Err()
}

func (r *DBRepository) DeleteMenuCleanup(ctx context.Context, generation string) error {
	if _, err := r.pool.Exec(ctx, `DELETE FROM menu_cleanup WHERE generation = $1`, generation); err != nil {
		return fmt.Errorf("delete menu cleanup: %w", err)
	}
	return nil
}

func (r *DBRepository) TouchMenuCleanup(ctx context.Context, generation string, deadline time.Time) error {
	if _, err := r.pool.Exec(ctx, `UPDATE menu_cleanup SET deadline = $2 WHERE generation = $1`, generation, deadline); err != nil {
		return fmt.Errorf("update menu cleanup deadline: %w", err)
	}
	return nil
}
