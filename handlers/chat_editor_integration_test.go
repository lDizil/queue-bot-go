package handlers

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"queuebot/chats"
	"queuebot/db"
	"queuebot/queue"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5"
)

func TestScheduleEditorAlwaysStartsWithChatListAndCreatesInSelectedChat(t *testing.T) {
	for _, source := range []struct {
		name   string
		chat   int64
		thread int
		target int64
	}{{"private menu", 99, 0, -200}, {"another group menu", -900, 8, -200}, {"known legacy private destination", 99, 0, 77}} {
		t.Run(source.name, func(t *testing.T) {
			h, telegram, api, _ := newChatEditor(t)
			if source.target > 0 {
				if err := h.chats.Remember(t.Context(), source.target, "Legacy destination"); err != nil {
					t.Fatal(err)
				}
			}
			processTelegramUpdates(t, h, telegram, chatEditorMessage(h.developerID, source.chat, source.thread, "/edit_schedule"))
			opened := lastMenuText(t, api)
			if !strings.Contains(opened, "Выберите чат") {
				t.Fatalf("editor skipped chat selection: %q", opened)
			}
			processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, fmt.Sprintf("SelectChat:%d", source.target)))
			processTelegramUpdates(t, h, telegram,
				chatEditorCallback(h, h.developerID, fmt.Sprintf("AddNewSchedule:%d", source.target)),
				chatEditorMessage(h.developerID, source.chat, source.thread, "понедельник, нечётная, 11:00, 12:30, 7, Лабораторная, Заголовок очереди"))
			rows, err := h.db.GetAllSchedules(t.Context())
			if err != nil || len(rows) != 1 || rows[0].ChatID != source.target || rows[0].ThreadID != 7 || rows[0].Title != "Заголовок очереди" {
				t.Fatalf("schedule inherited the menu address or lost its fields: rows=%+v err=%v", rows, err)
			}
			if !strings.Contains(lastMenuText(t, api), strconv.FormatInt(source.target, 10)) {
				t.Fatal("selected chat disappeared after creating a schedule")
			}
			for _, request := range api.history() {
				if request.method != "sendMessage" && request.method != "editMessageText" {
					continue
				}
				if request.values["chat_id"] != strconv.FormatInt(source.chat, 10) {
					t.Fatalf("editing UI moved from its source chat: %+v", request)
				}
				if request.method == "sendMessage" && request.values["message_thread_id"] != strconv.Itoa(source.thread) && !(source.thread == 0 && request.values["message_thread_id"] == "") {
					t.Fatalf("editing UI moved from its source topic: %+v", request)
				}
			}
		})
	}
}

func TestChangingChatAndReturningToListCancelPendingInputAndStaleButtons(t *testing.T) {
	h, telegram, _, _ := newChatEditor(t)
	const source = int64(99)
	processTelegramUpdates(t, h, telegram, chatEditorMessage(h.developerID, source, 0, "/edit_schedule"))
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, "SelectChat:-100"))
	staleAdd := chatEditorCallback(h, h.developerID, "AddNewSchedule:-100")
	processTelegramUpdates(t, h, telegram, staleAdd)
	processTelegramUpdates(t, h, telegram,
		chatEditorCallback(h, h.developerID, "SelectChat:-200"),
		staleAdd,
		chatEditorMessage(h.developerID, source, 0, "понедельник, нечётная, 11:00, 12:30, 7"))
	rows, err := h.db.GetAllSchedules(t.Context())
	if err != nil || len(rows) != 0 {
		t.Fatalf("old pending input created a schedule in another chat: %+v %v", rows, err)
	}
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, "AddNewSchedule:-200"))
	processTelegramUpdates(t, h, telegram,
		chatEditorMessage(h.developerID, -900, 0, "понедельник, нечётная, 11:00, 12:30, 7"),
		chatEditorMessage(h.developerID, source, 9, "понедельник, нечётная, 11:00, 12:30, 7"),
		chatEditorCallback(h, h.developerID, "ChatList"),
		chatEditorMessage(h.developerID, source, 0, "понедельник, нечётная, 11:00, 12:30, 7"))
	rows, err = h.db.GetAllSchedules(t.Context())
	if err != nil || len(rows) != 0 {
		t.Fatalf("input escaped its menu context or survived return to chat list: %+v %v", rows, err)
	}
}

func TestDefaultSelectionIsDeveloperOnlyAndPrivateCommandDoesNotUseEditorTarget(t *testing.T) {
	h, telegram, api, scheduler := newChatEditor(t)
	processTelegramUpdates(t, h, telegram, chatEditorMessage(h.developerID, 99, 0, "/edit_schedule"))
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, "SelectChat:-200"))
	if !strings.Contains(lastMenuMarkup(t, api), "SetDefaultChat:-200") {
		t.Fatal("developer cannot select the main chat from its card")
	}
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, "SetDefaultChat:-200"))
	processTelegramUpdates(t, h, telegram, chatEditorMessage(123, 88, 0, "/edit_schedule"))
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, 123, "SelectChat:-100"))
	if strings.Contains(lastMenuMarkup(t, api), "SetDefaultChat:") {
		t.Fatal("ordinary administrator sees the global default setting")
	}
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, 123, "SetDefaultChat:-100"))
	main, err := h.chats.Default(t.Context())
	if err != nil || main.ID != -200 {
		t.Fatalf("ordinary administrator changed the main chat: %+v %v", main, err)
	}
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, "SelectChat:-100"))
	processTelegramUpdates(t, h, telegram,
		chatEditorMessage(h.developerID, 99, 0, "/one_shot_queue Private command"),
		chatEditorMessage(h.developerID, -900, 8, "/one_shot_queue Group command"))
	calls := scheduler.instantCalls()
	if len(calls) != 2 {
		t.Fatalf("one-shot calls=%+v", calls)
	}
	var private, group bool
	for _, call := range calls {
		private = private || call.chat == -200 && call.thread == 0 && call.title == "Private command"
		group = group || call.chat == -900 && call.thread == 8 && call.title == "Group command"
	}
	if !private || !group {
		t.Fatalf("commands used the selected editor target instead of their own addressing rules: %+v", calls)
	}
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, "SetDefaultChat:-100"))
	processTelegramUpdates(t, h, telegram, chatEditorMessage(h.developerID, 99, 0, "/one_shot_queue Changed default"))
	calls = scheduler.instantCalls()
	if calls[len(calls)-1].chat != -100 {
		t.Fatalf("private command kept an old default: %+v", calls)
	}
}

func TestChatAliasDoesNotChangeScheduleTitleOrRunningQueue(t *testing.T) {
	h, telegram, api, _ := newChatEditor(t)
	id := addChatSchedule(t, h, -200, "Заголовок очереди")
	opens := time.Date(2026, 11, 6, 12, 0, 0, 123456000, time.UTC)
	closes := opens.Add(time.Hour)
	if _, err := h.db.BeginQueueRun(t.Context(), id, opens, closes, 30, 5); err != nil {
		t.Fatal(err)
	}
	if err := h.db.SetQueueMessageID(t.Context(), id, 901); err != nil {
		t.Fatal(err)
	}
	if err := h.db.JoinQueue(t.Context(), db.QueueEntry{ScheduleID: id, UserID: 101, Position: 1, Completed: true}); err != nil {
		t.Fatal(err)
	}
	processTelegramUpdates(t, h, telegram, chatEditorMessage(h.developerID, 99, 0, "/edit_schedule"))
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, "SelectChat:-200"))
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, "RenameChat:-200"), chatEditorMessage(h.developerID, 99, 0, "Локальное имя"))
	row, err := h.db.GetScheduleEntry(t.Context(), id)
	if err != nil || row.ChatID != -200 || row.Title != "Заголовок очереди" || row.QueueMesID == nil || *row.QueueMesID != 901 || row.ActiveOpensAt == nil || !row.ActiveOpensAt.Equal(opens) {
		t.Fatalf("renaming a chat moved or changed its running schedule: %+v %v", row, err)
	}
	entries, err := h.db.GetQueue(t.Context(), id)
	if err != nil || len(entries) != 1 || !entries[0].Completed {
		t.Fatalf("renaming a chat changed participants: %+v %v", entries, err)
	}
	if !strings.Contains(lastMenuText(t, api), "Локальное имя") {
		t.Fatal("chat card did not display the saved alias")
	}
}

func TestScheduleCallbacksAndTextWritersCannotChangeAnotherSelectedChat(t *testing.T) {
	h, telegram, _, _ := newChatEditor(t)
	id := addChatSchedule(t, h, -200, "Must remain")
	processTelegramUpdates(t, h, telegram, chatEditorMessage(h.developerID, 99, 0, "/edit_schedule"))
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, "SelectChat:-100"))
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, fmt.Sprintf("EditDescription:%d", id)))
	h.stateMu.RLock()
	state := h.userState[h.developerID].state
	h.stateMu.RUnlock()
	if state != "" {
		t.Fatalf("foreign schedule callback entered input mode: %q", state)
	}
	for _, edit := range []struct{ state, value string }{{"edit_start_time", "14:00"}, {"edit_end_time", "15:00"}, {"edit_thread_id", "99"}, {"edit_description", "Changed"}, {"edit_title", "Changed"}} {
		h.stateMu.Lock()
		session := h.userState[h.developerID]
		session.state, session.scheduleID = edit.state, id
		h.userState[h.developerID] = session
		h.stateMu.Unlock()
		processTelegramUpdates(t, h, telegram, chatEditorMessage(h.developerID, 99, 0, edit.value))
	}
	row, err := h.db.GetScheduleEntry(t.Context(), id)
	if err != nil || row.Title != "Must remain" || row.ThreadID != 7 || row.ThreadDescription != nil || row.StartTime.Hour() != 11 || row.EndTime.Hour() != 12 {
		t.Fatalf("a text writer changed a schedule from another chat: %+v %v", row, err)
	}
}

func TestLegacyUnassignedSchedulesRemainVisibleWithoutChatMetadataActions(t *testing.T) {
	h, telegram, api, _ := newChatEditor(t)
	id := addChatSchedule(t, h, 0, "Старая запись")
	processTelegramUpdates(t, h, telegram, chatEditorMessage(h.developerID, 99, 0, "/edit_schedule"))
	if !strings.Contains(lastMenuMarkup(t, api), "SelectChat:0") {
		t.Fatal("legacy schedules without a chat disappeared from the menu")
	}
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, "SelectChat:0"))
	if !strings.Contains(lastMenuMarkup(t, api), "Старая запись") {
		t.Fatal("virtual chat did not show its legacy schedule")
	}
	if strings.Contains(lastMenuText(t, api), "Старая запись") {
		t.Fatal("schedule titles are duplicated above their buttons")
	}
	markup := lastMenuMarkup(t, api)
	for _, forbidden := range []string{"AddNewSchedule:", "SetDefaultChat:", "RenameChat:"} {
		if strings.Contains(markup, forbidden) {
			t.Fatalf("virtual chat offers unsupported action %s", forbidden)
		}
	}
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, fmt.Sprintf("EditSchedule:%d", id)))
	if !strings.Contains(lastMenuText(t, api), "Старая запись") {
		t.Fatal("virtual chat could not open the existing schedule")
	}
}

func TestDeletingLastUnassignedScheduleReturnsToChatList(t *testing.T) {
	h, telegram, api, _ := newChatEditor(t)
	service := queue.NewService(t.Context(), h.db, h.settings, h)
	t.Cleanup(service.Stop)
	h.SetQueueService(service)
	id := addChatSchedule(t, h, 0, "Последняя старая запись")
	processTelegramUpdates(t, h, telegram, chatEditorMessage(h.developerID, 99, 0, "/edit_schedule"))
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, "SelectChat:0"))
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, fmt.Sprintf("EditSchedule:%d", id)))
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, fmt.Sprintf("DeleteEntry:%d", id)))
	if _, err := h.db.GetScheduleEntry(t.Context(), id); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("legacy schedule was not deleted: %v", err)
	}
	if text := lastMenuText(t, api); !strings.Contains(text, "Выберите чат") {
		t.Fatalf("deletion left the editor in a missing virtual chat: %q", text)
	}
	if strings.Contains(lastMenuMarkup(t, api), "SelectChat:0") {
		t.Fatal("the empty virtual chat remained in the chat list")
	}
	h.stateMu.RLock()
	session := h.userState[h.developerID]
	h.stateMu.RUnlock()
	if session.chatSelected || session.targetChatID != 0 || session.scheduleID != 0 || session.state != "" {
		t.Fatalf("returning to the list retained the deleted target or pending input: %+v", session)
	}
}

func TestSelectedChatMenuDoesNotTreatStorageFailureAsDisappearance(t *testing.T) {
	h, _, _, _ := newConfigTestHandler(t, "schedule")
	cause := errors.New("chat storage is unavailable")
	h.SetChatService(chats.NewService(&chatReadFailure{cause: cause}, nil))
	session := h.userState[h.developerID]
	session.targetChatID, session.chatSelected = -200, true
	h.userState[h.developerID] = session
	if _, _, err := h.selectedChatMenu(t.Context(), h.developerID); !errors.Is(err, cause) {
		t.Fatalf("storage failure was hidden as chat navigation: %v", err)
	}
	if current := h.userState[h.developerID]; !current.chatSelected || current.targetChatID != -200 {
		t.Fatalf("storage failure reset the selected chat: %+v", current)
	}
}

var _ chats.Store = (*chatReadFailure)(nil)

type chatReadFailure struct {
	chats.Store
	cause error
}

func (store *chatReadFailure) GetChat(context.Context, int64) (chats.Chat, error) {
	return chats.Chat{}, store.cause
}

func TestAddingChatUsesOnlyIDAndCancelsCommittedInput(t *testing.T) {
	h, telegram, api, _ := newChatEditor(t)
	processTelegramUpdates(t, h, telegram, chatEditorMessage(h.developerID, 99, 0, "/edit_schedule"))
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, "AddChat"), chatEditorMessage(h.developerID, 99, 0, "-300"))
	chat, err := h.chats.Get(t.Context(), -300)
	if err != nil || chat.Name != "Telegram name" {
		t.Fatalf("adding a group required or invented a manual name: %+v %v", chat, err)
	}
	if !strings.Contains(lastMenuText(t, api), "Telegram name") {
		t.Fatal("new chat was not selected after adding")
	}
	processTelegramUpdates(t, h, telegram, chatEditorMessage(h.developerID, 99, 0, "-400"))
	if _, err := h.chats.Get(t.Context(), -400); !errors.Is(err, chats.ErrNotFound) {
		t.Fatalf("pending add input survived successful registration: %v", err)
	}
}

func newChatEditor(t *testing.T) (*BotHandler, *bot.Bot, *telegramAdapterAPI, *chatTestScheduler) {
	t.Helper()
	h, telegram, api, _ := newTelegramAdapter(t)
	h.db = scheduleTestRepository(t)
	service := chats.NewService(h.db, chatTestReader{})
	for id, name := range map[int64]string{-100: "Первый чат", -200: "Второй чат"} {
		if err := service.Remember(t.Context(), id, name); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.Initialize(t.Context(), -100); err != nil {
		t.Fatal(err)
	}
	h.SetChatService(service)
	scheduler := &chatTestScheduler{}
	h.SetScheduler(scheduler)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := h.Shutdown(ctx); err != nil {
			t.Errorf("shutdown chat editor: %v", err)
		}
	})
	return h, telegram, api, scheduler
}

var _ chats.Reader = (*chatTestReader)(nil)

type chatTestReader struct{}

func (chatTestReader) ReadChat(_ context.Context, id int64) (chats.Info, error) {
	if id == -300 {
		return chats.Info{Title: "Telegram name"}, nil
	}
	return chats.Info{}, errors.New("metadata unavailable")
}

type instantCall struct {
	chat   int64
	thread int
	title  string
}

var _ SchedulerManager = (*chatTestScheduler)(nil)

type chatTestScheduler struct {
	mu    sync.Mutex
	calls []instantCall
}

func (*chatTestScheduler) ScheduleNext(db.Schedule) {}
func (*chatTestScheduler) RemoveSchedule(int)       {}
func (*chatTestScheduler) Reconfigure() error       { return nil }
func (scheduler *chatTestScheduler) RunInstant(_ context.Context, chat int64, thread int, title string) error {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	scheduler.calls = append(scheduler.calls, instantCall{chat, thread, title})
	return nil
}
func (scheduler *chatTestScheduler) instantCalls() []instantCall {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	return append([]instantCall(nil), scheduler.calls...)
}

func chatEditorMessage(userID, chatID int64, threadID int, text string) *models.Update {
	message := adapterMessage(userID, text)
	message.Chat.ID, message.MessageThreadID = chatID, threadID
	if chatID < 0 {
		message.Chat.Title = "Группа с меню"
	}
	return &models.Update{Message: message}
}

func chatEditorCallback(h *BotHandler, userID int64, data string) *models.Update {
	h.stateMu.RLock()
	session := h.userState[userID]
	h.stateMu.RUnlock()
	h.editMu.RLock()
	messageID := h.editMessages[userID]
	h.editMu.RUnlock()
	message := &models.Message{ID: messageID, Chat: models.Chat{ID: session.chatID}, MessageThreadID: session.threadID}
	return &models.Update{CallbackQuery: &models.CallbackQuery{ID: data, From: models.User{ID: userID}, Data: data, Message: models.MaybeInaccessibleMessage{Message: message}}}
}

func lastMenuText(t *testing.T, api *telegramAdapterAPI) string {
	t.Helper()
	requests := api.history()
	for index := len(requests) - 1; index >= 0; index-- {
		if requests[index].method == "sendMessage" || requests[index].method == "editMessageText" {
			return requests[index].values["text"]
		}
	}
	t.Fatal("no menu was sent")
	return ""
}

func lastMenuMarkup(t *testing.T, api *telegramAdapterAPI) string {
	t.Helper()
	requests := api.history()
	for index := len(requests) - 1; index >= 0; index-- {
		if requests[index].values["reply_markup"] != "" {
			return requests[index].values["reply_markup"]
		}
	}
	t.Fatal("no menu keyboard was sent")
	return ""
}

func addChatSchedule(t *testing.T, h *BotHandler, chatID int64, title string) int {
	t.Helper()
	start, _ := time.Parse("15:04", "11:00")
	end, _ := time.Parse("15:04", "12:30")
	id, err := h.db.AddNewScheduleEntry(t.Context(), db.Schedule{ChatID: chatID, Title: title, ThreadID: 7, DayOfWeek: "monday", WeekType: "odd", StartTime: start, EndTime: end})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
