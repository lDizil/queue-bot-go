package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"queuebot/db"
	"queuebot/queue"
	"queuebot/settings"
	"queuebot/utils"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func TestTelegramQueueAdapterSendsTitleCompletionAndOriginalTopic(t *testing.T) {
	handler, _, api, _ := newTelegramAdapter(t)
	activeThread := 7
	snapshot := queue.Snapshot{
		Schedule: db.Schedule{ID: 42, ChatID: -100, ThreadID: 99, ActiveThreadID: &activeThread,
			Title: "Практика по Go", ActiveCapacity: 3, ActiveColumns: 3},
		Entries: []db.QueueEntry{
			{ID: 101, UserID: 101, Username: "alice", Position: 1, Completed: true},
			{ID: 202, UserID: 202, DisplayName: "Борис", Position: 2},
		},
		Status: utils.QueueOpen,
	}
	messageID, err := handler.SendQueue(t.Context(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.EditQueue(t.Context(), snapshot, messageID); err != nil {
		t.Fatal(err)
	}
	requests := api.history()
	if len(requests) != 2 || requests[0].method != "sendMessage" || requests[1].method != "editMessageText" {
		t.Fatalf("unexpected Telegram calls: %+v", requests)
	}
	if messageID != 1001 || requests[0].values["chat_id"] != "-100" || requests[0].values["message_thread_id"] != "7" {
		t.Fatalf("queue moved to the edited topic or group: %+v, message ID=%d", requests[0], messageID)
	}
	if requests[1].values["chat_id"] != "-100" || requests[1].values["message_id"] != "1001" {
		t.Fatalf("queue edit targeted another message: %+v", requests[1])
	}
	for _, request := range requests {
		text := request.values["text"]
		if !strings.HasPrefix(text, "Практика по Go\n\nЭто сообщение") {
			t.Fatalf("title is not the first paragraph: %q", text)
		}
		if !strings.Contains(text, "1. @alice ✅\n") || !strings.Contains(text, "2. Борис\n") {
			t.Fatalf("completion mark is not after its participant: %q", text)
		}
		var markup models.InlineKeyboardMarkup
		if err := json.Unmarshal([]byte(request.values["reply_markup"]), &markup); err != nil {
			t.Fatalf("invalid Telegram keyboard: %v", err)
		}
		if len(markup.InlineKeyboard) == 0 || len(markup.InlineKeyboard[0]) != 3 {
			t.Fatalf("slot keyboard was not transmitted: %+v", markup)
		}
		completed := markup.InlineKeyboard[0][0]
		if completed.Text != "1 ✅" || completed.CallbackData != "CompleteSlot:42:101:1:false" {
			t.Fatalf("completed slot lost its entry identity or undo action: %+v", completed)
		}
		waiting := markup.InlineKeyboard[0][1]
		if waiting.Text != "2 ⏳" || waiting.CallbackData != "CompleteSlot:42:202:2:true" {
			t.Fatalf("waiting slot lost its completion action: %+v", waiting)
		}
	}
}

func TestTelegramQueueAdapterPropagatesFailuresAndAcceptsUnchangedMessage(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		status      int
		description string
		wantError   bool
	}{
		{"send unavailable", "sendMessage", http.StatusServiceUnavailable, "Service unavailable", true},
		{"edit unavailable", "editMessageText", http.StatusServiceUnavailable, "Service unavailable", true},
		{"unchanged edit", "editMessageText", http.StatusBadRequest, "Bad Request: message is not modified", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, _, api, _ := newTelegramAdapter(t)
			api.fail(test.method, test.status, test.description)
			snapshot := queue.Snapshot{Schedule: db.Schedule{ID: 42, ChatID: -100, ActiveCapacity: 3, ActiveColumns: 3}, Status: utils.QueueOpen}
			var err error
			if test.method == "sendMessage" {
				var messageID int
				messageID, err = handler.SendQueue(t.Context(), snapshot)
				if err != nil && messageID != 0 {
					t.Fatalf("failed send returned a message ID: %d", messageID)
				}
			} else {
				err = handler.EditQueue(t.Context(), snapshot, 1001)
			}
			if test.wantError && (err == nil || !strings.Contains(err.Error(), "503")) {
				t.Fatalf("temporary Telegram error was swallowed or changed: %v", err)
			}
			if !test.wantError && err != nil {
				t.Fatalf("unchanged message should count as successful delivery: %v", err)
			}
			requests := api.history()
			if len(requests) != 1 || requests[0].method != test.method {
				t.Fatalf("unexpected failure handling calls: %+v", requests)
			}
		})
	}
}

func TestTelegramRegistrationRoutesQualifiedDeveloperConfigAndPersistsInput(t *testing.T) {
	handler, telegram, api, store := newTelegramAdapter(t)
	processTelegramUpdates(t, handler, telegram, &models.Update{Message: privateAdapterMessage(handler.developerID, "/edit_config@QueueBot")})
	requests := api.history()
	if len(requests) != 1 || requests[0].method != "sendMessage" || !strings.Contains(requests[0].values["text"], "Настройки бота") {
		t.Fatalf("qualified developer command did not open settings: %+v", requests)
	}
	queryMessage := privateAdapterMessage(handler.developerID, "")
	queryMessage.ID = 1001
	processTelegramUpdates(t, handler, telegram, &models.Update{CallbackQuery: &models.CallbackQuery{
		ID: "select-slots", From: models.User{ID: handler.developerID}, Data: "EditConfig:total_slots_in_queue",
		Message: models.MaybeInaccessibleMessage{Message: queryMessage},
	}})
	processTelegramUpdates(t, handler, telegram, &models.Update{Message: privateAdapterMessage(handler.developerID, "31")})
	if handler.settings.Values().TotalSlotsInQueue != 31 || store.saves != 1 {
		t.Fatalf("routed field input was not persisted: slots=%d saves=%d", handler.settings.Values().TotalSlotsInQueue, store.saves)
	}
	for _, request := range api.history()[1:] {
		if request.method == "sendMessage" {
			t.Fatal("field editing sent an extra message instead of updating its menu")
		}
	}
}

func TestTelegramRegistrationDeniesConfigToOtherUsersAndAdministrators(t *testing.T) {
	for _, userID := range []int64{123, 456} {
		t.Run(strconv.FormatInt(userID, 10), func(t *testing.T) {
			handler, telegram, api, store := newTelegramAdapter(t)
			processTelegramUpdates(t, handler, telegram, &models.Update{Message: adapterMessage(userID, "/edit_config@QueueBot")})
			requests := api.history()
			if len(requests) != 1 || requests[0].method != "sendMessage" || !strings.Contains(requests[0].values["text"], "недостаточно прав") {
				t.Fatalf("non-developer did not receive a denial: %+v", requests)
			}
			if store.saves != 0 || handler.settings.Values().TotalSlotsInQueue != 30 {
				t.Fatal("denied command changed configuration")
			}
			handler.stateMu.RLock()
			_, exists := handler.userState[userID]
			handler.stateMu.RUnlock()
			if exists {
				t.Fatal("denied command created an editing session")
			}
		})
	}
}

func TestTelegramRegistrationIgnoresSimilarAndOtherBotCommandsDuringEditing(t *testing.T) {
	for _, command := range []string{"/edit_config_extra", "/edit_config@OtherBot"} {
		t.Run(command, func(t *testing.T) {
			handler, telegram, api, store := newTelegramAdapter(t)
			handler.userState[handler.developerID] = userState{
				menu: "config", state: "edit_config", configKey: "total_slots_in_queue",
				enteredAt: time.Now(), chatID: -100, threadID: 7,
			}
			handler.editMessages[handler.developerID] = 1001
			handler.editMessageChats[handler.developerID] = -100
			processTelegramUpdates(t, handler, telegram, &models.Update{Message: adapterMessage(handler.developerID, command)})
			if requests := api.history(); len(requests) != 0 {
				t.Fatalf("unrelated command was handled as a command or field input: %+v", requests)
			}
			if store.saves != 0 || handler.settings.Values().TotalSlotsInQueue != 30 {
				t.Fatal("unrelated command changed configuration")
			}
		})
	}
}

func newTelegramAdapter(t *testing.T) (*BotHandler, *bot.Bot, *telegramAdapterAPI, *configTestStore) {
	t.Helper()
	var (
		store = &configTestStore{values: settings.Defaults()}
		api   = &telegramAdapterAPI{nextMessageID: 1000}
	)
	manager, err := settings.NewManager(t.Context(), store, settings.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	const developerID = int64(789915524)
	handler := NewBotHandler(t.Context(), nil, manager, developerID, &testAccessControl{admins: map[int64]bool{123: true}})
	server := httptest.NewServer(http.HandlerFunc(api.serveHTTP))
	t.Cleanup(server.Close)
	telegram, err := handler.NewTelegramBot("1:adapter", bot.WithSkipGetMe(),
		bot.WithServerURL(server.URL), bot.WithHTTPClient(time.Second, server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := handler.Shutdown(ctx); err != nil {
			t.Errorf("shutdown Telegram dispatcher: %v", err)
		}
	})
	handler.SetBot(telegram)
	handler.RegisterHandlers(telegram, "QueueBot")
	return handler, telegram, api, store
}

func adapterMessage(userID int64, text string) *models.Message {
	return &models.Message{ID: 500, From: &models.User{ID: userID}, Chat: models.Chat{ID: -100}, MessageThreadID: 7, Text: text}
}

type telegramAdapterRequest struct {
	method string
	values map[string]string
}

type telegramAdapterAPI struct {
	mu            sync.Mutex
	requests      []telegramAdapterRequest
	nextMessageID int
	failureMethod string
	failureStatus int
	failureText   string
}

func (api *telegramAdapterAPI) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseMultipartForm(64 << 10); err != nil {
		http.Error(writer, "invalid multipart Telegram request", http.StatusBadRequest)
		return
	}
	defer request.MultipartForm.RemoveAll()
	values := make(map[string]string)
	for key, fieldValues := range request.MultipartForm.Value {
		values[key] = fieldValues[0]
	}
	method := path.Base(request.URL.Path)
	api.mu.Lock()
	api.requests = append(api.requests, telegramAdapterRequest{method: method, values: values})
	var (
		status = http.StatusOK
		body   = map[string]any{"ok": true, "result": true}
	)
	if method == api.failureMethod {
		status = api.failureStatus
		body = map[string]any{"ok": false, "error_code": status, "description": api.failureText}
	} else if method == "sendMessage" || method == "editMessageText" {
		messageID, _ := strconv.Atoi(values["message_id"])
		if method == "sendMessage" {
			api.nextMessageID++
			messageID = api.nextMessageID
		}
		chatID, _ := strconv.ParseInt(values["chat_id"], 10, 64)
		threadID, _ := strconv.Atoi(values["message_thread_id"])
		result := map[string]any{"message_id": messageID, "chat": map[string]any{"id": chatID}, "message_thread_id": threadID}
		if values["ephemeral_message_parameters"] != "" {
			ephemeralID := messageID
			if method == "editEphemeralMessageText" {
				ephemeralID, _ = strconv.Atoi(values["ephemeral_message_id"])
			}
			result["message_id"] = 0
			result["ephemeral_message_id"] = ephemeralID
		}
		body["result"] = result
	}
	api.mu.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(body)
}

func (api *telegramAdapterAPI) fail(method string, status int, text string) {
	api.mu.Lock()
	defer api.mu.Unlock()
	api.failureMethod, api.failureStatus, api.failureText = method, status, text
}

func (api *telegramAdapterAPI) history() []telegramAdapterRequest {
	api.mu.Lock()
	defer api.mu.Unlock()
	return append([]telegramAdapterRequest(nil), api.requests...)
}
