package access

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

var (
	ErrForbidden          = errors.New("only the developer can manage administrators")
	ErrInvalidID          = errors.New("user ID must be positive")
	ErrDeveloperProtected = errors.New("the developer role cannot be changed")
	ErrInvalidPermission  = errors.New("unknown permission")
	ErrNotFound           = errors.New("administrator not found")
)

type Store interface {
	BootstrapAdmins(context.Context, []int64) error
	HasAdmin(context.Context, int64) (bool, error)
	ListAdminIDs(context.Context) ([]int64, error)
	InsertAdmin(context.Context, int64) error
	DeleteAdmin(context.Context, int64) error
	LoadAdminPermissions(context.Context, int64) (map[Permission]bool, error)
	SetAdminPermission(context.Context, int64, Permission, bool) error
}

type Service struct {
	store       Store
	developerID int64
}

func NewService(store Store, developerID int64) *Service {
	return &Service{store: store, developerID: developerID}
}

func (s *Service) Initialize(ctx context.Context, legacyAdminIDs []int64) error {
	if s.developerID <= 0 {
		return ErrInvalidID
	}
	ids := make([]int64, 0, len(legacyAdminIDs))
	for _, id := range legacyAdminIDs {
		if id <= 0 {
			return ErrInvalidID
		}
		if !s.IsDeveloper(id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if err := s.store.BootstrapAdmins(ctx, ids); err != nil {
		return fmt.Errorf("initialize administrators: %w", err)
	}
	return nil
}

func (s *Service) IsDeveloper(id int64) bool {
	return id > 0 && id == s.developerID
}

func (s *Service) IsAdmin(ctx context.Context, id int64) (bool, error) {
	if id <= 0 {
		return false, ErrInvalidID
	}
	if s.IsDeveloper(id) {
		return true, nil
	}
	admin, err := s.store.HasAdmin(ctx, id)
	if err != nil {
		return false, fmt.Errorf("check administrator: %w", err)
	}
	return admin, nil
}

func (s *Service) ListAdmins(ctx context.Context) ([]int64, error) {
	ids, err := s.store.ListAdminIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list administrators: %w", err)
	}
	if s.developerID > 0 {
		ids = append(slices.Clone(ids), s.developerID)
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

func (s *Service) AddAdmin(ctx context.Context, actorID, targetID int64) error {
	if err := s.checkChange(actorID, targetID); err != nil {
		return err
	}
	if err := s.store.InsertAdmin(ctx, targetID); err != nil {
		return fmt.Errorf("add administrator: %w", err)
	}
	return nil
}

func (s *Service) checkChange(actorID, targetID int64) error {
	if !s.IsDeveloper(actorID) {
		return ErrForbidden
	}
	if targetID <= 0 {
		return ErrInvalidID
	}
	if s.IsDeveloper(targetID) {
		return ErrDeveloperProtected
	}
	return nil
}

func (s *Service) RemoveAdmin(ctx context.Context, actorID, targetID int64) error {
	if err := s.checkChange(actorID, targetID); err != nil {
		return err
	}
	if err := s.store.DeleteAdmin(ctx, targetID); err != nil {
		return fmt.Errorf("remove administrator: %w", err)
	}
	return nil
}

func (s *Service) Permissions(ctx context.Context, id int64) (map[Permission]bool, error) {
	if id <= 0 {
		return nil, ErrInvalidID
	}
	var stored map[Permission]bool
	if !s.IsDeveloper(id) {
		var err error
		stored, err = s.store.LoadAdminPermissions(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("load administrator permissions: %w", err)
		}
	}
	permissions := make(map[Permission]bool, len(AllPermissions()))
	for _, permission := range AllPermissions() {
		permissions[permission] = s.IsDeveloper(id) || stored[permission]
	}
	return permissions, nil
}

func (s *Service) Allowed(ctx context.Context, id int64, permission Permission) (bool, error) {
	if !validPermission(permission) {
		return false, ErrInvalidPermission
	}
	permissions, err := s.Permissions(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return permissions[permission], nil
}

func (s *Service) SetPermission(ctx context.Context, actorID, targetID int64, permission Permission, enabled bool) error {
	if err := s.checkChange(actorID, targetID); err != nil {
		return err
	}
	if !validPermission(permission) {
		return ErrInvalidPermission
	}
	if err := s.store.SetAdminPermission(ctx, targetID, permission, enabled); err != nil {
		return fmt.Errorf("set administrator permission: %w", err)
	}
	return nil
}
