package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"queuebot/access"
	"queuebot/queue"
	"queuebot/settings"

	"github.com/go-telegram/bot/models"
)

func TestModerationSDKModeCapturesDurationRejectsStaleButtonsAndResets(t *testing.T) {
	h, telegram, api := newModerationAdapter(t)
	ref := openModerationQueue(t, h, -100, "Режим удаления")
	for position := 1; position <= 3; position++ {
		if _, err := h.queues.Join(t.Context(), ref, queue.Actor{ID: int64(100 + position), DisplayName: fmt.Sprintf("Студент %d", position)}, position); err != nil {
			t.Fatal(err)
		}
	}
	openModerationSelection(t, h, telegram, ref.ScheduleID)
	if request := moderationMenuRequest(t, api); !strings.Contains(request.values["reply_markup"], "Удаление: без блокировки") {
		t.Fatalf("new menu enabled punishment: %+v", request)
	}
	oldUnblocked := moderationButtons(t, api, "ModerationRemove:")[0]
	toggle := moderationButtons(t, api, "ModerationMode:")[0]
	processTelegramUpdates(t, h, telegram,
		chatEditorCallback(h, h.developerID, toggle),
		chatEditorCallback(h, h.developerID, toggle),
		chatEditorCallback(h, h.developerID, oldUnblocked))
	entries, err := h.db.GetQueue(t.Context(), ref.ScheduleID)
	if err != nil || len(entries) != 3 {
		t.Fatalf("stale unblocked button removed or punished a participant: %+v err=%v", entries, err)
	}
	if request := moderationMenuRequest(t, api); !strings.Contains(request.values["reply_markup"], "Удаление: с блокировкой на 3 мин") {
		t.Fatalf("replayed explicit mode inverted selection: %+v", request)
	}
	if err := h.settings.Update(t.Context(), func(values *settings.Values) error {
		values.ModerationBlockDuration = 5 * time.Minute
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	blockedButtons := moderationButtons(t, api, "ModerationRemove:")
	processTelegramUpdates(t, h, telegram,
		chatEditorCallback(h, h.developerID, blockedButtons[0]),
		chatEditorCallback(h, h.developerID, blockedButtons[1]))
	for _, userID := range []int64{101, 102} {
		if _, err := h.queues.Join(t.Context(), ref, queue.Actor{ID: userID}, 0); err == nil {
			t.Fatalf("participant %d rejoined despite enabled block", userID)
		}
	}
	h.stateMu.RLock()
	session := h.userState[h.developerID]
	h.stateMu.RUnlock()
	blocks, err := h.queues.Blocks(t.Context(), session.moderationRun, h.developerID)
	if err != nil || len(blocks) != 2 {
		t.Fatalf("quick clicks did not create both blocks: %+v err=%v", blocks, err)
	}
	for _, block := range blocks {
		remaining := time.Until(block.BlockedUntil)
		if remaining < 2*time.Minute+50*time.Second || remaining > 3*time.Minute+time.Second {
			t.Fatalf("configuration silently changed rendered duration: %s", remaining)
		}
	}
	waitModerationMenu(t, api, func(request telegramAdapterRequest) bool {
		return !strings.Contains(request.values["reply_markup"], "Студент 1") && !strings.Contains(request.values["reply_markup"], "Студент 2") && strings.Contains(request.values["reply_markup"], "Удаление: с блокировкой на 3 мин")
	})
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, moderationButtons(t, api, "ModerationMode:")[0]), chatEditorCallback(h, h.developerID, blockedButtons[2]))
	entries, err = h.db.GetQueue(t.Context(), ref.ScheduleID)
	if err != nil || len(entries) != 1 || entries[0].UserID != 103 {
		t.Fatalf("stale blocked button punished a participant after disabling: %+v err=%v", entries, err)
	}
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, moderationButtons(t, api, "ModerationRemove:")[0]))
	if _, err := h.queues.Join(t.Context(), ref, queue.Actor{ID: 103}, 1); err != nil {
		t.Fatalf("disabled mode prevented rejoin: %v", err)
	}
	openModerationSelection(t, h, telegram, ref.ScheduleID)
	h.stateMu.RLock()
	session = h.userState[h.developerID]
	h.stateMu.RUnlock()
	if session.moderationBlockEnabled || session.moderationBlockDuration != 5*time.Minute {
		t.Fatalf("new session did not reset mode or capture current config: %+v", session)
	}
}

func TestModerationSDKUnblockUsesCurrentGenerationAndReturnsToParticipants(t *testing.T) {
	h, telegram, api := newModerationAdapter(t)
	ref := openModerationQueue(t, h, -100, "Снятие блокировки")
	if _, err := h.queues.Join(t.Context(), ref, queue.Actor{ID: 101, Username: "alice", DisplayName: "Алиса"}, 1); err != nil {
		t.Fatal(err)
	}
	openModerationSelection(t, h, telegram, ref.ScheduleID)
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, moderationButtons(t, api, "ModerationMode:")[0]))
	remove := moderationButtons(t, api, "ModerationRemove:")[0]
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, remove), chatEditorCallback(h, h.developerID, "ModerationBlocks"))
	if request := moderationMenuRequest(t, api); !strings.Contains(request.values["reply_markup"], "@alice") || strings.Contains(request.values["reply_markup"], "ModerationRemove:") {
		t.Fatalf("blocked participant list is missing or mixed with removal actions: %+v", request)
	}
	unblock := moderationButtons(t, api, "ModerationUnblock:")[0]
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, unblock))
	if request := moderationMenuRequest(t, api); !strings.Contains(request.values["text"], "Действующих блокировок нет") {
		t.Fatalf("unblocked participant remains in live list: %+v", request)
	}
	if _, err := h.queues.Join(t.Context(), ref, queue.Actor{ID: 101, Username: "alice"}, 1); err != nil {
		t.Fatalf("unblocked participant could not rejoin: %v", err)
	}
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, "ModerationParticipants"))
	if request := moderationMenuRequest(t, api); !strings.Contains(request.values["reply_markup"], "Удаление: с блокировкой на 3 мин") || !strings.Contains(request.values["reply_markup"], "@alice") {
		t.Fatalf("return lost participant or selected mode: %+v", request)
	}
	processTelegramUpdates(t, h, telegram,
		chatEditorCallback(h, h.developerID, moderationButtons(t, api, "ModerationRemove:")[0]),
		chatEditorCallback(h, h.developerID, "ModerationBlocks"),
		chatEditorCallback(h, h.developerID, unblock))
	if _, err := h.queues.Join(t.Context(), ref, queue.Actor{ID: 101}, 1); err == nil {
		t.Fatal("stale unblock removed a newer block generation")
	}
}

func TestModerationSDKUnblockRejectsForeignOwnerSourceAndRevokedPermission(t *testing.T) {
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
	h.queues.SetAuthorizer(permissions)
	ref := openModerationQueue(t, h, -100, "Приватные блокировки")
	if _, err := h.queues.Join(t.Context(), ref, queue.Actor{ID: 101, Username: "alice"}, 1); err != nil {
		t.Fatal(err)
	}
	processTelegramUpdates(t, h, telegram, sharedModerationCallback(moderator, ref))
	request := lastModerationEphemeralEdit(t, api, ref.ChatID)
	mode := privateModerationButton(t, request, "ModerationMode:")
	processTelegramUpdates(t, h, telegram, privateModerationCallback(h, moderator, mode))
	remove := privateModerationButton(t, lastModerationEphemeralEdit(t, api, ref.ChatID), "ModerationRemove:")
	processTelegramUpdates(t, h, telegram, privateModerationCallback(h, moderator, remove))
	processTelegramUpdates(t, h, telegram, privateModerationCallback(h, moderator, "m:"+h.MenuGeneration(moderator)+":ModerationBlocks"))
	unblock := privateModerationButton(t, lastModerationEphemeralEdit(t, api, ref.ChatID), "ModerationUnblock:")
	wrongOwner := privateModerationCallback(h, moderator, unblock)
	wrongOwner.CallbackQuery.From.ID = h.developerID
	wrongSource := privateModerationCallback(h, moderator, unblock)
	wrongSource.CallbackQuery.Message.Message.Chat.ID = -200
	wrongTopic := privateModerationCallback(h, moderator, unblock)
	wrongTopic.CallbackQuery.Message.Message.MessageThreadID++
	processTelegramUpdates(t, h, telegram, wrongOwner, wrongSource, wrongTopic)
	if _, err := h.queues.Join(t.Context(), ref, queue.Actor{ID: 101}, 1); err == nil {
		t.Fatal("foreign private callback removed a block")
	}
	if err := permissions.SetPermission(t.Context(), h.developerID, moderator, access.RemoveParticipants, false); err != nil {
		t.Fatal(err)
	}
	processTelegramUpdates(t, h, telegram, privateModerationCallback(h, moderator, unblock))
	if _, err := h.queues.Join(t.Context(), ref, queue.Actor{ID: 101}, 1); err == nil || errors.Is(err, queue.ErrForbidden) {
		t.Fatalf("revoked moderator changed block state: %v", err)
	}
	for _, call := range api.history() {
		if call.method == "sendMessage" && call.values["ephemeral_message_parameters"] == "" && strings.Contains(call.values["text"], "alice") {
			t.Fatal("participant block details escaped the private menu")
		}
	}
}

func TestModerationSDKBlockListPaginatesAndClampsAfterUnblock(t *testing.T) {
	h, telegram, api := newModerationAdapter(t)
	ref := openModerationQueue(t, h, -100, "Много блокировок")
	for position := 1; position <= 11; position++ {
		if _, err := h.queues.Join(t.Context(), ref, queue.Actor{ID: int64(100 + position), DisplayName: fmt.Sprintf("Студент %d", position)}, position); err != nil {
			t.Fatal(err)
		}
	}
	openModerationSelection(t, h, telegram, ref.ScheduleID)
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, moderationButtons(t, api, "ModerationMode:")[0]))
	var removals []*models.Update
	for _, callback := range moderationButtons(t, api, "ModerationRemove:") {
		removals = append(removals, chatEditorCallback(h, h.developerID, callback))
	}
	processTelegramUpdates(t, h, telegram, removals...)
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, "ModerationBlocks"))
	if callbacks := moderationButtons(t, api, "ModerationUnblock:"); len(callbacks) != 10 {
		t.Fatalf("first page has %d block buttons, want10", len(callbacks))
	}
	request := moderationMenuRequest(t, api)
	for _, label := range []string{"Первый чат", "Много блокировок", "Группа:", "Тема:", "Время:", "Страница 1 из 2"} {
		if !strings.Contains(request.values["text"], label) {
			t.Fatalf("blocked list lost queue context %q: %+v", label, request)
		}
	}
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, "ModerationBlockPage:1"))
	callbacks := moderationButtons(t, api, "ModerationUnblock:")
	if len(callbacks) != 1 {
		t.Fatalf("second page has %d block buttons, want1", len(callbacks))
	}
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, callbacks[0]))
	request = moderationMenuRequest(t, api)
	if strings.Contains(request.values["text"], "Страница ") || len(moderationButtons(t, api, "ModerationUnblock:")) != 10 {
		t.Fatalf("unblocking did not clamp the removed last page: %+v", request)
	}
}

func TestModerationSDKBlockListAutomaticallyDropsExpiredBlocks(t *testing.T) {
	h, telegram, api := newModerationAdapter(t)
	if err := h.settings.Update(t.Context(), func(values *settings.Values) error {
		values.ModerationBlockDuration = time.Second
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ref := openModerationQueue(t, h, -100, "Истекающая блокировка")
	if _, err := h.queues.Join(t.Context(), ref, queue.Actor{ID: 101, Username: "alice"}, 1); err != nil {
		t.Fatal(err)
	}
	openModerationSelection(t, h, telegram, ref.ScheduleID)
	processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, moderationButtons(t, api, "ModerationMode:")[0]))
	processTelegramUpdates(t, h, telegram,
		chatEditorCallback(h, h.developerID, moderationButtons(t, api, "ModerationRemove:")[0]),
		chatEditorCallback(h, h.developerID, "ModerationBlocks"))
	if !strings.Contains(moderationMenuRequest(t, api).values["reply_markup"], "@alice") {
		t.Fatal("active block is missing from the menu")
	}
	waitModerationMenu(t, api, func(request telegramAdapterRequest) bool {
		return strings.Contains(request.values["text"], "Действующих блокировок нет") && !strings.Contains(request.values["reply_markup"], "ModerationUnblock:")
	})
	if _, err := h.queues.Join(t.Context(), ref, queue.Actor{ID: 101}, 1); err != nil {
		t.Fatalf("expired block prevented rejoin: %v", err)
	}
}

func moderationButtons(t *testing.T, api *telegramAdapterAPI, prefix string) []string {
	t.Helper()
	var result []string
	for _, row := range moderationMenuMarkup(t, api).InlineKeyboard {
		for _, button := range row {
			if strings.HasPrefix(button.CallbackData, prefix) {
				result = append(result, button.CallbackData)
			}
		}
	}
	if len(result) == 0 {
		t.Fatalf("menu has no %q buttons", prefix)
	}
	return result
}

func privateModerationButton(t *testing.T, request telegramAdapterRequest, prefix string) string {
	t.Helper()
	var markup models.InlineKeyboardMarkup
	if err := json.Unmarshal([]byte(request.values["reply_markup"]), &markup); err != nil {
		t.Fatal(err)
	}
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			if strings.HasPrefix(menuCallbackData(button.CallbackData), prefix) {
				return button.CallbackData
			}
		}
	}
	t.Fatalf("private menu has no %q button: %+v", prefix, request)
	return ""
}
