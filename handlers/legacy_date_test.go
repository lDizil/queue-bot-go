package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"queuebot/db"
	"queuebot/queue"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func TestQueueMessageDateUsesOriginalDateWithoutSendingMessages(t *testing.T) {
	for _, unchanged := range []bool{false, true} {
		t.Run(map[bool]string{false: "existing keyboard", true: "keyboard already removed"}[unchanged], func(t *testing.T) {
			var (
				mu       sync.Mutex
				requests []models.InlineKeyboardMarkup
				sentAt   = time.Date(2026, 9, 7, 20, 59, 0, 0, time.UTC)
			)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if path.Base(request.URL.Path) != "editMessageReplyMarkup" {
					t.Errorf("unexpected Telegram method: %s", request.URL.Path)
					http.Error(writer, "unexpected method", http.StatusBadRequest)
					return
				}
				if err := request.ParseMultipartForm(64 << 10); err != nil {
					t.Error(err)
					return
				}
				defer request.MultipartForm.RemoveAll()
				var markup models.InlineKeyboardMarkup
				if err := json.Unmarshal([]byte(request.FormValue("reply_markup")), &markup); err != nil {
					t.Error(err)
					return
				}
				if request.FormValue("text") != "" {
					t.Error("metadata request changed message text")
				}
				mu.Lock()
				requests = append(requests, markup)
				first := len(requests) == 1
				mu.Unlock()
				writer.Header().Set("Content-Type", "application/json")
				if unchanged && first {
					writer.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(writer).Encode(map[string]any{"ok": false, "error_code": 400, "description": "Bad Request: message is not modified"})
					return
				}
				_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 123, "chat": map[string]any{"id": -100}, "date": sentAt.Unix(), "edit_date": sentAt.AddDate(0, 0, 7).Unix()}})
			}))
			t.Cleanup(server.Close)
			telegram, err := bot.New("1:legacy-test", bot.WithSkipGetMe(), bot.WithServerURL(server.URL), bot.WithHTTPClient(time.Second, server.Client()))
			if err != nil {
				t.Fatal(err)
			}
			handler := &BotHandler{b: telegram}
			got, err := handler.QueueMessageDate(t.Context(), db.Schedule{ChatID: -100}, 123)
			if err != nil || !got.Equal(sentAt) {
				t.Fatalf("wrong original date: got=%v err=%v", got, err)
			}
			mu.Lock()
			defer mu.Unlock()
			wantCalls := 1
			if unchanged {
				wantCalls = 2
			}
			if len(requests) != wantCalls || len(requests[0].InlineKeyboard) != 0 {
				t.Fatalf("unexpected metadata calls: %+v", requests)
			}
			if unchanged && (len(requests[1].InlineKeyboard) != 1 || requests[1].InlineKeyboard[0][0].CallbackData != "CloseQueue") {
				t.Fatalf("fallback introduced a new action: %+v", requests[1])
			}
		})
	}
}

func TestQueueMessageDateRejectsUnknownOrDifferentMessage(t *testing.T) {
	for _, test := range []struct {
		name      string
		messageID int
		chatID    int64
		date      int64
	}{
		{"missing date", 123, -100, 0},
		{"different message", 456, -100, 100},
		{"different chat", 123, -200, 100},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": test.messageID, "chat": map[string]any{"id": test.chatID}, "date": test.date}})
			}))
			t.Cleanup(server.Close)
			telegram, err := bot.New("1:legacy-test", bot.WithSkipGetMe(), bot.WithServerURL(server.URL), bot.WithHTTPClient(time.Second, server.Client()))
			if err != nil {
				t.Fatal(err)
			}
			handler := &BotHandler{b: telegram}
			got, err := handler.QueueMessageDate(t.Context(), db.Schedule{ChatID: -100}, 123)
			if !errors.Is(err, queue.ErrLegacyDateUnavailable) || !got.IsZero() {
				t.Fatalf("untrusted date accepted: got=%v err=%v", got, err)
			}
		})
	}
}

func TestRetireQueueHandlesAlreadyChangedOrDeletedMessage(t *testing.T) {
	for _, test := range []struct {
		description string
		missing     bool
	}{
		{"Bad Request: message is not modified", false},
		{"Bad Request: message to edit not found", true},
	} {
		t.Run(test.description, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(writer).Encode(map[string]any{"ok": false, "error_code": 400, "description": test.description})
			}))
			t.Cleanup(server.Close)
			telegram, err := bot.New("1:legacy-test", bot.WithSkipGetMe(), bot.WithServerURL(server.URL), bot.WithHTTPClient(time.Second, server.Client()))
			if err != nil {
				t.Fatal(err)
			}
			handler := &BotHandler{b: telegram}
			err = handler.RetireQueue(t.Context(), -100, 123)
			if test.missing && !errors.Is(err, queue.ErrMessageMissing) {
				t.Fatalf("missing message not classified: %v", err)
			}
			if !test.missing && err != nil {
				t.Fatalf("already retired message should succeed: %v", err)
			}
		})
	}
}

func TestQueueMessageDatePreservesTelegramFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(writer).Encode(map[string]any{"ok": false, "error_code": 503, "description": "Service unavailable"})
	}))
	t.Cleanup(server.Close)
	telegram, err := bot.New("1:legacy-test", bot.WithSkipGetMe(), bot.WithServerURL(server.URL), bot.WithHTTPClient(time.Second, server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	handler := &BotHandler{b: telegram}
	got, err := handler.QueueMessageDate(t.Context(), db.Schedule{ChatID: -100}, 123)
	if err == nil || !strings.Contains(err.Error(), "503") || !got.IsZero() {
		t.Fatalf("Telegram failure was hidden: got=%v err=%v", got, err)
	}
}
