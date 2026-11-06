package db

import (
	"context"
	"errors"

	"queuebot/access"

	"github.com/jackc/pgx/v5"
)

var _ access.Store = (*DBRepository)(nil)

func (db *DBRepository) BootstrapAdmins(ctx context.Context, ids []int64) error {
	tx, err := db.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, "INSERT INTO access_bootstrap (singleton) VALUES (TRUE) ON CONFLICT DO NOTHING")
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		if _, err := tx.Exec(ctx, "INSERT INTO global_admins (user_id) SELECT unnest($1::bigint[]) ON CONFLICT DO NOTHING", ids); err != nil {
			return err
		}
		var permissions []string
		for _, permission := range access.AllPermissions() {
			permissions = append(permissions, string(permission))
		}
		if _, err := tx.Exec(ctx, `INSERT INTO admin_permissions (user_id, permission)
 SELECT user_id, permission FROM unnest($1::bigint[]) AS user_id CROSS JOIN unnest($2::text[]) AS permission
 ON CONFLICT DO NOTHING`, ids, permissions); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (db *DBRepository) HasAdmin(ctx context.Context, id int64) (bool, error) {
	var admin bool
	err := db.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM global_admins WHERE user_id = $1)", id).Scan(&admin)
	return admin, err
}

func (db *DBRepository) ListAdminIDs(ctx context.Context) ([]int64, error) {
	rows, err := db.pool.Query(ctx, "SELECT user_id FROM global_admins ORDER BY user_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func (db *DBRepository) InsertAdmin(ctx context.Context, id int64) error {
	_, err := db.pool.Exec(ctx, "INSERT INTO global_admins (user_id) VALUES ($1) ON CONFLICT DO NOTHING", id)
	return err
}

func (db *DBRepository) DeleteAdmin(ctx context.Context, id int64) error {
	_, err := db.pool.Exec(ctx, "DELETE FROM global_admins WHERE user_id = $1", id)
	return err
}

func (db *DBRepository) LoadAdminPermissions(ctx context.Context, id int64) (map[access.Permission]bool, error) {
	rows, err := db.pool.Query(ctx, `SELECT permission FROM global_admins
 LEFT JOIN admin_permissions USING (user_id) WHERE user_id = $1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var found bool
	permissions := make(map[access.Permission]bool)
	for rows.Next() {
		found = true
		var permission *string
		if err := rows.Scan(&permission); err != nil {
			return nil, err
		}
		if permission != nil {
			permissions[access.Permission(*permission)] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, access.ErrNotFound
	}
	return permissions, nil
}

func (db *DBRepository) SetAdminPermission(ctx context.Context, id int64, permission access.Permission, enabled bool) error {
	tx, err := db.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var existingID int64
	if err := tx.QueryRow(ctx, "SELECT user_id FROM global_admins WHERE user_id = $1 FOR KEY SHARE", id).Scan(&existingID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return access.ErrNotFound
		}
		return err
	}
	if enabled {
		if _, err := tx.Exec(ctx, "INSERT INTO admin_permissions (user_id, permission) VALUES ($1, $2) ON CONFLICT DO NOTHING", id, string(permission)); err != nil {
			return err
		}
	}
	if !enabled {
		if _, err := tx.Exec(ctx, "DELETE FROM admin_permissions WHERE user_id = $1 AND permission = $2", id, string(permission)); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
