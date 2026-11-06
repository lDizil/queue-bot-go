package access

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	ErrForbidden          = errors.New("only the developer can manage administrators")
	ErrInvalidID          = errors.New("user ID must be positive")
	ErrDeveloperProtected = errors.New("the developer role cannot be changed")
	ErrInvalidPermission  = errors.New("unknown permission")
	ErrInvalidLabel       = errors.New("administrator label must contain at most 100 characters and no control characters")
	ErrNotFound           = errors.New("administrator not found")
)

type Admin struct {
	ID    int64
	Label string
}

type Store interface {
	BootstrapAdmins(context.Context, []int64) error
	HasAdmin(context.Context, int64) (bool, error)
	ListAdmins(context.Context) ([]Admin, error)
	GetAdmin(context.Context, int64) (Admin, error)
	SetAdminLabel(context.Context, int64, string) error
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

func (s *Service) ListAdmins(ctx context.Context) ([]Admin, error) {
	admins, err := s.store.ListAdmins(ctx)
	if err != nil {
		return nil, fmt.Errorf("list administrators: %w", err)
	}
	admins = slices.Clone(admins)
	if s.developerID > 0 {
		for index, admin := range admins {
			if s.IsDeveloper(admin.ID) {
				admins[index].Label = ""
			}
		}
		admins = append(admins, Admin{ID: s.developerID})
	}
	slices.SortFunc(admins, func(a, b Admin) int { return cmp.Compare(a.ID, b.ID) })
	return slices.CompactFunc(admins, func(a, b Admin) bool { return a.ID == b.ID }), nil
}

func (s *Service) Admin(ctx context.Context, id int64) (Admin, error) {
	if id <= 0 {
		return Admin{}, ErrInvalidID
	}
	if s.IsDeveloper(id) {
		return Admin{ID: id}, nil
	}
	admin, err := s.store.GetAdmin(ctx, id)
	if err != nil {
		return Admin{}, fmt.Errorf("load administrator: %w", err)
	}
	return admin, nil
}

func (s *Service) SetAdminLabel(ctx context.Context, actorID, targetID int64, label string) error {
	if err := s.checkChange(actorID, targetID); err != nil {
		return err
	}
	if !utf8.ValidString(label) || strings.ContainsFunc(label, unicode.IsControl) {
		return ErrInvalidLabel
	}
	label = strings.Join(strings.Fields(label), " ")
	if utf8.RuneCountInString(label) > 100 {
		return ErrInvalidLabel
	}
	if err := s.store.SetAdminLabel(ctx, targetID, label); err != nil {
		return fmt.Errorf("set administrator label: %w", err)
	}
	return nil
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
