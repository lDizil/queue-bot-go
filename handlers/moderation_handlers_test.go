package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"queuebot/access"
	"queuebot/queue"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func TestModerationSDKListsOpenRunsAcrossChatsWithStructuredSelection(t *testing.T) {
	h, telegram, api := newModerationAdapter(t)
	first := openModerationQueue(t, h, -100, "Первая работа")
	second := openModerationQueue(t, h, -500, "Вторая работа")
	if err := h.db.ChangeDescription(t.Context(), first.ScheduleID, "Лабораторная"); err != nil {
		t.Fatal(err)
	}
	pending := addChatSchedule(t, h, -200, "Ещё не открыта")
	now := time.Now()
	if err := h.queues.Prepare(t.Context(), pending, now.Add(time.Hour), now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	expired := addChatSchedule(t, h, -400, "Уже закрыта")
	if err := h.queues.Prepare(t.Context(), expired, now.Add(-2*time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	processTelegramUpdates(t, h, telegram, chatEditorMessage(h.developerID, 99, 0, "/manage_queue@QueueBot"))
	request := moderationMenuRequest(t, api)
	for _, label := range []string{"Первый чат", "Лабораторная", "Первая работа", "Группа: ID -500", "Вторая работа", "МСК"} {
		if !strings.Contains(request.values["text"], label) {
			t.Fatalf("queue selector lost %q: %+v", label, request)
		}
	}
	for _, label := range []string{"Ещё не открыта", "Уже закрыта"} {
		if strings.Contains(request.values["text"], label) {
			t.Fatalf("selector includes closed run %q", label)
		}
	}
	h.stateMu.RLock()
	refs := h.userState[h.developerID].moderationRuns
	h.stateMu.RUnlock()
	if len(refs) != 2 || refs[0].ScheduleID != first.ScheduleID || refs[1].ScheduleID != second.ScheduleID || refs[1].ChatID != -500 || refs[1].OpensAt.IsZero() {
		t.Fatalf("selection did not retain structured run references: %+v", refs)
	}
	for _, ref := range refs {
		if len("m:0123456789abcdef:"+moderationSelectData(ref)) > 64 {
			t.Fatal("selector exceeds Telegram callback limit")
		}
	}
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, moderationSelectData(refs[1])))
	h.stateMu.RLock()
	selected := h.userState[h.developerID]
	h.stateMu.RUnlock()
	if !selected.moderationSelected || selected.moderationRun.ScheduleID != second.ScheduleID || selected.moderationRun.ChatID != -500 {
		t.Fatalf("selection inferred target from UI location: %+v", selected)
	}
	if request := moderationMenuRequest(t, api); !strings.Contains(request.values["text"], "Вторая работа") || request.values["chat_id"] != "99" {
		t.Fatalf("selected menu moved from source or opened another run: %+v", request)
	}
}

func TestModerationSDKFourQuickButtonsAndStaleRejoin(t *testing.T) {
	h, telegram, api := newModerationAdapter(t)
	ref := openModerationQueue(t, h, -100, "Практика")
	for position := 1; position <= 5; position++ {
		if _, err := h.queues.Join(t.Context(), ref, queue.Actor{ID: int64(position + 100), DisplayName: fmt.Sprintf("Студент %d", position)}, position); err != nil {
			t.Fatal(err)
		}
	}
	openModerationSelection(t, h, telegram, ref.ScheduleID)
	markup := moderationMenuMarkup(t, api)
	var callbacks []*models.Update
	var oldAlice string
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			if strings.HasPrefix(button.CallbackData, "ModerationRemove:") && len(callbacks) < 4 {
				callbacks = append(callbacks, chatEditorCallback(h, h.developerID, button.CallbackData))
				if oldAlice == "" {
					oldAlice = button.CallbackData
				}
			}
		}
	}
	if len(callbacks) != 4 {
		t.Fatalf("menu did not provide occupied entry buttons: %+v", markup)
	}
	processTelegramUpdates(t, h, telegram, callbacks...)
	entries, err := h.db.GetQueue(t.Context(), ref.ScheduleID)
	if err != nil || len(entries) != 1 || entries[0].UserID != 105 || entries[0].Position != 5 {
		t.Fatalf("quick buttons lost their original participants: %+v err=%v", entries, err)
	}
	if _, err := h.queues.Join(t.Context(), ref, queue.Actor{ID: 101, DisplayName: "Студент 1 снова"}, 1); err != nil {
		t.Fatalf("removed participant could not rejoin: %v", err)
	}
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, oldAlice))
	entries, err = h.db.GetQueue(t.Context(), ref.ScheduleID)
	if err != nil || len(entries) != 2 || entries[0].UserID != 101 || entries[1].UserID != 105 {
		t.Fatalf("stale SDK callback removed rejoined person: %+v err=%v", entries, err)
	}
}

func TestModerationSDKLiveMenuTracksMutationsAndRemovesClosedRunButtons(t *testing.T) {
	h, telegram, api := newModerationAdapter(t)
	ref := openModerationQueue(t, h, -100, "Живая очередь")
	openModerationSelection(t, h, telegram, ref.ScheduleID)
	if _, err := h.queues.Join(t.Context(), ref, queue.Actor{ID: 101, DisplayName: "Алиса"}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := h.queues.Join(t.Context(), ref, queue.Actor{ID: 202, DisplayName: "Борис"}, 2); err != nil {
		t.Fatal(err)
	}
	waitModerationMenu(t, api, func(request telegramAdapterRequest) bool {
		return strings.Contains(request.values["reply_markup"], "1. Алиса") && strings.Contains(request.values["reply_markup"], "2. Борис")
	})
	updates := moderationEditCount(api)
	time.Sleep(1100 * time.Millisecond)
	if got := moderationEditCount(api); got != updates {
		t.Fatalf("unchanged menu caused repeated HTTP edits: before=%d after=%d", updates, got)
	}
	alice, err := h.db.GetQueueEntryByUser(t.Context(), 101, ref.ScheduleID)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := h.db.GetQueueEntryByUser(t.Context(), 202, ref.ScheduleID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.queues.Complete(t.Context(), ref, queue.Actor{ID: 101}, 0, 0, nil, "live-complete"); err != nil {
		t.Fatal(err)
	}
	if err := h.queues.Swap(t.Context(), ref, alice.ID, bob.ID, 1, 2); err != nil {
		t.Fatal(err)
	}
	waitModerationMenu(t, api, func(request telegramAdapterRequest) bool {
		return strings.Contains(request.values["reply_markup"], "2. Алиса ✅") && strings.Contains(request.values["reply_markup"], "1. Борис")
	})
	if err := h.queues.Repost(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	schedule, err := h.db.GetScheduleEntry(t.Context(), ref.ScheduleID)
	if err != nil || schedule.QueueMesID == nil {
		t.Fatal("missing reposted queue")
	}
	ref.MessageID = *schedule.QueueMesID
	if err := h.queues.Leave(t.Context(), ref, queue.Actor{ID: 202}); err != nil {
		t.Fatal(err)
	}
	waitModerationMenu(t, api, func(request telegramAdapterRequest) bool {
		return strings.Contains(request.values["reply_markup"], "Алиса") && !strings.Contains(request.values["reply_markup"], "Борис")
	})
	if err := h.queues.Close(t.Context(), ref.ScheduleID); err != nil {
		t.Fatal(err)
	}
	waitModerationMenu(t, api, func(request telegramAdapterRequest) bool {
		return strings.Contains(request.values["text"], "очередь закрыта") && !strings.Contains(request.values["reply_markup"], "ModerationRemove:")
	})
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, "ModerationBack"))
	if request := moderationMenuRequest(t, api); !strings.Contains(request.values["text"], "нет открытых очередей") {
		t.Fatalf("closed run is still selectable: %+v", request)
	}
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, "ModerationLeave"))
	h.stateMu.RLock()
	_, live := h.userState[h.developerID]
	h.stateMu.RUnlock()
	if live {
		t.Fatal("closing management retained the active session")
	}
}

func newModerationAdapter(t *testing.T) (*BotHandler, *bot.Bot, *telegramAdapterAPI) {
	t.Helper()
	h, telegram, api, _ := newChatEditor(t)
	service := queue.NewService(t.Context(), h.db, h.settings, h)
	service.SetAuthorizer(moderationTestAuthorizer{handler: h})
	h.SetQueueService(service)
	t.Cleanup(service.Stop)
	return h, telegram, api
}

type moderationTestAuthorizer struct{ handler *BotHandler }

func (authorizer moderationTestAuthorizer) Allowed(ctx context.Context, id int64, permission access.Permission) (bool, error) {
	return authorizer.handler.allowed(ctx, id, permission), nil
}

func openModerationQueue(t *testing.T, h *BotHandler, chatID int64, title string) queue.Reference {
	t.Helper()
	id := addChatSchedule(t, h, chatID, title)
	now := time.Now()
	if err := h.queues.Prepare(t.Context(), id, now.Add(-time.Minute), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := h.queues.Refresh(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	schedule, err := h.db.GetScheduleEntry(t.Context(), id)
	if err != nil || schedule.QueueMesID == nil {
		t.Fatal("queue did not publish")
	}
	return queue.Reference{ScheduleID: id, ChatID: chatID, MessageID: *schedule.QueueMesID}
}

func openModerationSelection(t *testing.T, h *BotHandler, telegram *bot.Bot, scheduleID int) {
	t.Helper()
	processTelegramUpdates(t, h, telegram, chatEditorMessage(h.developerID, 99, 0, "/manage_queue"))
	h.stateMu.RLock()
	refs := h.userState[h.developerID].moderationRuns
	h.stateMu.RUnlock()
	for _, ref := range refs {
		if ref.ScheduleID == scheduleID {
			processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, moderationSelectData(ref)))
			return
		}
	}
	t.Fatalf("open queue %d is missing from management", scheduleID)
}

func moderationMenuRequest(t *testing.T, api *telegramAdapterAPI) telegramAdapterRequest {
	t.Helper()
	requests := api.history()
	for index := len(requests) - 1; index >= 0; index-- {
		request := requests[index]
		if request.values["chat_id"] == "99" && (request.method == "sendMessage" || request.method == "editMessageText") {
			return request
		}
	}
	t.Fatal("management menu was not sent to its source chat")
	return telegramAdapterRequest{}
}

func moderationMenuMarkup(t *testing.T, api *telegramAdapterAPI) models.InlineKeyboardMarkup {
	t.Helper()
	var markup models.InlineKeyboardMarkup
	if err := json.Unmarshal([]byte(moderationMenuRequest(t, api).values["reply_markup"]), &markup); err != nil {
		t.Fatal(err)
	}
	return markup
}

func waitModerationMenu(t *testing.T, api *telegramAdapterAPI, predicate func(telegramAdapterRequest) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if predicate(moderationMenuRequest(t, api)) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("management menu did not refresh: %+v", moderationMenuRequest(t, api))
}

func moderationEditCount(api *telegramAdapterAPI) int {
	count := 0
	for _, request := range api.history() {
		if request.values["chat_id"] == "99" && request.method == "editMessageText" {
			count++
		}
	}
	return count
}

func TestModerationSDKRevokedPermissionClosesLiveMenu(t *testing.T) {
	h, telegram, api := newModerationAdapter(t)
	permissions := access.NewService(h.db, h.developerID)
	if err := permissions.Initialize(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	const moderator = int64(777)
	if err := permissions.AddAdmin(t.Context(), h.developerID, moderator); err != nil {
		t.Fatal(err)
	}
	if err := permissions.SetPermission(t.Context(), h.developerID, moderator, access.RemoveParticipants, true); err != nil {
		t.Fatal(err)
	}
	h.access = permissions
	ref := openModerationQueue(t, h, -100, "Отзыв доступа")
	if _, err := h.queues.Join(t.Context(), ref, queue.Actor{ID: 101, DisplayName: "Алиса"}, 1); err != nil {
		t.Fatal(err)
	}
	processTelegramUpdates(t, h, telegram, chatEditorMessage(moderator, 99, 0, "/manage_queue"))
	h.stateMu.RLock()
	refs := h.userState[moderator].moderationRuns
	h.stateMu.RUnlock()
	if len(refs) != 1 {
		t.Fatalf("moderator did not open queue selection: %+v", refs)
	}
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, moderator, moderationSelectData(refs[0])))
	stale := chatEditorCallback(h, moderator, moderationMenuMarkup(t, api).InlineKeyboard[0][0].CallbackData)
	if err := permissions.SetPermission(t.Context(), h.developerID, moderator, access.RemoveParticipants, false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	live := true
	for time.Now().Before(deadline) {
		h.stateMu.RLock()
		_, live = h.userState[moderator]
		h.stateMu.RUnlock()
		if !live {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if live {
		t.Fatal("revoked permission retained live participant menu")
	}
	processTelegramUpdates(t, h, telegram, stale)
	entries, err := h.db.GetQueue(t.Context(), ref.ScheduleID)
	if err != nil || len(entries) != 1 || entries[0].UserID != 101 {
		t.Fatalf("revoked callback changed participants: %+v err=%v", entries, err)
	}
}
