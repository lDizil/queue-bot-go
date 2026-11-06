package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"
)

func TestChatReaderChecksTypeMembershipAndTextPermissionsWithoutSending(t *testing.T) {
	for _, test := range []struct {
		name        string
		chatType    string
		status      string
		permissions any
		isMember    bool
		canSend     bool
		memberID    int64
		wantAllowed bool
	}{
		{"administrator in restricted group", "supergroup", "administrator", map[string]bool{"can_send_messages": false}, true, false, 42, true},
		{"ordinary member", "group", "member", map[string]bool{"can_send_messages": true}, true, true, 42, true},
		{"omitted optional permissions", "group", "member", nil, true, true, 42, true},
		{"muted default permissions", "supergroup", "member", map[string]bool{"can_send_messages": false}, true, false, 42, false},
		{"restricted but can send", "supergroup", "restricted", nil, true, true, 42, true},
		{"restricted non-member", "supergroup", "restricted", nil, false, true, 42, false},
		{"muted member", "supergroup", "restricted", nil, true, false, 42, false},
		{"bot left", "group", "left", nil, false, false, 42, false},
		{"wrong member identity", "group", "member", nil, true, true, 99, false},
		{"channel", "channel", "administrator", nil, true, true, 42, false},
		{"private chat", "private", "member", nil, true, true, 42, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var (
				mu      sync.Mutex
				methods []string
			)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if err := request.ParseMultipartForm(64 << 10); err != nil {
					t.Error(err)
					return
				}
				defer request.MultipartForm.RemoveAll()
				method := path.Base(request.URL.Path)
				mu.Lock()
				methods = append(methods, method)
				mu.Unlock()
				var result any
				switch method {
				case "getChat":
					result = map[string]any{"id": -100, "type": test.chatType, "title": "БГГ323", "permissions": test.permissions}
				case "getChatMember":
					if request.FormValue("user_id") != "42" || request.FormValue("chat_id") != "-100" {
						t.Error("membership check targeted another user or chat")
					}
					result = map[string]any{"status": test.status, "user": map[string]any{"id": test.memberID}, "is_member": test.isMember, "can_send_messages": test.canSend}
				default:
					t.Errorf("metadata lookup unexpectedly called %s", method)
				}
				_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "result": result})
			}))
			t.Cleanup(server.Close)
			telegram, err := bot.New("42:chat-reader", bot.WithSkipGetMe(), bot.WithServerURL(server.URL), bot.WithHTTPClient(time.Second, server.Client()))
			if err != nil {
				t.Fatal(err)
			}
			info, err := NewChatReader(telegram, 42).ReadChat(t.Context(), -100)
			if (err == nil) != test.wantAllowed {
				t.Fatalf("chat access=%v, want allowed=%v", err, test.wantAllowed)
			}
			if test.wantAllowed && info.Title != "БГГ323" {
				t.Fatalf("chat title was lost: %+v", info)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(methods) == 0 || len(methods) > 2 {
				t.Fatalf("unexpected metadata calls: %v", methods)
			}
		})
	}
}

func TestChatReaderRejectsAPIErrorAndMismatchedChat(t *testing.T) {
	for _, failedMethod := range []string{"getChat", "getChatMember", "wrong chat"} {
		t.Run(failedMethod, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if path.Base(request.URL.Path) == failedMethod {
					writer.WriteHeader(http.StatusForbidden)
					_ = json.NewEncoder(writer).Encode(map[string]any{"ok": false, "error_code": 403, "description": "Forbidden"})
					return
				}
				id := int64(-100)
				if failedMethod == "wrong chat" {
					id = -200
				}
				_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "result": map[string]any{"id": id, "type": "group", "title": "Group"}})
			}))
			t.Cleanup(server.Close)
			telegram, err := bot.New("42:chat-reader", bot.WithSkipGetMe(), bot.WithServerURL(server.URL), bot.WithHTTPClient(time.Second, server.Client()))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := NewChatReader(telegram, 42).ReadChat(t.Context(), -100); err == nil {
				t.Fatal("unavailable or mismatched group was accepted")
			}
		})
	}
}
