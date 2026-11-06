package db

import (
	"context"
	"time"
)

func (db *DBRepository) RemoveQueueEntry(ctx context.Context, scheduleID int, opensAt time.Time, entryID int) error {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := lockQueue(ctx, tx, scheduleID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM queue_entries
 WHERE schedule_id = $1 AND id = $2 AND EXISTS (
   SELECT 1 FROM schedules WHERE id = $1 AND active_opens_at = $3
   AND active_opens_at <= clock_timestamp() AND active_closes_at > clock_timestamp()
 )`, scheduleID, entryID, opensAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrQueueStateChanged
	}
	return tx.Commit(ctx)
}
