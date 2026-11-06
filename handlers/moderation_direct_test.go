package handlers

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"

	"queuebot/queue"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func TestModerationSDKSharedButtonOpensItsOwnQueueAndUsesUsernames(t *testing.T) {
	h, telegram, api := newModerationAdapter(t)
	first := openModerationQueue(t, h, -100, "Первая работа")
	second := openModerationQueue(t, h, -200, "Вторая работа")
	for _, actor := range []struct {
		ref      queue.Reference
		actor    queue.Actor
		position int
	}{
		{first, queue.Actor{ID: 101, Username: "alice", DisplayName: "Алиса"}, 1},
		{second, queue.Actor{ID: 202, Username: "bob", DisplayName: "Борис"}, 1},
		{second, queue.Actor{ID: 303, DisplayName: "Вера без username"}, 2},
	} {
		if _, err := h.queues.Join(t.Context(), actor.ref, actor.actor, actor.position); err != nil {
			t.Fatal(err)
		}
	}
	for _, target := range []struct {
		ref          queue.Reference
		title, label string
		legacy       bool
	}{
		{first, "Первая работа", "@alice", false},
		{second, "Вторая работа", "@bob", false},
		{second, "Вторая работа", "@bob", true},
	} {
		update := sharedModerationCallback(h.developerID, target.ref)
		if target.legacy {
			update.CallbackQuery.Data = "ManageQueue"
		}
		processTelegramUpdates(t, h, telegram, update)
		h.stateMu.RLock()
		session := h.userState[h.developerID]
		h.stateMu.RUnlock()
		schedule, err := h.db.GetScheduleEntry(t.Context(), target.ref.ScheduleID)
		if err != nil || !session.moderationSelected || session.moderationRun.ScheduleID != target.ref.ScheduleID || session.moderationRun.ChatID != target.ref.ChatID || !session.moderationRun.OpensAt.Equal(*schedule.ActiveOpensAt) {
			t.Fatalf("shared button opened selector or another run: session=%+v err=%v", session, err)
		}
		request := lastModerationEphemeralEdit(t, api, target.ref.ChatID)
		if !strings.Contains(request.values["text"], target.title) || strings.Contains(request.values["reply_markup"], "ModerationSelect:") || !strings.Contains(request.values["reply_markup"], target.label) {
			t.Fatalf("shared button did not immediately display its participants: %+v", request)
		}
		if target.ref.ScheduleID == second.ScheduleID && (strings.Contains(request.values["reply_markup"], "Борис") || !strings.Contains(request.values["reply_markup"], "Вера без username")) {
			t.Fatalf("participant names do not prefer username with display-name fallback: %+v", request)
		}
		var parameters models.EphemeralMessageParameters
		for _, call := range api.history() {
			if call.method == "sendMessage" && call.values["ephemeral_message_parameters"] != "" {
				if err := json.Unmarshal([]byte(call.values["ephemeral_message_parameters"]), &parameters); err != nil {
					t.Fatal(err)
				}
			}
		}
		if parameters.ReceiverUserID != h.developerID || parameters.CallbackQueryID != update.CallbackQuery.ID || parameters.ReplaceCallbackQueryMessage {
			t.Fatalf("shared queue was not left behind a separate private overlay: %+v", parameters)
		}
	}
	var markup models.InlineKeyboardMarkup
	if err := json.Unmarshal([]byte(lastModerationEphemeralEdit(t, api, second.ChatID).values["reply_markup"]), &markup); err != nil {
		t.Fatal(err)
	}
	processTelegramUpdates(t, h, telegram, privateModerationCallback(h, h.developerID, markup.InlineKeyboard[0][0].CallbackData))
	firstEntries, err := h.db.GetQueue(t.Context(), first.ScheduleID)
	if err != nil {
		t.Fatal(err)
	}
	secondEntries, err := h.db.GetQueue(t.Context(), second.ScheduleID)
	if err != nil || len(firstEntries) != 1 || firstEntries[0].UserID != 101 || len(secondEntries) != 1 || secondEntries[0].UserID != 303 {
		t.Fatalf("direct menu removal affected another queue: first=%+v second=%+v err=%v", firstEntries, secondEntries, err)
	}
	processTelegramUpdates(t, h, telegram, privateModerationCallback(h, h.developerID, "m:"+h.MenuGeneration(h.developerID)+":ModerationBack"))
	h.stateMu.RLock()
	session := h.userState[h.developerID]
	h.stateMu.RUnlock()
	if session.moderationSelected || session.moderationPage != 0 || len(session.moderationRuns) != 2 {
		t.Fatalf("back did not restore all open queues: %+v", session)
	}
}

func TestModerationSDKSharedButtonRejectsForeignStaleAndClosedSource(t *testing.T) {
	h, telegram, api := newModerationAdapter(t)
	ref := openModerationQueue(t, h, -100, "Проверяем источник")
	other := openModerationQueue(t, h, -200, "Другая очередь")
	wrongChat := ref
	wrongChat.ChatID = other.ChatID
	wrongMessage := ref
	wrongMessage.MessageID = other.MessageID
	for _, source := range []queue.Reference{wrongChat, wrongMessage} {
		assertModerationOpenerRejected(t, h, telegram, api, sharedModerationCallback(h.developerID, source))
	}
	malformed := sharedModerationCallback(h.developerID, ref)
	malformed.CallbackQuery.Data = "ManageQueue:invalid"
	assertModerationOpenerRejected(t, h, telegram, api, malformed)
	malformed = sharedModerationCallback(h.developerID, ref)
	malformed.CallbackQuery.Data += ":unexpected"
	assertModerationOpenerRejected(t, h, telegram, api, malformed)
	if err := h.queues.Repost(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	assertModerationOpenerRejected(t, h, telegram, api, sharedModerationCallback(h.developerID, ref))
	schedule, err := h.db.GetScheduleEntry(t.Context(), ref.ScheduleID)
	if err != nil || schedule.QueueMesID == nil {
		t.Fatal("missing reposted queue")
	}
	ref.MessageID = *schedule.QueueMesID
	if err := h.queues.Close(t.Context(), ref.ScheduleID); err != nil {
		t.Fatal(err)
	}
	assertModerationOpenerRejected(t, h, telegram, api, sharedModerationCallback(h.developerID, ref))
}

func TestModerationSDKPagesKeepLongMetadataReadableAndClampAfterClosing(t *testing.T) {
	h, telegram, api := newModerationAdapter(t)
	var refs []queue.Reference
	titles := make(map[string]bool)
	for index := 0; index < 12; index++ {
		title := fmt.Sprintf("Работа %d", index+1)
		description := "Тема"
		group := fmt.Sprintf("Группа %d", index+1)
		if index < 6 {
			title = strings.Repeat("🎓", 170) + fmt.Sprintf(" КОНЕЦ_РАБОТЫ_%d", index+1)
			description = strings.Repeat("🧪", 480) + fmt.Sprintf(" КОНЕЦ_ТЕМЫ_%d", index+1)
			group = strings.Repeat("🏫", 175) + fmt.Sprintf(" КОНЕЦ_ГРУППЫ_%d", index+1)
		}
		ref := openModerationQueue(t, h, -1000-int64(index), title)
		if err := h.db.ChangeDescription(t.Context(), ref.ScheduleID, description); err != nil {
			t.Fatal(err)
		}
		if err := h.db.EnsureChat(t.Context(), ref.ChatID, group); err != nil {
			t.Fatal(err)
		}
		refs = append(refs, ref)
		titles[title] = false
	}
	processTelegramUpdates(t, h, telegram, chatEditorMessage(h.developerID, 99, 0, "/manage_queue"))
	visited := make(map[string]bool)
	var sawFive bool
	for attempts := 0; attempts < len(refs); attempts++ {
		request := moderationMenuRequest(t, api)
		if units := len(utf16.Encode([]rune(request.values["text"]))); units > 3500 {
			t.Fatalf("page exceeds message budget: %d UTF-16 units", units)
		}
		for title := range titles {
			if strings.Contains(request.values["text"], title) {
				titles[title] = true
			}
		}
		markup := moderationMenuMarkup(t, api)
		var next string
		count := 0
		for _, row := range markup.InlineKeyboard {
			for _, button := range row {
				if strings.HasPrefix(button.CallbackData, "ModerationSelect:") {
					count++
					if !strings.HasPrefix(button.Text, "Открыть №") || len([]rune(button.Text)) > 15 {
						t.Fatalf("selector still contains clipped metadata: %+v", button)
					}
					visited[button.CallbackData] = true
				}
				if button.Text == "Далее →" {
					next = button.CallbackData
				}
			}
		}
		if count > 5 {
			t.Fatalf("too many queue blocks on one page: %d", count)
		}
		sawFive = sawFive || count == 5
		h.stateMu.RLock()
		allRefs := h.userState[h.developerID].moderationRuns
		h.stateMu.RUnlock()
		if len(allRefs) != len(refs) {
			t.Fatalf("pagination dropped structured refs: %+v", allRefs)
		}
		if next == "" {
			break
		}
		processTelegramUpdates(t, h, telegram, chatEditorCallback(h, h.developerID, next))
	}
	if len(visited) != len(refs) || !sawFive {
		t.Fatalf("pagination lost queues or did not pack short entries: visited=%d five=%t", len(visited), sawFive)
	}
	for title, seen := range titles {
		if !seen {
			t.Fatalf("full queue title was clipped: %q", title)
		}
	}
	for _, ref := range refs[1:] {
		if err := h.queues.Close(t.Context(), ref.ScheduleID); err != nil {
			t.Fatal(err)
		}
	}
	waitModerationMenu(t, api, func(request telegramAdapterRequest) bool {
		return strings.Contains(request.values["text"], "КОНЕЦ_РАБОТЫ_1") && !strings.Contains(request.values["text"], "Страница ")
	})
	h.stateMu.RLock()
	page := h.userState[h.developerID].moderationPage
	h.stateMu.RUnlock()
	if page != 0 {
		t.Fatalf("live page did not clamp after closing queues: %d", page)
	}
}

func sharedModerationCallback(userID int64, ref queue.Reference) *models.Update {
	message := &models.Message{ID: ref.MessageID, Chat: models.Chat{ID: ref.ChatID, Title: "Telegram group"}, MessageThreadID: 7}
	return &models.Update{CallbackQuery: &models.CallbackQuery{ID: fmt.Sprintf("manage-%d", ref.ScheduleID), From: models.User{ID: userID}, Data: fmt.Sprintf("ManageQueue:%d", ref.ScheduleID), Message: models.MaybeInaccessibleMessage{Message: message}}}
}

func privateModerationCallback(h *BotHandler, userID int64, data string) *models.Update {
	h.menusMu.Lock()
	ref := h.menus[userID].ref
	h.menusMu.Unlock()
	message := &models.Message{ID: ref.MessageID, EphemeralMessageID: ref.EphemeralID, Chat: models.Chat{ID: ref.ChatID}, MessageThreadID: ref.ThreadID}
	return menuTestCallback(userID, message, data)
}

func lastModerationEphemeralEdit(t *testing.T, api *telegramAdapterAPI, chatID int64) telegramAdapterRequest {
	t.Helper()
	requests := api.history()
	for index := len(requests) - 1; index >= 0; index-- {
		request := requests[index]
		if request.method == "editEphemeralMessageText" && request.values["chat_id"] == strconv.FormatInt(chatID, 10) {
			return request
		}
	}
	t.Fatal("participant menu was not displayed in its source group")
	return telegramAdapterRequest{}
}

func assertModerationOpenerRejected(t *testing.T, h *BotHandler, telegram *bot.Bot, api *telegramAdapterAPI, update *models.Update) {
	t.Helper()
	before := len(api.history())
	processTelegramUpdates(t, h, telegram, update)
	for _, request := range api.history()[before:] {
		if request.method == "sendMessage" || request.method == "editEphemeralMessageText" {
			t.Fatalf("invalid source opened a management menu: %+v", request)
		}
	}
	h.stateMu.RLock()
	_, live := h.userState[h.developerID]
	h.stateMu.RUnlock()
	if live {
		t.Fatal("invalid source created an authorized management session")
	}
}
