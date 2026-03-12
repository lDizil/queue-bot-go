package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type DBRepository struct {
	pool *pgxpool.Pool
}

func NewDBRepo(pool *pgxpool.Pool) *DBRepository {
	return &DBRepository{pool: pool}
}

func (db *DBRepository) GetQueue(ctx context.Context) ([]QueueEntry, error) {
	rows, err := db.pool.Query(ctx, "SELECT id, user_id, username, schedule_id, position FROM queue_entries")
		
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var queues []QueueEntry
	
	for rows.Next() {
		var entry QueueEntry
		rows.Scan(&entry.ID, &entry.UserId, &entry.Username, &entry.ScheduleID, &entry.Position)
		queues = append(queues, entry)
	}
	
	return queues, nil
}

func (db *DBRepository) EnterQueue(ctx context.Context, entry QueueEntry) error {
	_, err := db.pool.Exec(ctx, 
		"INSERT INTO queue_entries (user_id, username, schedule_id, position) VALUES ($1, $2, $3, $4)",
		entry.UserId, entry.Username, entry.ScheduleID, entry.Position,
	)
	
	if err != nil {
		return err
	}

	return nil
}

func (db *DBRepository) RemoveFromQueue(ctx context.Context, userID int64) error {
	_, err := db.pool.Exec(ctx,
		"DELETE FROM queue_entries WHERE user_id=$1",
		userID,
	)

	if err != nil {
		return err
	}

	return nil
}

func (db *DBRepository) IsPositionTaken(ctx context.Context, position int) (bool, error) {
	row := db.pool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM queue_entries WHERE position=$1)",
		position,
	)

	var taken bool
	err := row.Scan(&taken)

	if err != nil {
		return false, err
	}

	return taken, nil
}

func (db *DBRepository) ClearQueue(ctx context.Context, scheduleID int) error {
	_, err := db.pool.Exec(ctx,
		"DELETE FROM queue_entries WHERE schedule_id=$1",
		scheduleID,
	)

	if err != nil {
		return err
	}

	return nil
}