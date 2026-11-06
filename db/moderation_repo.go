package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var _ error = (*RejoinBlockedError)(nil)

type RejoinBlockedError struct {
	Until time.Time
}

func (err *RejoinBlockedError) Error() string {
	return "queue rejoin is temporarily blocked"
}

func (db *DBRepository) RemoveQueueEntry(ctx context.Context, scheduleID int, opensAt time.Time, entryID int, blockFor time.Duration) error {
	if blockFor < 0 || blockFor > 24*time.Hour {
		return ErrQueueStateChanged
	}
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockQueue(ctx, tx, scheduleID); err != nil {
		return err
	}
	var removed QueueBlock
	err = tx.QueryRow(ctx, `DELETE FROM queue_entries
 WHERE schedule_id = $1 AND id = $2 AND EXISTS (
   SELECT 1 FROM schedules WHERE id = $1 AND active_opens_at = $3
   AND active_opens_at <= clock_timestamp() AND active_closes_at > clock_timestamp()
 ) RETURNING user_id, COALESCE(username, ''), display_name`, scheduleID, entryID, opensAt).
		Scan(&removed.UserID, &removed.Username, &removed.DisplayName)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrQueueStateChanged
	}
	if err != nil {
		return err
	}
	if blockFor > 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO queue_rejoin_blocks
 (schedule_id, active_opens_at, user_id, username, display_name, blocked_until)
 VALUES ($1, $2, $3, $4, $5, clock_timestamp() + $6::double precision * interval '1 second')
 ON CONFLICT (schedule_id, active_opens_at, user_id) DO UPDATE SET
 id = EXCLUDED.id, username = EXCLUDED.username, display_name = EXCLUDED.display_name,
 blocked_until = EXCLUDED.blocked_until`, scheduleID, opensAt, removed.UserID, removed.Username, removed.DisplayName, blockFor.Seconds()); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func checkQueueRejoin(ctx context.Context, tx pgx.Tx, scheduleID int, userID int64) error {
	var until time.Time
	err := tx.QueryRow(ctx, `SELECT b.blocked_until FROM queue_rejoin_blocks b
 JOIN schedules s ON s.id = b.schedule_id AND s.active_opens_at = b.active_opens_at
 WHERE b.schedule_id = $1 AND b.user_id = $2 AND b.blocked_until > clock_timestamp()`, scheduleID, userID).Scan(&until)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return &RejoinBlockedError{Until: until}
}

func (db *DBRepository) ListQueueBlocks(ctx context.Context, scheduleID int, opensAt time.Time) ([]QueueBlock, error) {
	rows, err := db.pool.Query(ctx, `SELECT b.id, b.user_id, b.username, b.display_name, b.blocked_until
 FROM queue_rejoin_blocks b JOIN schedules s ON s.id = b.schedule_id AND s.active_opens_at = b.active_opens_at
 WHERE b.schedule_id = $1 AND b.active_opens_at = $2 AND b.blocked_until > clock_timestamp()
 AND s.active_opens_at <= clock_timestamp() AND s.active_closes_at > clock_timestamp()
 ORDER BY b.blocked_until, b.id`, scheduleID, opensAt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var blocks []QueueBlock
	for rows.Next() {
		var block QueueBlock
		if err := rows.Scan(&block.ID, &block.UserID, &block.Username, &block.DisplayName, &block.BlockedUntil); err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}
	return blocks, rows.Err()
}

func (db *DBRepository) UnblockQueueEntry(ctx context.Context, scheduleID int, opensAt time.Time, blockID int) error {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockQueue(ctx, tx, scheduleID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM queue_rejoin_blocks
 WHERE schedule_id = $1 AND active_opens_at = $2 AND id = $3 AND blocked_until > clock_timestamp()
 AND EXISTS (SELECT 1 FROM schedules WHERE id = $1 AND active_opens_at = $2
 AND active_opens_at <= clock_timestamp() AND active_closes_at > clock_timestamp())`, scheduleID, opensAt, blockID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrQueueStateChanged
	}
	return tx.Commit(ctx)
}
