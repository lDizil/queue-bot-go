package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot"
)

func TestChatIDCommandIsPublicQualifiedAndKeepsSourceTopic(t *testing.T) {
	for _, source := range []struct {
		name   string
		chat   int64
		thread int
	}{{"group topic", -100, 7}, {"private chat", 88, 0}} {
		t.Run(source.name, func(t *testing.T) {
			h, telegram, api, _ := newTelegramAdapter(t)
			update := chatEditorMessage(456, source.chat, source.thread, "/chat_id@QueueBot")
			update.Message.Chat.Title = "Do not expose the title"
			update.Message.From.Username = "DoNotExposeUsername"
			processTelegramUpdates(t, h, telegram, update,
				chatEditorMessage(456, source.chat, source.thread, "/start"),
				chatEditorMessage(456, source.chat, source.thread, "/chat_id@OtherBot"),
				chatEditorMessage(456, source.chat, source.thread, "/chat_id_extra"))
			requests := api.history()
			if len(requests) != 2 || requests[0].method != "sendMessage" || requests[1].method != "sendMessage" {
				t.Fatalf("routing changed or cleanup blocked the next command: %+v", requests)
			}
			reply := requests[0].values
			if reply["chat_id"] != strconv.FormatInt(source.chat, 10) || reply["parse_mode"] != "HTML" {
				t.Fatalf("identifier reply has wrong address or copyable formatting: %+v", reply)
			}
			if !strings.Contains(reply["text"], "<code>"+strconv.FormatInt(source.chat, 10)+"</code>") {
				t.Fatalf("actual chat ID missing: %q", reply["text"])
			}
			if source.thread > 0 {
				if reply["message_thread_id"] != strconv.Itoa(source.thread) || !strings.Contains(reply["text"], "<code>7</code>") {
					t.Fatalf("actual topic ID missing or reply moved: %+v", reply)
				}
			} else if strings.Contains(reply["text"], "ID темы") {
				t.Fatalf("private chat invented a topic ID: %q", reply["text"])
			}
			if strings.Contains(reply["text"], "Do not expose") || strings.Contains(reply["text"], "DoNotExpose") {
				t.Fatalf("identifier reply includes names: %q", reply["text"])
			}
		})
	}
}

func TestChatIDCleanupDeletesOwnReplyEvenWhenCommandDeletionIsForbidden(t *testing.T) {
	var (
		mu      sync.Mutex
		deleted []int
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseMultipartForm(64 << 10); err != nil {
			t.Error(err)
			return
		}
		defer request.MultipartForm.RemoveAll()
		id, _ := strconv.Atoi(request.FormValue("message_id"))
		if request.FormValue("chat_id") != "-100" || !strings.HasSuffix(request.URL.Path, "/deleteMessage") {
			t.Error("cleanup targeted another chat or method")
		}
		mu.Lock()
		deleted = append(deleted, id)
		mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		if id == 10 {
			writer.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(writer).Encode(map[string]any{"ok": false, "error_code": 403, "description": "Cannot delete someone else's command"})
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "result": true})
	}))
	t.Cleanup(server.Close)
	telegram, err := bot.New("1:cleanup-test", bot.WithSkipGetMe(), bot.WithServerURL(server.URL), bot.WithHTTPClient(time.Second, server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	h := &BotHandler{ctx: t.Context()}
	h.deleteChatIDMessages(telegram, -100, 20, 10, time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(deleted) != 2 || deleted[0] != 20 || deleted[1] != 10 {
		t.Fatalf("own reply was not deleted before the best-effort command: %v", deleted)
	}
}

func TestChatIDCleanupCancelsPendingTimerAndInflightRequest(t *testing.T) {
	for _, inflight := range []bool{false, true} {
		name := "pending timer"
		if inflight {
			name = "inflight delete"
		}
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			started := make(chan struct{})
			stop := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if err := request.ParseMultipartForm(64 << 10); err != nil {
					t.Error(err)
					return
				}
				defer request.MultipartForm.RemoveAll()
				requests.Add(1)
				close(started)
				select {
				case <-request.Context().Done():
				case <-stop:
				}
			}))
			t.Cleanup(server.Close)
			t.Cleanup(func() { close(stop) })
			telegram, err := bot.New("1:cleanup-test", bot.WithSkipGetMe(), bot.WithServerURL(server.URL), bot.WithHTTPClient(5*time.Second, server.Client()))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			h := &BotHandler{ctx: ctx}
			delay := time.Hour
			if inflight {
				delay = time.Millisecond
			}
			finished := make(chan struct{})
			go func() { h.deleteChatIDMessages(telegram, -100, 20, 10, delay); close(finished) }()
			if inflight {
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("delete request did not start")
				}
			}
			cancel()
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("cleanup did not stop with the bot context")
			}
			want := int32(0)
			if inflight {
				want = 1
			}
			if requests.Load() != want {
				t.Fatalf("cleanup continued after cancellation: calls=%d want=%d", requests.Load(), want)
			}
		})
	}
}

func TestChatIDSendFailureDoesNotStartCleanup(t *testing.T) {
	h, telegram, api, _ := newTelegramAdapter(t)
	api.fail("sendMessage", http.StatusServiceUnavailable, "Service unavailable")
	processTelegramUpdates(t, h, telegram, chatEditorMessage(456, -100, 7, "/chat_id"))
	if requests := api.history(); len(requests) != 1 || requests[0].method != "sendMessage" {
		t.Fatalf("failed reply scheduled unrelated actions: %+v", requests)
	}
}
