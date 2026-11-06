package chats

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrNotFound          = errors.New("chat not found")
	ErrDefaultNotSet     = errors.New("default chat is not set")
	ErrInvalidID         = errors.New("chat ID must not be zero")
	ErrInvalidName       = errors.New("chat name must contain between 1 and 200 characters")
	ErrReaderUnavailable = errors.New("chat reader is unavailable")
)

const metadataTimeout = 3 * time.Second

type Chat struct {
	ID        int64
	Name      string
	IsDefault bool
}

type Info struct{ Title string }

type Store interface {
	ListChats(context.Context) ([]Chat, error)
	GetChat(context.Context, int64) (Chat, error)
	EnsureChat(context.Context, int64, string) error
	SetChatName(context.Context, int64, string) error
	SetDefaultChat(context.Context, int64, bool) error
}

type Reader interface {
	ReadChat(context.Context, int64) (Info, error)
}

type Service struct {
	store  Store
	reader Reader
}

func NewService(store Store, reader Reader) *Service {
	return &Service{store: store, reader: reader}
}

func (s *Service) Initialize(ctx context.Context, legacyDefaultID int64) error {
	known, err := s.store.ListChats(ctx)
	if err != nil {
		return err
	}
	var (
		candidate  int64
		candidates int
	)
	for _, chat := range known {
		if chat.IsDefault {
			return nil
		}
		if chat.ID != 0 {
			candidate = chat.ID
			candidates++
		}
	}
	if legacyDefaultID == 0 && candidates == 1 {
		legacyDefaultID = candidate
	}
	if legacyDefaultID == 0 {
		return nil
	}
	if err := s.store.EnsureChat(ctx, legacyDefaultID, ""); err != nil {
		return err
	}
	return s.store.SetDefaultChat(ctx, legacyDefaultID, true)
}

func (s *Service) List(ctx context.Context) ([]Chat, error) {
	known, err := s.store.ListChats(ctx)
	if err != nil {
		return nil, err
	}
	metadataCtx, cancel := context.WithTimeout(ctx, metadataTimeout)
	defer cancel()
	for index, chat := range known {
		if metadataCtx.Err() != nil {
			break
		}
		known[index] = s.enrich(metadataCtx, chat)
	}
	return known, nil
}

func (s *Service) Get(ctx context.Context, id int64) (Chat, error) {
	chat, err := s.store.GetChat(ctx, id)
	if err != nil {
		return Chat{}, err
	}
	metadataCtx, cancel := context.WithTimeout(ctx, metadataTimeout)
	defer cancel()
	return s.enrich(metadataCtx, chat), nil
}

func (s *Service) enrich(ctx context.Context, chat Chat) Chat {
	if chat.ID == 0 || chat.Name != "" || s.reader == nil {
		return chat
	}
	info, err := s.reader.ReadChat(ctx, chat.ID)
	if err != nil {
		return chat
	}
	name := strings.TrimSpace(info.Title)
	if !validName(name) {
		return chat
	}
	if err := s.store.EnsureChat(ctx, chat.ID, name); err != nil {
		return chat
	}
	if current, err := s.store.GetChat(ctx, chat.ID); err == nil {
		chat.Name = current.Name
	}
	return chat
}

func validName(name string) bool {
	return name != "" && utf8.RuneCountInString(name) <= 200
}

func (s *Service) Add(ctx context.Context, id int64) (Chat, error) {
	if id == 0 {
		return Chat{}, ErrInvalidID
	}
	if chat, err := s.Get(ctx, id); !errors.Is(err, ErrNotFound) {
		return chat, err
	}
	if s.reader == nil {
		return Chat{}, ErrReaderUnavailable
	}
	metadataCtx, cancel := context.WithTimeout(ctx, metadataTimeout)
	defer cancel()
	info, err := s.reader.ReadChat(metadataCtx, id)
	if err != nil {
		return Chat{}, fmt.Errorf("read chat: %w", err)
	}
	name := strings.TrimSpace(info.Title)
	if name != "" && !validName(name) {
		return Chat{}, ErrInvalidName
	}
	if err := s.store.EnsureChat(ctx, id, name); err != nil {
		return Chat{}, err
	}
	return s.store.GetChat(ctx, id)
}

func (s *Service) Rename(ctx context.Context, id int64, name string) (Chat, error) {
	name = strings.TrimSpace(name)
	if id == 0 {
		return Chat{}, ErrInvalidID
	}
	if !validName(name) {
		return Chat{}, ErrInvalidName
	}
	if _, err := s.store.GetChat(ctx, id); err != nil {
		return Chat{}, err
	}
	if err := s.store.EnsureChat(ctx, id, ""); err != nil {
		return Chat{}, err
	}
	if err := s.store.SetChatName(ctx, id, name); err != nil {
		return Chat{}, err
	}
	return s.store.GetChat(ctx, id)
}

func (s *Service) SetDefault(ctx context.Context, id int64) (Chat, error) {
	if id == 0 {
		return Chat{}, ErrInvalidID
	}
	if _, err := s.store.GetChat(ctx, id); err != nil {
		return Chat{}, err
	}
	if err := s.store.EnsureChat(ctx, id, ""); err != nil {
		return Chat{}, err
	}
	if err := s.store.SetDefaultChat(ctx, id, false); err != nil {
		return Chat{}, err
	}
	return s.Get(ctx, id)
}

func (s *Service) Default(ctx context.Context) (Chat, error) {
	known, err := s.store.ListChats(ctx)
	if err != nil {
		return Chat{}, err
	}
	for _, chat := range known {
		if chat.IsDefault {
			return chat, nil
		}
	}
	return Chat{}, ErrDefaultNotSet
}

func (s *Service) ResolveTarget(ctx context.Context, sourceChatID int64) (int64, error) {
	if sourceChatID == 0 {
		return 0, ErrInvalidID
	}
	if sourceChatID < 0 {
		return sourceChatID, nil
	}
	chat, err := s.Default(ctx)
	if err != nil {
		return 0, err
	}
	return chat.ID, nil
}

func (s *Service) Remember(ctx context.Context, id int64, title string) error {
	if id == 0 {
		return ErrInvalidID
	}
	name := strings.TrimSpace(title)
	if name != "" && !validName(name) {
		return ErrInvalidName
	}
	return s.store.EnsureChat(ctx, id, name)
}
