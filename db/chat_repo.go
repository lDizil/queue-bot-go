package db

import (
	"context"
	"errors"

	"queuebot/chats"

	"github.com/jackc/pgx/v5"
)

var _ chats.Store = (*DBRepository)(nil)

const knownChats = `WITH known_ids AS (
 SELECT chat_id FROM chats UNION SELECT chat_id FROM schedules
)
SELECT ids.chat_id, COALESCE(meta.name, ''), COALESCE(meta.is_default, false)
FROM known_ids ids LEFT JOIN chats meta USING (chat_id)`

func (db *DBRepository) ListChats(ctx context.Context) ([]chats.Chat, error) {
	rows, err := db.pool.Query(ctx, knownChats+" ORDER BY COALESCE(meta.is_default, false) DESC, COALESCE(meta.name, ''), ids.chat_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var known []chats.Chat
	for rows.Next() {
		var chat chats.Chat
		if err := rows.Scan(&chat.ID, &chat.Name, &chat.IsDefault); err != nil {
			return nil, err
		}
		known = append(known, chat)
	}
	return known, rows.Err()
}

func (db *DBRepository) GetChat(ctx context.Context, id int64) (chats.Chat, error) {
	var chat chats.Chat
	err := db.pool.QueryRow(ctx, knownChats+" WHERE ids.chat_id = $1", id).Scan(&chat.ID, &chat.Name, &chat.IsDefault)
	if errors.Is(err, pgx.ErrNoRows) {
		return chats.Chat{}, chats.ErrNotFound
	}
	return chat, err
}

func (db *DBRepository) EnsureChat(ctx context.Context, id int64, name string) error {
	if id == 0 {
		return chats.ErrInvalidID
	}
	_, err := db.pool.Exec(ctx, `INSERT INTO chats (chat_id, name) VALUES ($1, $2)
 ON CONFLICT (chat_id) DO UPDATE SET name = CASE WHEN chats.name = '' THEN EXCLUDED.name ELSE chats.name END`, id, name)
	return err
}

func (db *DBRepository) SetChatName(ctx context.Context, id int64, name string) error {
	tag, err := db.pool.Exec(ctx, "UPDATE chats SET name = $2 WHERE chat_id = $1", id, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return chats.ErrNotFound
	}
	return nil
}

func (db *DBRepository) SetDefaultChat(ctx context.Context, id int64, onlyIfUnset bool) error {
	tx, err := db.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('queuebot:default-chat', 0))"); err != nil {
		return err
	}
	if onlyIfUnset {
		var configured bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM chats WHERE is_default)").Scan(&configured); err != nil {
			return err
		}
		if configured {
			return tx.Commit(ctx)
		}
	}
	if _, err := tx.Exec(ctx, "UPDATE chats SET is_default = false WHERE is_default"); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, "UPDATE chats SET is_default = true WHERE chat_id = $1", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return chats.ErrNotFound
	}
	return tx.Commit(ctx)
}
