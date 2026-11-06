package access

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestInitializeValidatesAndImportsOnlyMutableAdmins(t *testing.T) {
	var imported []int64
	store := &storeStub{bootstrap: func(_ context.Context, ids []int64) error {
		imported = ids
		return nil
	}}
	legacy := []int64{30, 10, 20, 30}
	if err := NewService(store, 10).Initialize(t.Context(), legacy); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(imported, []int64{20, 30}) || !reflect.DeepEqual(legacy, []int64{30, 10, 20, 30}) {
		t.Fatalf("imported=%v original=%v", imported, legacy)
	}
	for _, developerID := range []int64{0, -1} {
		if err := NewService(nil, developerID).Initialize(t.Context(), nil); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("invalid developer %d: %v", developerID, err)
		}
	}
	for _, id := range []int64{0, -1} {
		if err := NewService(nil, 10).Initialize(t.Context(), []int64{20, id}); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("invalid legacy ID %d: %v", id, err)
		}
	}
}

func TestDeveloperHasEveryPermissionWithoutDatabase(t *testing.T) {
	service := NewService(nil, 10)
	if admin, err := service.IsAdmin(t.Context(), 10); err != nil || !admin {
		t.Fatalf("developer admin=%v err=%v", admin, err)
	}
	permissions, err := service.Permissions(t.Context(), 10)
	if err != nil || len(permissions) != len(AllPermissions()) {
		t.Fatalf("developer permissions=%v err=%v", permissions, err)
	}
	for _, permission := range AllPermissions() {
		allowed, err := service.Allowed(t.Context(), 10, permission)
		if err != nil || !allowed || !permissions[permission] {
			t.Fatalf("developer permission %s: allowed=%v err=%v", permission, allowed, err)
		}
	}
	for _, id := range []int64{0, -10, 20} {
		if service.IsDeveloper(id) {
			t.Fatalf("non-developer ID %d was accepted", id)
		}
	}
}

func TestAllRoleWritersEnforceDeveloperAndTargetProtection(t *testing.T) {
	service := NewService(nil, 10)
	writers := map[string]func(int64, int64) error{
		"add":    func(actor, target int64) error { return service.AddAdmin(t.Context(), actor, target) },
		"remove": func(actor, target int64) error { return service.RemoveAdmin(t.Context(), actor, target) },
		"set permission": func(actor, target int64) error {
			return service.SetPermission(t.Context(), actor, target, EditSchedule, true)
		},
	}
	for name, write := range writers {
		t.Run(name, func(t *testing.T) {
			for _, test := range []struct {
				actor  int64
				target int64
				want   error
			}{
				{0, 20, ErrForbidden},
				{20, 30, ErrForbidden},
				{10, 0, ErrInvalidID},
				{10, -20, ErrInvalidID},
				{10, 10, ErrDeveloperProtected},
			} {
				if err := write(test.actor, test.target); !errors.Is(err, test.want) {
					t.Fatalf("actor=%d target=%d: err=%v want=%v", test.actor, test.target, err, test.want)
				}
			}
		})
	}
	if err := NewService(nil, 0).AddAdmin(t.Context(), 0, 20); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unset developer acquired write access: %v", err)
	}
}

func TestAdminMembershipDoesNotGrantPermissionsAndReadsStayCurrent(t *testing.T) {
	var (
		member  = true
		granted = make(map[Permission]bool)
	)
	store := &storeStub{
		has: func(context.Context, int64) (bool, error) { return member, nil },
		permissions: func(context.Context, int64) (map[Permission]bool, error) {
			if !member {
				return nil, ErrNotFound
			}
			return granted, nil
		},
	}
	service := NewService(store, 10)
	if admin, err := service.IsAdmin(t.Context(), 20); err != nil || !admin {
		t.Fatalf("admin=%v err=%v", admin, err)
	}
	for _, permission := range AllPermissions() {
		if allowed, err := service.Allowed(t.Context(), 20, permission); err != nil || allowed {
			t.Fatalf("role itself granted %s: allowed=%v err=%v", permission, allowed, err)
		}
	}
	granted[EditSchedule] = true
	if allowed, err := service.Allowed(t.Context(), 20, EditSchedule); err != nil || !allowed {
		t.Fatalf("new permission was not visible: allowed=%v err=%v", allowed, err)
	}
	permissions, err := service.Permissions(t.Context(), 20)
	if err != nil || len(permissions) != len(AllPermissions()) || !permissions[EditSchedule] || permissions[ChangeWeek] {
		t.Fatalf("permissions=%v err=%v", permissions, err)
	}
	permissions[ChangeWeek] = true
	if granted[ChangeWeek] {
		t.Fatal("returned permissions alias persistent state")
	}
	granted[EditSchedule] = false
	if allowed, err := service.Allowed(t.Context(), 20, EditSchedule); err != nil || allowed {
		t.Fatalf("revocation was not visible: allowed=%v err=%v", allowed, err)
	}
	member = false
	if admin, err := service.IsAdmin(t.Context(), 20); err != nil || admin {
		t.Fatalf("role removal was not visible: admin=%v err=%v", admin, err)
	}
	if allowed, err := service.Allowed(t.Context(), 20, EditSchedule); err != nil || allowed {
		t.Fatalf("regular member allowed=%v err=%v", allowed, err)
	}
	if _, err := service.Permissions(t.Context(), 20); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing admin permissions: %v", err)
	}
}

func TestListAdminsIncludesDeveloperOnceInSortedOrder(t *testing.T) {
	stored := []int64{30, 10, 20}
	store := &storeStub{list: func(context.Context) ([]int64, error) { return stored, nil }}
	ids, err := NewService(store, 10).ListAdmins(t.Context())
	if err != nil || !reflect.DeepEqual(ids, []int64{10, 20, 30}) {
		t.Fatalf("admins=%v err=%v", ids, err)
	}
	if !reflect.DeepEqual(stored, []int64{30, 10, 20}) {
		t.Fatalf("list mutated stored state: %v", stored)
	}
}

func TestUnknownPermissionsAreRejectedEvenForDeveloper(t *testing.T) {
	service := NewService(nil, 10)
	for _, permission := range []Permission{"", "config", "roles", "default_chat"} {
		if allowed, err := service.Allowed(t.Context(), 10, permission); !errors.Is(err, ErrInvalidPermission) || allowed {
			t.Fatalf("unknown permission %q: allowed=%v err=%v", permission, allowed, err)
		}
		if err := service.SetPermission(t.Context(), 10, 20, permission, true); !errors.Is(err, ErrInvalidPermission) {
			t.Fatalf("grant unknown permission %q: %v", permission, err)
		}
	}
}

func TestStoreFailuresDenyAccessAndPreserveCause(t *testing.T) {
	unavailable := errors.New("database unavailable")
	store := &storeStub{
		bootstrap: func(context.Context, []int64) error { return unavailable },
		has:       func(context.Context, int64) (bool, error) { return true, unavailable },
		list:      func(context.Context) ([]int64, error) { return []int64{20}, unavailable },
		insert:    func(context.Context, int64) error { return unavailable },
		remove:    func(context.Context, int64) error { return unavailable },
		permissions: func(context.Context, int64) (map[Permission]bool, error) {
			return map[Permission]bool{EditSchedule: true}, unavailable
		},
		set: func(context.Context, int64, Permission, bool) error { return unavailable },
	}
	service := NewService(store, 10)
	if admin, err := service.IsAdmin(t.Context(), 20); admin || !errors.Is(err, unavailable) {
		t.Fatalf("admin=%v err=%v", admin, err)
	}
	if ids, err := service.ListAdmins(t.Context()); ids != nil || !errors.Is(err, unavailable) {
		t.Fatalf("admins=%v err=%v", ids, err)
	}
	if permissions, err := service.Permissions(t.Context(), 20); permissions != nil || !errors.Is(err, unavailable) {
		t.Fatalf("permissions=%v err=%v", permissions, err)
	}
	if allowed, err := service.Allowed(t.Context(), 20, EditSchedule); allowed || !errors.Is(err, unavailable) {
		t.Fatalf("allowed=%v err=%v", allowed, err)
	}
	for name, call := range map[string]func() error{
		"initialize":     func() error { return service.Initialize(t.Context(), []int64{20}) },
		"add":            func() error { return service.AddAdmin(t.Context(), 10, 20) },
		"remove":         func() error { return service.RemoveAdmin(t.Context(), 10, 20) },
		"set permission": func() error { return service.SetPermission(t.Context(), 10, 20, EditSchedule, true) },
	} {
		if err := call(); !errors.Is(err, unavailable) {
			t.Fatalf("%s lost failure cause: %v", name, err)
		}
	}
}

type storeStub struct {
	bootstrap   func(context.Context, []int64) error
	has         func(context.Context, int64) (bool, error)
	list        func(context.Context) ([]int64, error)
	insert      func(context.Context, int64) error
	remove      func(context.Context, int64) error
	permissions func(context.Context, int64) (map[Permission]bool, error)
	set         func(context.Context, int64, Permission, bool) error
}

var _ Store = (*storeStub)(nil)

func (s *storeStub) BootstrapAdmins(ctx context.Context, ids []int64) error {
	return s.bootstrap(ctx, ids)
}
func (s *storeStub) HasAdmin(ctx context.Context, id int64) (bool, error) { return s.has(ctx, id) }
func (s *storeStub) ListAdminIDs(ctx context.Context) ([]int64, error)    { return s.list(ctx) }
func (s *storeStub) InsertAdmin(ctx context.Context, id int64) error      { return s.insert(ctx, id) }
func (s *storeStub) DeleteAdmin(ctx context.Context, id int64) error      { return s.remove(ctx, id) }
func (s *storeStub) LoadAdminPermissions(ctx context.Context, id int64) (map[Permission]bool, error) {
	return s.permissions(ctx, id)
}
func (s *storeStub) SetAdminPermission(ctx context.Context, id int64, permission Permission, enabled bool) error {
	return s.set(ctx, id, permission, enabled)
}
