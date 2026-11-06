package chats

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestInitializeKeepsStoredDefaultAndUsesLegacyPriorityWithoutReadingTelegram(t *testing.T) {
	for _, test := range []struct {
		name   string
		known  []Chat
		legacy int64
		want   int64
	}{
		{"stored wins", []Chat{{ID: -1, IsDefault: true}}, -2, -1},
		{"legacy wins over only schedule", []Chat{{ID: -1}}, -2, -2},
		{"single known chat", []Chat{{ID: -1}}, 0, -1},
		{"legacy private default", []Chat{{ID: 123}}, 0, 123},
		{"legacy private environment", []Chat{{ID: -1}}, 123, 123},
		{"unassigned rows are not a default candidate", []Chat{{ID: 0}, {ID: -1}}, 0, -1},
		{"only unassigned rows stay unselected", []Chat{{ID: 0}}, 0, 0},
		{"several known chats stay unselected", []Chat{{ID: -1}, {ID: -2}}, 0, 0},
		{"no address is invented", nil, 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryStore(test.known...)
			service := NewService(store, readerFunc(func(context.Context, int64) (Info, error) {
				t.Fatal("bootstrap attempted a Telegram request")
				return Info{}, nil
			}))
			if err := service.Initialize(t.Context(), test.legacy); err != nil {
				t.Fatal(err)
			}
			selected, err := service.Default(t.Context())
			if test.want == 0 {
				if !errors.Is(err, ErrDefaultNotSet) {
					t.Fatalf("an unknown default was guessed: %+v err=%v", selected, err)
				}
				return
			}
			if err != nil || selected.ID != test.want {
				t.Fatalf("default=%+v err=%v want=%d", selected, err, test.want)
			}
			if test.name == "stored wins" && len(store.known) != 1 {
				t.Fatal("restart registered an obsolete environment default")
			}
		})
	}
}

func TestMetadataReplyCannotOverwriteConcurrentManualAlias(t *testing.T) {
	store := newMemoryStore(Chat{ID: -1})
	var (
		started = make(chan struct{})
		release = make(chan struct{})
	)
	service := NewService(store, readerFunc(func(ctx context.Context, id int64) (Info, error) {
		close(started)
		select {
		case <-release:
			return Info{Title: "Telegram title"}, nil
		case <-ctx.Done():
			return Info{}, ctx.Err()
		}
	}))
	result := make(chan []Chat, 1)
	go func() {
		known, _ := service.List(t.Context())
		result <- known
	}()
	<-started
	if _, err := service.Rename(t.Context(), -1, "Ручное название"); err != nil {
		t.Fatal(err)
	}
	close(release)
	known := <-result
	if len(known) != 1 || known[0].Name != "Ручное название" {
		t.Fatalf("late metadata overwrote a manual alias: %+v", known)
	}
	if err := service.Remember(t.Context(), -1, "Another automatic title"); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetChat(t.Context(), -1)
	if err != nil || current.Name != "Ручное название" {
		t.Fatalf("group update overwrote a manual alias: %+v err=%v", current, err)
	}
}

func TestListSharesOneMetadataDeadlineAndKeepsFallbacks(t *testing.T) {
	store := newMemoryStore(Chat{ID: -1}, Chat{ID: -2}, Chat{ID: -3})
	var calls int
	service := NewService(store, readerFunc(func(ctx context.Context, id int64) (Info, error) {
		calls++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("metadata lookup has no deadline")
		}
		<-ctx.Done()
		return Info{}, ctx.Err()
	}))
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	known, err := service.List(ctx)
	if err != nil || len(known) != 3 || calls != 1 {
		t.Fatalf("metadata failure hid known chats or restarted the budget: known=%+v calls=%d err=%v", known, calls, err)
	}
	for _, chat := range known {
		if chat.Name != "" {
			t.Fatalf("an unavailable title was invented: %+v", chat)
		}
	}
}

func TestExistingNamesAvoidLookupsAndMetadataFailuresRemainBestEffort(t *testing.T) {
	store := newMemoryStore(Chat{ID: -1, Name: "Alias"}, Chat{ID: -2})
	cause := errors.New("Telegram unavailable")
	service := NewService(store, readerFunc(func(_ context.Context, id int64) (Info, error) {
		if id == -1 {
			t.Fatal("a nonempty alias was refreshed automatically")
		}
		return Info{}, cause
	}))
	known, err := service.List(t.Context())
	if err != nil || len(known) != 2 {
		t.Fatalf("title lookup failed the list: %+v err=%v", known, err)
	}
	if chat, err := service.Get(t.Context(), -2); err != nil || chat.Name != "" {
		t.Fatalf("selection did not preserve an ID fallback: %+v err=%v", chat, err)
	}
	if _, err := service.Add(t.Context(), -3); !errors.Is(err, cause) {
		t.Fatalf("explicit add lost its verification failure: %v", err)
	}
	if _, err := store.GetChat(t.Context(), -3); !errors.Is(err, ErrNotFound) {
		t.Fatal("failed verification registered a new chat")
	}
}

func TestResolveTargetUsesStoredDefaultOnlyForPrivateCommands(t *testing.T) {
	store := newMemoryStore(Chat{ID: -1, Name: "First", IsDefault: true}, Chat{ID: -2, Name: "Second"})
	service := NewService(store, nil)
	if id, err := service.ResolveTarget(t.Context(), -2); err != nil || id != -2 {
		t.Fatalf("group command was redirected to the default: id=%d err=%v", id, err)
	}
	if id, err := service.ResolveTarget(t.Context(), 789); err != nil || id != -1 {
		t.Fatalf("private command lost the stored default: id=%d err=%v", id, err)
	}
	if _, err := service.SetDefault(t.Context(), -2); err != nil {
		t.Fatal(err)
	}
	if err := service.Initialize(t.Context(), -1); err != nil {
		t.Fatal(err)
	}
	if id, err := service.ResolveTarget(t.Context(), 789); err != nil || id != -2 {
		t.Fatalf("restart restored the obsolete environment target: id=%d err=%v", id, err)
	}
	if _, err := service.ResolveTarget(t.Context(), 0); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("zero source ID was accepted: %v", err)
	}
	unset := NewService(newMemoryStore(), nil)
	if _, err := unset.ResolveTarget(t.Context(), 789); !errors.Is(err, ErrDefaultNotSet) {
		t.Fatalf("private target was guessed without a default: %v", err)
	}
}

func TestAddRenameAndRememberKeepIdentityAndValidateNames(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store, readerFunc(func(context.Context, int64) (Info, error) {
		return Info{Title: "Telegram title"}, nil
	}))
	added, err := service.Add(t.Context(), -1)
	if err != nil || added.ID != -1 || added.Name != "Telegram title" {
		t.Fatalf("new chat metadata was not saved: %+v err=%v", added, err)
	}
	if _, err := service.Default(t.Context()); !errors.Is(err, ErrDefaultNotSet) {
		t.Fatal("adding a chat unexpectedly changed the default")
	}
	renamed, err := service.Rename(t.Context(), -1, "  Local alias  ")
	if err != nil || renamed.ID != -1 || renamed.Name != "Local alias" {
		t.Fatalf("rename changed chat identity or did not trim input: %+v err=%v", renamed, err)
	}
	for _, name := range []string{"  ", strings.Repeat("я", 201)} {
		if _, err := service.Rename(t.Context(), -1, name); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("invalid alias was accepted: %v", err)
		}
	}
	if _, err := service.SetDefault(t.Context(), -99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown chat became the default: %v", err)
	}
	if _, err := service.Add(t.Context(), 0); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("zero chat ID was registered: %v", err)
	}
	if err := service.Remember(t.Context(), -2, "Observed group"); err != nil {
		t.Fatal(err)
	}
	if chat, err := store.GetChat(t.Context(), -2); err != nil || chat.Name != "Observed group" {
		t.Fatalf("observed group was not registered: %+v err=%v", chat, err)
	}
}

func TestUnassignedLegacyChatIsVisibleButCannotBeRegisteredOrSelectedAsDefault(t *testing.T) {
	store := newMemoryStore(Chat{ID: 0})
	service := NewService(store, readerFunc(func(context.Context, int64) (Info, error) {
		t.Fatal("virtual legacy chat attempted a Telegram lookup")
		return Info{}, nil
	}))
	known, err := service.List(t.Context())
	if err != nil || len(known) != 1 || known[0].ID != 0 || known[0].IsDefault {
		t.Fatalf("unassigned schedules disappeared from the list: %+v err=%v", known, err)
	}
	if chat, err := service.Get(t.Context(), 0); err != nil || chat.ID != 0 {
		t.Fatalf("unassigned schedules cannot be opened: %+v err=%v", chat, err)
	}
	if _, err := service.Add(t.Context(), 0); !errors.Is(err, ErrInvalidID) {
		t.Fatal("virtual chat could be registered")
	}
	if _, err := service.SetDefault(t.Context(), 0); !errors.Is(err, ErrInvalidID) {
		t.Fatal("virtual chat could become the default")
	}
	if _, err := service.Rename(t.Context(), 0, "Alias"); !errors.Is(err, ErrInvalidID) {
		t.Fatal("virtual chat could be renamed")
	}
}

func TestKnownLegacyPrivateChatRemainsEditableWithoutGroupVerification(t *testing.T) {
	store := newMemoryStore(Chat{ID: 123})
	service := NewService(store, readerFunc(func(context.Context, int64) (Info, error) {
		return Info{}, errors.New("Telegram private chat is not a group")
	}))
	if chat, err := service.Add(t.Context(), 123); err != nil || chat.ID != 123 {
		t.Fatalf("an imported private chat was rejected: %+v err=%v", chat, err)
	}
	if _, err := service.Rename(t.Context(), 123, "Legacy private"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetDefault(t.Context(), 123); err != nil {
		t.Fatal(err)
	}
	if target, err := service.ResolveTarget(t.Context(), 789); err != nil || target != 123 {
		t.Fatalf("legacy private default was no longer routable: target=%d err=%v", target, err)
	}
}

func TestMetadataEnrichmentDoesNotMixDefaultSnapshots(t *testing.T) {
	store := newMemoryStore(Chat{ID: -2, Name: "First", IsDefault: true}, Chat{ID: -1})
	service := NewService(store, readerFunc(func(ctx context.Context, id int64) (Info, error) {
		if err := store.SetDefaultChat(ctx, -1, false); err != nil {
			t.Fatal(err)
		}
		return Info{Title: "Second"}, nil
	}))
	known, err := service.List(t.Context())
	if err != nil || len(known) != 2 || !known[0].IsDefault || known[1].IsDefault || known[1].Name != "Second" {
		t.Fatalf("metadata read mixed two default selections into one list: %+v err=%v", known, err)
	}
	if selected, err := service.Default(t.Context()); err != nil || selected.ID != -1 {
		t.Fatalf("metadata lookup undid the concurrent manual default change: %+v err=%v", selected, err)
	}
}

type readerFunc func(context.Context, int64) (Info, error)

var _ Reader = readerFunc(nil)

func (reader readerFunc) ReadChat(ctx context.Context, id int64) (Info, error) {
	return reader(ctx, id)
}

type memoryStore struct {
	mu    sync.Mutex
	known map[int64]Chat
}

var _ Store = (*memoryStore)(nil)

func newMemoryStore(known ...Chat) *memoryStore {
	store := &memoryStore{known: make(map[int64]Chat)}
	for _, chat := range known {
		store.known[chat.ID] = chat
	}
	return store
}

func (store *memoryStore) ListChats(context.Context) ([]Chat, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var known []Chat
	for _, chat := range store.known {
		known = append(known, chat)
	}
	sort.Slice(known, func(i, j int) bool { return known[i].ID < known[j].ID })
	return known, nil
}

func (store *memoryStore) GetChat(_ context.Context, id int64) (Chat, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if chat, ok := store.known[id]; ok {
		return chat, nil
	}
	return Chat{}, ErrNotFound
}

func (store *memoryStore) EnsureChat(_ context.Context, id int64, name string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	chat := store.known[id]
	chat.ID = id
	if chat.Name == "" {
		chat.Name = name
	}
	store.known[id] = chat
	return nil
}

func (store *memoryStore) SetChatName(_ context.Context, id int64, name string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	chat, ok := store.known[id]
	if !ok {
		return ErrNotFound
	}
	chat.Name = name
	store.known[id] = chat
	return nil
}

func (store *memoryStore) SetDefaultChat(_ context.Context, id int64, onlyIfUnset bool) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, chat := range store.known {
		if onlyIfUnset && chat.IsDefault {
			return nil
		}
	}
	if _, ok := store.known[id]; !ok {
		return ErrNotFound
	}
	for currentID, chat := range store.known {
		chat.IsDefault = currentID == id
		store.known[currentID] = chat
	}
	return nil
}
