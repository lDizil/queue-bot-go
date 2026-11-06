package db

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"queuebot/access"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestAccessBootstrapPreservesRoleRemovalAndRevokedPermissionsAfterRestart(t *testing.T) {
	repo, _ := newChatTestRepository(t, 10)
	ctx := t.Context()
	service := access.NewService(repo, 10)
	if err := service.Initialize(ctx, []int64{20, 10, 30, 20}); err != nil {
		t.Fatal(err)
	}
	if stored, err := repo.ListAdminIDs(ctx); err != nil || !reflect.DeepEqual(stored, []int64{20, 30}) {
		t.Fatalf("developer entered mutable role storage: admins=%v err=%v", stored, err)
	}
	for _, id := range []int64{10, 20, 30} {
		assertAccessPermissions(t, service, id, access.AllPermissions())
	}
	if err := service.RemoveAdmin(ctx, 10, 20); err != nil {
		t.Fatal(err)
	}
	if err := service.SetPermission(ctx, 10, 30, access.EditSchedule, false); err != nil {
		t.Fatal(err)
	}
	restarted := access.NewService(NewDBRepo(repo.pool), 10)
	if err := restarted.Initialize(ctx, []int64{20, 30, 40}); err != nil {
		t.Fatal(err)
	}
	if ids, err := restarted.ListAdmins(ctx); err != nil || !reflect.DeepEqual(ids, []int64{10, 30}) {
		t.Fatalf("restart restored obsolete environment roles: admins=%v err=%v", ids, err)
	}
	retained := slices.DeleteFunc(access.AllPermissions(), func(permission access.Permission) bool { return permission == access.EditSchedule })
	assertAccessPermissions(t, restarted, 30, retained)
	if err := restarted.AddAdmin(ctx, 10, 20); err != nil {
		t.Fatal(err)
	}
	assertAccessPermissions(t, restarted, 20, nil)
	if err := access.NewService(NewDBRepo(repo.pool), 10).Initialize(ctx, []int64{20, 30}); err != nil {
		t.Fatal(err)
	}
	assertAccessPermissions(t, restarted, 20, nil)
}

func TestAccessAdminPermissionLifecycleAndWriterProtection(t *testing.T) {
	repo, _ := newChatTestRepository(t, 10)
	ctx := t.Context()
	service := access.NewService(repo, 10)
	if err := service.Initialize(ctx, nil); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := service.AddAdmin(ctx, 10, 20); err != nil {
			t.Fatal(err)
		}
	}
	assertAccessPermissions(t, service, 20, nil)
	if err := service.AddAdmin(ctx, 20, 30); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("admin could add another admin: %v", err)
	}
	if err := service.RemoveAdmin(ctx, 20, 20); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("admin could remove a role: %v", err)
	}
	if err := service.SetPermission(ctx, 20, 20, access.EditSchedule, true); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("admin could grant own permission: %v", err)
	}
	for range 2 {
		if err := service.SetPermission(ctx, 10, 20, access.EditSchedule, true); err != nil {
			t.Fatal(err)
		}
		if err := service.SetPermission(ctx, 10, 20, access.ChangeWeek, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.AddAdmin(ctx, 10, 20); err != nil {
		t.Fatal(err)
	}
	assertAccessPermissions(t, access.NewService(NewDBRepo(repo.pool), 10), 20, []access.Permission{access.EditSchedule})
	for range 2 {
		if err := service.RemoveAdmin(ctx, 10, 20); err != nil {
			t.Fatal(err)
		}
	}
	for _, enabled := range []bool{true, false} {
		if err := service.SetPermission(ctx, 10, 20, access.EditSchedule, enabled); !errors.Is(err, access.ErrNotFound) {
			t.Fatalf("set permission for missing role, enabled=%v: %v", enabled, err)
		}
	}
	if allowed, err := service.Allowed(ctx, 20, access.EditSchedule); err != nil || allowed {
		t.Fatalf("removed admin still allowed=%v err=%v", allowed, err)
	}
	if err := service.AddAdmin(ctx, 10, 20); err != nil {
		t.Fatal(err)
	}
	assertAccessPermissions(t, service, 20, nil)
	for _, call := range []func() error{
		func() error { return service.AddAdmin(ctx, 10, 10) },
		func() error { return service.RemoveAdmin(ctx, 10, 10) },
		func() error { return service.SetPermission(ctx, 10, 10, access.EditSchedule, false) },
	} {
		if err := call(); !errors.Is(err, access.ErrDeveloperProtected) {
			t.Fatalf("developer role changed: %v", err)
		}
	}
	assertAccessPermissions(t, service, 10, access.AllPermissions())
}

func TestAccessConcurrentBootstrapImportsOneCompleteLegacySet(t *testing.T) {
	repo, _ := newChatTestRepository(t, 10)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	var (
		groups  = [][]int64{{100, 101}, {200, 201, 202}}
		start   = make(chan struct{})
		results = make(chan error, 24)
		workers sync.WaitGroup
	)
	for i := range 24 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			results <- access.NewService(NewDBRepo(repo.pool), 10).Initialize(ctx, groups[i%len(groups)])
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	ids, err := repo.ListAdminIDs(ctx)
	if err != nil || (!reflect.DeepEqual(ids, groups[0]) && !reflect.DeepEqual(ids, groups[1])) {
		t.Fatalf("concurrent bootstrap mixed legacy snapshots: admins=%v err=%v", ids, err)
	}
	service := access.NewService(repo, 10)
	for _, id := range ids {
		assertAccessPermissions(t, service, id, access.AllPermissions())
	}
	var markerCount int
	if err := repo.pool.QueryRow(ctx, "SELECT count(*) FROM access_bootstrap").Scan(&markerCount); err != nil || markerCount != 1 {
		t.Fatalf("bootstrap markers=%d err=%v", markerCount, err)
	}
}

func TestAccessFailedBootstrapRollsBackMarkerAndRoles(t *testing.T) {
	repo, _ := newChatTestRepository(t, 10)
	ctx := t.Context()
	var constraint *pgconn.PgError
	if err := repo.BootstrapAdmins(ctx, []int64{20, -1}); !errors.As(err, &constraint) || constraint.Code != "23514" {
		t.Fatalf("invalid ID bypassed database constraint: %v", err)
	}
	if ids, err := repo.ListAdminIDs(ctx); err != nil || len(ids) != 0 {
		t.Fatalf("failed bootstrap partially stored roles: ids=%v err=%v", ids, err)
	}
	service := access.NewService(repo, 10)
	if err := service.Initialize(ctx, []int64{30}); err != nil {
		t.Fatal(err)
	}
	if ids, err := repo.ListAdminIDs(ctx); err != nil || !reflect.DeepEqual(ids, []int64{30}) {
		t.Fatalf("failed bootstrap persisted its marker: ids=%v err=%v", ids, err)
	}
	assertAccessPermissions(t, service, 30, access.AllPermissions())
}

func TestAccessPermissionWritesRaceSafelyWithRoleRemoval(t *testing.T) {
	repo, _ := newChatTestRepository(t, 10)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	service := access.NewService(repo, 10)
	if err := service.Initialize(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := service.AddAdmin(ctx, 10, 20); err != nil {
		t.Fatal(err)
	}
	var (
		start   = make(chan struct{})
		results = make(chan error, 17)
		workers sync.WaitGroup
	)
	for range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			results <- service.SetPermission(ctx, 10, 20, access.EditSchedule, true)
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		<-start
		results <- service.RemoveAdmin(ctx, 10, 20)
	}()
	close(start)
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil && !errors.Is(err, access.ErrNotFound) {
			t.Fatalf("concurrent role removal leaked storage error: %v", err)
		}
	}
	if admin, err := service.IsAdmin(ctx, 20); err != nil || admin {
		t.Fatalf("role removal lost: admin=%v err=%v", admin, err)
	}
	if err := service.AddAdmin(ctx, 10, 20); err != nil {
		t.Fatal(err)
	}
	assertAccessPermissions(t, service, 20, nil)
}

func assertAccessPermissions(t *testing.T, service *access.Service, id int64, granted []access.Permission) {
	t.Helper()
	permissions, err := service.Permissions(t.Context(), id)
	if err != nil || len(permissions) != len(access.AllPermissions()) {
		t.Fatalf("permissions for %d=%v err=%v", id, permissions, err)
	}
	for _, permission := range access.AllPermissions() {
		expected := slices.Contains(granted, permission)
		if permissions[permission] != expected {
			t.Fatalf("permission %s for %d=%v want=%v", permission, id, permissions[permission], expected)
		}
	}
}
