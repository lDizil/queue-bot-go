package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func TestMenuTimeoutClosesStateBeforeDeletionAndRetries(t *testing.T) {
	h, b, api := newMenuTestHandler(t)
	message, err := h.sendMenu(t.Context(), b, menuTestSource(h.developerID, -100), "config", "Private fields", menuTestMarkup())
	if err != nil {
		t.Fatal(err)
	}
	h.stateMu.Lock()
	session := h.userState[h.developerID]
	session.state, session.configKey = "edit_config", "total_slots_in_queue"
	session.enteredAt = time.Now().Add(-time.Hour)
	h.userState[h.developerID] = session
	h.stateMu.Unlock()
	api.failDeletes = 1
	api.onDelete = func() {
		h.stateMu.RLock()
		_, live := h.userState[h.developerID]
		h.stateMu.RUnlock()
		if live {
			t.Error("pending input remained authorized during Telegram deletion")
		}
	}
	h.isExpiredUserEditSes(time.Minute)
	if len(h.userState) != 0 || len(h.editMessages) != 0 || len(h.menus) != 0 || len(h.menuCleanup) != 1 {
		t.Fatalf("timeout kept pending input or lost retry: states=%v edits=%v menus=%v cleanup=%v", h.userState, h.editMessages, h.menus, h.menuCleanup)
	}
	if _, ok := h.NormalizeMenuCallback(t.Context(), menuTestCallback(h.developerID, message, "m:"+h.MenuGeneration(h.developerID)+":ConfigBack")); ok {
		t.Fatal("expired menu callback was accepted")
	}
	h.menusMu.Lock()
	for generation, ref := range h.menuCleanup {
		ref.Deadline = time.Now().Add(-time.Second)
		h.menuCleanup[generation] = ref
	}
	h.retryMenuCleanupLocked(t.Context(), b)
	h.menusMu.Unlock()
	if len(h.menuCleanup) != 0 {
		t.Fatal("failed deletion was not retried")
	}
	deletes := api.methods("deleteEphemeralMessage")
	if len(deletes) != 2 || deletes[0].values["ephemeral_message_id"] != "77" || deletes[0].values["receiver_user_id"] != strconv.FormatInt(h.developerID, 10) || deletes[0].values["message_id"] != "" {
		t.Fatalf("timeout used the wrong transport identity: %+v", deletes)
	}
}

func TestEphemeralMenuCallbackBindsOwnerChatThreadAndGeneration(t *testing.T) {
	h, b, api := newMenuTestHandler(t)
	message, err := h.sendMenu(t.Context(), b, menuTestSource(h.developerID, -100), "config", "Config", menuTestMarkup())
	if err != nil {
		t.Fatal(err)
	}
	generation := h.MenuGeneration(h.developerID)
	wrapped := "m:" + generation + ":ConfigBack"
	tests := []struct {
		name   string
		change func(*models.Update)
	}{
		{"other owner", func(update *models.Update) { update.CallbackQuery.From.ID++ }},
		{"other chat", func(update *models.Update) { update.CallbackQuery.Message.Message.Chat.ID-- }},
		{"other thread", func(update *models.Update) { update.CallbackQuery.Message.Message.MessageThreadID++ }},
		{"other ephemeral id", func(update *models.Update) { update.CallbackQuery.Message.Message.EphemeralMessageID++ }},
		{"standard id is not ephemeral", func(update *models.Update) { update.CallbackQuery.Message.Message.ID = 77 }},
		{"unwrapped callback", func(update *models.Update) { update.CallbackQuery.Data = "ConfigBack" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			update := menuTestCallback(h.developerID, message, wrapped)
			test.change(update)
			if _, ok := h.NormalizeMenuCallback(t.Context(), update); ok {
				t.Fatal("foreign or unverified callback accepted")
			}
		})
	}
	valid := menuTestCallback(h.developerID, message, wrapped)
	ctx, ok := h.NormalizeMenuCallback(t.Context(), valid)
	if !ok || valid.CallbackQuery.Data != "ConfigBack" || !h.menuCallbackMatches(ctx, valid) {
		t.Fatal("valid callback was not authenticated and restored for existing handlers")
	}
	if h.menuCallbackMatches(context.Background(), valid) {
		t.Fatal("direct ephemeral handler bypassed callback authentication")
	}
	api.failDeletes = 1
	current, err := h.sendMenu(t.Context(), b, menuTestSource(h.developerID, -100), "config", "Reopened", menuTestMarkup())
	if err != nil || current.EphemeralMessageID != message.EphemeralMessageID {
		t.Fatalf("reused ephemeral ID setup failed: %+v %v", current, err)
	}
	if _, ok := h.NormalizeMenuCallback(t.Context(), menuTestCallback(h.developerID, message, wrapped)); ok {
		t.Fatal("old generation authorized a reused ephemeral ID")
	}
	if len(h.menuCleanup) != 0 {
		t.Fatal("old cleanup still targets the new generation")
	}
	before := len(api.methods("deleteEphemeralMessage"))
	h.menusMu.Lock()
	h.retryMenuCleanupLocked(t.Context(), b)
	h.menusMu.Unlock()
	if len(api.methods("deleteEphemeralMessage")) != before {
		t.Fatal("old retry deleted the current menu after ID reuse")
	}
	if err := h.editMenuGeneration(t.Context(), b, h.developerID, generation, "Stale refresh", nil); !errors.Is(err, errMenuClosed) {
		t.Fatalf("stale background refresh edited a newer menu: %v", err)
	}
}

func TestMenuUsesEphemeralOnlyForPrivateGroupTools(t *testing.T) {
	for _, test := range []struct {
		kind    string
		chatID  int64
		private bool
	}{{"schedule", -100, false}, {"config", 123, false}, {"moderation", -100, true}, {"config", -100, true}} {
		t.Run(test.kind+strconv.FormatInt(test.chatID, 10), func(t *testing.T) {
			h, b, api := newMenuTestHandler(t)
			if _, err := h.sendMenu(t.Context(), b, menuTestSource(h.developerID, test.chatID), test.kind, "Menu", menuTestMarkup()); err != nil {
				t.Fatal(err)
			}
			sends := api.methods("sendMessage")
			if len(sends) != 1 || (sends[0].values["ephemeral_message_parameters"] != "") != test.private {
				t.Fatalf("wrong transport: %+v", sends)
			}
			if test.private {
				edits := api.methods("editEphemeralMessageText")
				if sends[0].values["reply_markup"] != "" || sends[0].values["text"] != "Открываем личное меню…" || len(edits) != 1 || !strings.Contains(edits[0].values["reply_markup"], "m:"+h.MenuGeneration(h.developerID)+":ConfigBack") {
					t.Fatal("private menu did not verify the transport before displaying its nonce keyboard")
				}
			}
		})
	}
}

func TestEphemeralMenuFromSharedQueueKeepsPublicQueueOutsideCleanup(t *testing.T) {
	h, b, api := newMenuTestHandler(t)
	queueMessage := &models.Message{ID: 300, From: &models.User{ID: 999}, Chat: models.Chat{ID: -100}, MessageThreadID: 7}
	update := menuTestCallback(h.developerID, queueMessage, "ManageQueue:1")
	if _, err := h.sendMenu(t.Context(), b, update, "moderation", "Private moderation", menuTestMarkup()); err != nil {
		t.Fatal(err)
	}
	var parameters models.EphemeralMessageParameters
	if err := json.Unmarshal([]byte(api.methods("sendMessage")[0].values["ephemeral_message_parameters"]), &parameters); err != nil {
		t.Fatal(err)
	}
	if parameters.ReceiverUserID != h.developerID || parameters.CallbackQueryID != update.CallbackQuery.ID || parameters.ReplaceCallbackQueryMessage {
		t.Fatalf("private overlay was not scoped to the clicking user: %+v", parameters)
	}
	if err := h.closeMenu(t.Context(), b, h.developerID); err != nil {
		t.Fatal(err)
	}
	if len(api.methods("deleteMessage")) != 0 || len(api.methods("deleteEphemeralMessage")) != 1 {
		t.Fatal("closing moderation touched its public queue message")
	}
}

func TestUnavailableEphemeralMenuOffersPrivateChatWithoutPublishingFields(t *testing.T) {
	h, b, api := newMenuTestHandler(t)
	api.failEphemeral = true
	if _, err := h.sendMenu(t.Context(), b, menuTestSource(h.developerID, -100), "config", "Sensitive configuration fields", menuTestMarkup()); err == nil {
		t.Fatal("unsupported ephemeral API was reported as an open session")
	}
	sends := api.methods("sendMessage")
	if len(sends) != 2 || sends[0].values["ephemeral_message_parameters"] == "" || sends[1].values["reply_markup"] != "" || strings.Contains(sends[1].values["text"], "Sensitive") || !strings.Contains(sends[1].values["text"], "личном чате") {
		t.Fatalf("unsafe fallback after ephemeral send failure: %+v", sends)
	}
	if len(h.userState) != 0 || len(h.menus) != 0 {
		t.Fatal("failed private send left input authorized")
	}
}

func TestOlderTelegramAPINeverReceivesPublicMenuFields(t *testing.T) {
	h, b, api := newMenuTestHandler(t)
	api.ignoreEphemeral = true
	if _, err := h.sendMenu(t.Context(), b, menuTestSource(h.developerID, -100), "config", "Sensitive fields", menuTestMarkup()); err == nil {
		t.Fatal("ordinary response to an ephemeral request was accepted")
	}
	sends := api.methods("sendMessage")
	if len(sends) != 2 || len(api.methods("editEphemeralMessageText")) != 0 || len(api.methods("deleteMessage")) != 1 {
		t.Fatalf("unsupported private transport did not remove the neutral placeholder: %+v", sends)
	}
	for _, send := range sends {
		if strings.Contains(send.values["text"], "Sensitive") || send.values["reply_markup"] != "" {
			t.Fatalf("older Telegram API could publish menu fields or actions: %+v", send)
		}
	}
	if len(h.userState) != 0 || len(h.menus) != 0 {
		t.Fatal("ordinary Telegram message became a private session")
	}
}

func TestPrivateMenuDisplayFailureClosesItsReferenceAndShowsOnlyNotice(t *testing.T) {
	h, b, api := newMenuTestHandler(t)
	api.failEphemeralEdit = true
	if _, err := h.sendMenu(t.Context(), b, menuTestSource(h.developerID, -100), "config", "Sensitive fields", menuTestMarkup()); err == nil {
		t.Fatal("private display failure was reported as a live menu")
	}
	if len(h.userState) != 0 || len(h.menus) != 0 || len(api.methods("deleteEphemeralMessage")) != 1 {
		t.Fatal("failed private display retained a session or its Telegram placeholder")
	}
	for _, send := range api.methods("sendMessage") {
		if strings.Contains(send.values["text"], "Sensitive") || send.values["reply_markup"] != "" {
			t.Fatalf("private edit failure published menu fields: %+v", send)
		}
	}
}

func TestConfigEphemeralSessionEditsAndFinishesTheSamePrivateMessage(t *testing.T) {
	h, b, api := newMenuTestHandler(t)
	h.EditConfigReplyText(t.Context(), b, menuTestSource(h.developerID, -100))
	ref := h.menus[h.developerID].ref
	message := &models.Message{ID: 0, EphemeralMessageID: ref.EphemeralID, Chat: models.Chat{ID: ref.ChatID}, MessageThreadID: ref.ThreadID}
	update := menuTestCallback(h.developerID, message, "m:"+ref.Generation+":EditConfig:total_slots_in_queue")
	ctx, ok := h.NormalizeMenuCallback(t.Context(), update)
	if !ok {
		t.Fatal("configuration button was not authorized")
	}
	h.EditConfigField(ctx, b, update)
	input := menuTestSource(h.developerID, -100)
	input.Message.Text = "31"
	h.HandleConfigInput(t.Context(), b, input)
	if h.settings.Values().TotalSlotsInQueue != 31 || h.userState[h.developerID].state != "" {
		t.Fatal("scoped group input did not save the private menu setting")
	}
	edits := api.methods("editEphemeralMessageText")
	if len(edits) != 3 || len(api.methods("editMessageText")) != 0 || len(api.methods("sendMessage")) != 1 {
		t.Fatalf("configuration edit escaped its private transport: %+v", edits)
	}
	for _, edit := range edits {
		if edit.values["ephemeral_message_id"] != "77" || edit.values["receiver_user_id"] != strconv.FormatInt(h.developerID, 10) || !strings.Contains(edit.values["reply_markup"], "m:"+ref.Generation+":") {
			t.Fatalf("private menu edit lost its owner, ID or nonce: %+v", edit)
		}
	}
	leave := menuTestCallback(h.developerID, message, "m:"+ref.Generation+":ConfigLeave")
	ctx, ok = h.NormalizeMenuCallback(t.Context(), leave)
	if !ok {
		t.Fatal("configuration finish was not authorized")
	}
	h.LeaveEditConfig(ctx, b, leave)
	if len(h.userState) != 0 || len(api.methods("deleteEphemeralMessage")) != 1 {
		t.Fatal("finishing configuration did not remove its private menu")
	}
}

func TestMenuCleanupRestartDeletesStoredReferencesAndRetriesFailures(t *testing.T) {
	repo := scheduleTestRepository(t)
	h, b, api := newMenuTestHandler(t)
	h.db = repo
	if _, err := h.sendMenu(t.Context(), b, menuTestSource(h.developerID, -100), "config", "Private", menuTestMarkup()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.sendMenu(t.Context(), b, menuTestSource(h.developerID+1, -100), "schedule", "Normal schedule", menuTestMarkup()); err != nil {
		t.Fatal(err)
	}
	api.failDeletes = 100
	if err := h.closeMenu(t.Context(), b, h.developerID); err == nil {
		t.Fatal("delete failure setup failed")
	}
	stored, err := repo.ListMenuCleanup(t.Context())
	if err != nil || len(stored) != 2 {
		t.Fatalf("failed deletion or active normal reference was not durable: %+v %v", stored, err)
	}
	for _, ref := range stored {
		if err := repo.TouchMenuCleanup(t.Context(), ref.Generation, time.Now().Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	restarted, telegram, restartAPI := newMenuTestHandler(t)
	restarted.db = repo
	restartAPI.failDeletes = 1
	if err := restarted.BootstrapMenuCleanup(t.Context(), telegram); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := restarted.ShutdownMenuCleanup(ctx); err != nil {
			t.Error(err)
		}
	})
	restarted.menusMu.Lock()
	if len(restarted.userState) != 0 || len(restarted.menus) != 0 || len(restarted.menuCleanup) != 1 {
		t.Error("restart restored pending input or lost failed cleanup")
	}
	for generation, ref := range restarted.menuCleanup {
		ref.Deadline = time.Now().Add(-time.Second)
		restarted.menuCleanup[generation] = ref
	}
	restarted.menusMu.Unlock()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		stored, err = repo.ListMenuCleanup(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(stored) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var (
		privateDeletes = restartAPI.methods("deleteEphemeralMessage")
		normalDeletes  = restartAPI.methods("deleteMessage")
	)
	if len(stored) != 0 || len(privateDeletes)+len(normalDeletes) != 3 || len(privateDeletes) == 0 || len(normalDeletes) == 0 {
		t.Fatalf("restart worker did not retry durable deletion: refs=%+v private=%+v normal=%+v", stored, privateDeletes, normalDeletes)
	}
}

func TestMenuCleanupDatabaseReopenReusesIDWithoutDeletingCurrentGeneration(t *testing.T) {
	h, b, api := newMenuTestHandler(t)
	h.db = scheduleTestRepository(t)
	if _, err := h.sendMenu(t.Context(), b, menuTestSource(h.developerID, -100), "config", "First menu", menuTestMarkup()); err != nil {
		t.Fatal(err)
	}
	oldGeneration := h.MenuGeneration(h.developerID)
	api.failDeletes = 1
	message, err := h.sendMenu(t.Context(), b, menuTestSource(h.developerID, -100), "config", "Reopened menu", menuTestMarkup())
	if err != nil {
		t.Fatal(err)
	}
	generation := h.MenuGeneration(h.developerID)
	update := menuTestCallback(h.developerID, message, "m:"+generation+":ConfigBack")
	ctx, ok := h.NormalizeMenuCallback(t.Context(), update)
	if !ok || !h.menuCallbackMatches(ctx, update) {
		t.Fatal("reopened menu did not authorize its new generation")
	}
	h.menusMu.Lock()
	h.refreshMenuDeadlinesLocked(t.Context())
	h.retryMenuCleanupLocked(t.Context(), b)
	h.menusMu.Unlock()
	if err := h.db.TouchMenuCleanup(t.Context(), oldGeneration, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := h.db.DeleteMenuCleanup(t.Context(), oldGeneration); err != nil {
		t.Fatal(err)
	}
	stored, err := h.db.ListMenuCleanup(t.Context())
	deadline := h.userState[h.developerID].enteredAt.Add(h.menuTimeout())
	if err != nil || len(stored) != 1 || stored[0].Generation != generation || !stored[0].Deadline.Equal(deadline.Truncate(time.Microsecond)) || len(api.methods("deleteEphemeralMessage")) != 1 {
		t.Fatalf("stale cleanup affected the new generation or its extended deadline: refs=%+v err=%v", stored, err)
	}
	if err := h.closeMenu(t.Context(), b, h.developerID); err != nil {
		t.Fatal(err)
	}
	stored, err = h.db.ListMenuCleanup(t.Context())
	if err != nil || len(stored) != 0 {
		t.Fatalf("current generation was not removed after closing: refs=%+v err=%v", stored, err)
	}
}

func TestMenuCleanupPersistsTelegramRetryAfterFromFirstFailure(t *testing.T) {
	h, b, api := newMenuTestHandler(t)
	h.db = scheduleTestRepository(t)
	if _, err := h.sendMenu(t.Context(), b, menuTestSource(h.developerID, -100), "config", "Menu", menuTestMarkup()); err != nil {
		t.Fatal(err)
	}
	api.failDeletes, api.deleteRetryAfter = 1, 30
	before := time.Now()
	if err := h.closeMenu(t.Context(), b, h.developerID); err == nil {
		t.Fatal("rate limit failure setup failed")
	}
	stored, err := h.db.ListMenuCleanup(t.Context())
	if err != nil || len(stored) != 1 || stored[0].Deadline.Before(before.Add(30*time.Second)) {
		t.Fatalf("first Telegram rate limit was not persisted: refs=%+v err=%v", stored, err)
	}
	h.menusMu.Lock()
	h.retryMenuCleanupLocked(t.Context(), b)
	h.menusMu.Unlock()
	if len(api.methods("deleteEphemeralMessage")) != 1 {
		t.Fatal("cleanup retried before Telegram RetryAfter")
	}
	restarted, telegram, restartAPI := newMenuTestHandler(t)
	restarted.db = h.db
	if err := restarted.BootstrapMenuCleanup(t.Context(), telegram); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := restarted.ShutdownMenuCleanup(ctx); err != nil {
			t.Error(err)
		}
	})
	restarted.menusMu.Lock()
	restarted.retryMenuCleanupLocked(t.Context(), telegram)
	pending := restarted.menuCleanup[stored[0].Generation]
	restarted.menusMu.Unlock()
	if !pending.Deadline.Equal(stored[0].Deadline) || len(restartAPI.methods("deleteEphemeralMessage")) != 0 {
		t.Fatalf("restart ignored the saved Telegram RetryAfter: before=%s after=%s requests=%+v", stored[0].Deadline, pending.Deadline, restartAPI.methods("deleteEphemeralMessage"))
	}
	if len(restarted.userState) != 0 || len(restarted.menus) != 0 {
		t.Fatal("waiting for persisted cleanup restored pending input")
	}
}

func newMenuTestHandler(t *testing.T) (*BotHandler, *bot.Bot, *menuTestAPI) {
	t.Helper()
	h, _, _, _ := newConfigTestHandler(t, "config")
	h.userState = make(map[int64]userState)
	h.editMessages = make(map[int64]int)
	h.editMessageChats = make(map[int64]int64)
	api := &menuTestAPI{}
	telegram, err := bot.New("1:test", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, api))
	if err != nil {
		t.Fatal(err)
	}
	h.b = telegram
	return h, telegram, api
}

func menuTestSource(ownerID, chatID int64) *models.Update {
	return &models.Update{Message: &models.Message{From: &models.User{ID: ownerID}, Chat: models.Chat{ID: chatID}, MessageThreadID: 7}}
}

func menuTestMarkup() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "Back", CallbackData: "ConfigBack"}}}}
}

func menuTestCallback(ownerID int64, message *models.Message, data string) *models.Update {
	copy := *message
	return &models.Update{CallbackQuery: &models.CallbackQuery{ID: "menu-callback", From: models.User{ID: ownerID}, Data: data, Message: models.MaybeInaccessibleMessage{Message: &copy}}}
}

type menuTestAPI struct {
	mu                sync.Mutex
	requests          []configTestRequest
	failDeletes       int
	deleteRetryAfter  int
	failEphemeral     bool
	ignoreEphemeral   bool
	failEphemeralEdit bool
	nextMessageID     int
	onDelete          func()
}

func (api *menuTestAPI) methods(method string) []configTestRequest {
	api.mu.Lock()
	defer api.mu.Unlock()
	var matching []configTestRequest
	for _, request := range api.requests {
		if request.method == method {
			matching = append(matching, request)
		}
	}
	return matching
}

func (api *menuTestAPI) Do(request *http.Request) (*http.Response, error) {
	if err := request.ParseMultipartForm(64 << 10); err != nil {
		return nil, err
	}
	values := make(map[string]string)
	for key, fields := range request.MultipartForm.Value {
		values[key] = fields[0]
	}
	method := path.Base(request.URL.Path)
	api.mu.Lock()
	defer api.mu.Unlock()
	api.requests = append(api.requests, configTestRequest{method: method, values: values})
	if strings.HasPrefix(method, "delete") {
		if api.onDelete != nil {
			api.onDelete()
		}
		if api.failDeletes > 0 {
			api.failDeletes--
			if api.deleteRetryAfter > 0 {
				return menuTestResponse(map[string]any{"ok": false, "error_code": 429, "description": "Too Many Requests", "parameters": map[string]any{"retry_after": api.deleteRetryAfter}})
			}
			return menuTestResponse(map[string]any{"ok": false, "error_code": 500, "description": "temporary deletion failure"})
		}
	}
	var result any = true
	if method == "editEphemeralMessageText" && api.failEphemeralEdit {
		return menuTestResponse(map[string]any{"ok": false, "error_code": 500, "description": "temporary private edit failure"})
	}
	if method == "sendMessage" {
		chatID, _ := strconv.ParseInt(values["chat_id"], 10, 64)
		threadID, _ := strconv.Atoi(values["message_thread_id"])
		if values["ephemeral_message_parameters"] != "" && !api.ignoreEphemeral {
			if api.failEphemeral {
				return menuTestResponse(map[string]any{"ok": false, "error_code": 400, "description": "unsupported ephemeral_message_parameters"})
			}
			result = map[string]any{"message_id": 0, "ephemeral_message_id": 77, "chat": map[string]any{"id": chatID}, "message_thread_id": threadID}
		} else {
			api.nextMessageID++
			result = map[string]any{"message_id": 500 + api.nextMessageID, "chat": map[string]any{"id": chatID}, "message_thread_id": threadID}
		}
	} else if method == "editMessageText" {
		result = map[string]any{"message_id": 501, "chat": map[string]any{"id": -100}}
	}
	return menuTestResponse(map[string]any{"ok": true, "result": result})
}

func menuTestResponse(response map[string]any) (*http.Response, error) {
	body, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
}
