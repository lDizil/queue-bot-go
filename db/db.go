package db

import (
	"context"
	"fmt"
	config "queuebot/config"

	"github.com/jackc/pgx/v5/pgxpool"
)

func SetUpDBConn(cfg config.Config) (*pgxpool.Pool, error) {
	databaseUrl := fmt.Sprintf("postgresql://%s:%s@postgres:%s/%s", cfg.DBUser, cfg.DBPass, cfg.DBPort, cfg.DBName)

	pool, err := pgxpool.New(context.Background(), databaseUrl)

	if err != nil {
		return nil, err
	}

	return pool, nil
}
