package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"queuebot/db"
	"queuebot/queue"
	"queuebot/utils"

	"github.com/go-telegram/bot"
)

func TestQueueOutboundErrorsPreserveRateLimitAndCause(t *testing.T) {
	for _, operation := range []string{"send", "edit", "retire", "notify", "original date"} {
		for _, test := range []struct {
			name        string
			status      int
			description string
		}{
			{"rate limited", http.StatusTooManyRequests, "Too Many Requests: retry after 7"},
			{"rate limited with unchanged text", http.StatusTooManyRequests, "message is not modified"},
			{"unavailable", http.StatusServiceUnavailable, "Service unavailable"},
			{"unavailable with unchanged text", http.StatusServiceUnavailable, "message is not modified"},
		} {
			t.Run(operation+"/"+test.name, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					requests.Add(1)
					wantMethod := "sendMessage"
					if operation == "edit" {
						wantMethod = "editMessageText"
					}
					if operation == "retire" || operation == "original date" {
						wantMethod = "editMessageReplyMarkup"
					}
					if path.Base(request.URL.Path) != wantMethod {
						t.Errorf("unexpected Telegram method: %s", request.URL.Path)
					}
					writer.Header().Set("Content-Type", "application/json")
					writer.WriteHeader(test.status)
					_ = json.NewEncoder(writer).Encode(map[string]any{
						"ok": false, "error_code": test.status, "description": test.description,
						"parameters": map[string]any{"retry_after": 7},
					})
				}))
				t.Cleanup(server.Close)
				telegram, err := bot.New("1:rate-limit-test", bot.WithSkipGetMe(), bot.WithServerURL(server.URL), bot.WithHTTPClient(time.Second, server.Client()))
				if err != nil {
					t.Fatal(err)
				}
				handler := &BotHandler{b: telegram}
				err = callQueueOutbound(t, handler, operation)
				if err == nil {
					t.Fatal("Telegram delivery failure was swallowed")
				}
				var limited queue.RetryAfterError
				if test.status == http.StatusTooManyRequests {
					var cause *bot.TooManyRequestsError
					if !errors.As(err, &limited) || limited.RetryAfter() != 7*time.Second {
						t.Fatalf("Telegram retry_after was lost: %v", err)
					}
					if !errors.As(err, &cause) || cause.RetryAfter != 7 || !errors.Is(err, cause) {
						t.Fatalf("original Telegram error was lost: %v", err)
					}
				} else if errors.As(err, &limited) || !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), test.description) {
					t.Fatalf("ordinary Telegram failure was changed into a rate limit: %v", err)
				}
				if requests.Load() != 1 {
					t.Fatalf("adapter retried or hid the failed request: calls=%d", requests.Load())
				}
			})
		}
	}
}

func callQueueOutbound(t *testing.T, handler *BotHandler, operation string) error {
	t.Helper()
	ctx := t.Context()
	snapshot := queue.Snapshot{Schedule: db.Schedule{ID: 42, ChatID: -100, ActiveCapacity: 3, ActiveColumns: 3}, Status: utils.QueueOpen}
	switch operation {
	case "send":
		messageID, err := handler.SendQueue(ctx, snapshot)
		if messageID != 0 {
			t.Fatalf("failed send returned a message ID: %d", messageID)
		}
		return err
	case "edit":
		return handler.EditQueue(ctx, snapshot, 123)
	case "retire":
		return handler.RetireQueue(ctx, -100, 123)
	case "notify":
		return handler.NotifyQueue(ctx, snapshot.Schedule, "Queue opens soon")
	case "original date":
		date, err := handler.QueueMessageDate(ctx, snapshot.Schedule, 123)
		if !date.IsZero() {
			t.Fatalf("failed metadata request returned a date: %v", date)
		}
		return err
	default:
		t.Fatalf("unsupported test operation: %s", operation)
		return nil
	}
}

func TestQueueDeliveryErrorRetainsWrappedSDKCause(t *testing.T) {
	cause := &bot.TooManyRequestsError{Message: "Too many requests", RetryAfter: 3}
	wrapped := fmt.Errorf("delivery context: %w", cause)
	converted := queueDeliveryError(wrapped)
	var limited queue.RetryAfterError
	var original *bot.TooManyRequestsError
	if !errors.As(converted, &limited) || limited.RetryAfter() != 3*time.Second || !errors.As(converted, &original) || original != cause {
		t.Fatalf("wrapped rate limit lost its policy or cause: %v", converted)
	}
	if !errors.Is(converted, wrapped) || !errors.Is(converted, cause) || converted.Error() != wrapped.Error() {
		t.Fatalf("wrapping changed the error chain: %v", converted)
	}
	ordinary := context.DeadlineExceeded
	if got := queueDeliveryError(ordinary); got != ordinary {
		t.Fatalf("ordinary delivery error changed identity: %v", got)
	}
	if got := queueDeliveryError(nil); got != nil {
		t.Fatalf("successful delivery became an error: %v", got)
	}
}
