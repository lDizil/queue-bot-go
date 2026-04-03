package db

import (
	"context"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DBRepository struct {
	pool *pgxpool.Pool
}

func NewDBRepo(pool *pgxpool.Pool) *DBRepository {
	return &DBRepository{pool: pool}
}

func SetUpDBConn(databaseUrl string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(context.Background(), databaseUrl)

	if err != nil {
		return nil, err
	}

	return pool, nil
}

func RunMigrations(databaseUrl string) error {
	m, err := migrate.New("file://migrations", databaseUrl)

	if err != nil {
		return err
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return err
	}

	return nil
}
