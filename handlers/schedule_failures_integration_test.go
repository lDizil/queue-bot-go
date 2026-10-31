package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"queuebot/db"
)

type scheduleFaultAPI struct {
	base       configTestAPI
	failMethod string
	failed     bool
	calls      []string
}

func (api *scheduleFaultAPI) Do(request *http.Request) (*http.Response, error) {
	method := path.Base(request.URL.Path)
	api.calls = append(api.calls, method)
	if method == api.failMethod && !api.failed {
		api.failed = true
		return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":false,"error_code":503,"description":"injected temporary Telegram failure"}`))}, nil
	}
	return api.base.Do(request)
}

func newScheduleTestHandler(t *testing.T, failMethod string) (*BotHandler, *bot.Bot, *scheduleFaultAPI) {
	t.Helper()
	handler, _, _, _ := newConfigTestHandler(t, "schedule")
	handler.db = scheduleTestRepository(t)
	api := &scheduleFaultAPI{failMethod: failMethod}
	telegram, err := handler.NewTelegramBot("1:test", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, api))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := handler.Shutdown(ctx); err != nil {
			t.Errorf("shutdown dispatcher: %v", err)
		}
	})
	handler.RegisterHandlers(telegram, "QueueBot")
	return handler, telegram, api
}

func TestScheduleCreateRetryDoesNotDuplicateAfterTelegramFailure(t *testing.T) {
	for _, method := range []string{"editMessageText", "sendMessage"} {
		t.Run(method, func(t *testing.T) {
			handler, telegram, api := newScheduleTestHandler(t, method)
			session := handler.userState[handler.developerID]
			session.state = "awaiting_schedule"
			handler.userState[handler.developerID] = session
			input := &models.Update{Message: configTestMessage(handler.developerID, "понедельник, нечётная, 11:00, 12:30, 7, Лабораторная, БГГ323")}
			processTelegramUpdates(t, handler, telegram, input)
			first, err := handler.db.GetAllSchedules(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(first) != 1 || !api.failed {
				t.Fatalf("precondition failed: first inserts=%d injected=%v", len(first), api.failed)
			}
			stateAfterFailure := handler.userState[handler.developerID].state
			processTelegramUpdates(t, handler, telegram, input)
			final, err := handler.db.GetAllSchedules(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("after committed INSERT and %s failure: pending_state=%q; after repeated input: actual rows=%d, HTTP methods=%v", method, stateAfterFailure, len(final), api.calls)
			if len(final) != 1 {
				t.Errorf("duplicate schedule after delivery failure: got %d rows, want 1", len(final))
			}
			if stateAfterFailure != "" {
				t.Errorf("committed creation kept pending input: %q", stateAfterFailure)
			}
		})
	}
}

func TestScheduleMenuReopenFailureCancelsPendingInput(t *testing.T) {
	handler, telegram, api := newScheduleTestHandler(t, "sendMessage")
	original := "Original description"
	id, err := handler.db.AddNewScheduleEntry(t.Context(), db.Schedule{ChatID: -100, ThreadID: 7, DayOfWeek: "monday", WeekType: "odd", StartTime: time.Now(), EndTime: time.Now().Add(time.Hour), ThreadDescription: &original})
	if err != nil {
		t.Fatal(err)
	}
	session := handler.userState[handler.developerID]
	session.state = "edit_description"
	session.scheduleID = id
	handler.userState[handler.developerID] = session
	processTelegramUpdates(t, handler, telegram, &models.Update{Message: configTestMessage(handler.developerID, "/edit_schedule")})
	if !api.failed || len(api.calls) != 2 || api.calls[0] != "deleteMessage" || api.calls[1] != "sendMessage" {
		t.Fatalf("precondition failed: HTTP methods=%v", api.calls)
	}
	stateAfterFailure := handler.userState[handler.developerID].state
	processTelegramUpdates(t, handler, telegram, &models.Update{Message: configTestMessage(handler.developerID, "Ordinary text after failed menu reopen")})
	final, err := handler.db.GetScheduleEntry(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	description := "<nil>"
	if final.ThreadDescription != nil {
		description = *final.ThreadDescription
	}
	t.Logf("after delete success/send failure: pending_state=%q; after ordinary text: database description=%q; HTTP methods=%v", stateAfterFailure, description, api.calls)
	if description != original {
		t.Errorf("ordinary text changed description through invisible pending session: got %q, want %q", description, original)
	}
	if stateAfterFailure != "" {
		t.Errorf("failed reopening kept pending input: %q", stateAfterFailure)
	}
	handler.stateMu.RLock()
	_, hasState := handler.userState[handler.developerID]
	handler.stateMu.RUnlock()
	handler.editMu.RLock()
	_, hasMenu := handler.editMessages[handler.developerID]
	_, hasChat := handler.editMessageChats[handler.developerID]
	handler.editMu.RUnlock()
	if hasState || hasMenu || hasChat {
		t.Errorf("failed reopening retained old session: state=%v menu=%v chat=%v", hasState, hasMenu, hasChat)
	}
}

func scheduleTestRepository(t *testing.T) *db.DBRepository {
	t.Helper()
	databaseURL := os.Getenv("QUEUEBOT_TEST_DATABASE_URL")
	if databaseURL == "" {
		if os.Getenv("QUEUEBOT_REQUIRE_DATABASE") == "1" {
			t.Fatal("QUEUEBOT_TEST_DATABASE_URL is required in integration CI")
		}
		t.Skip("set QUEUEBOT_TEST_DATABASE_URL to run queue integration tests")
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil || parsed.Scheme == "" {
		t.Fatal("invalid QUEUEBOT_TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect integration database: %s", scheduleTestDatabaseError(err, parsed))
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	schema := "queue_service_" + hex.EncodeToString(random[:])
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create isolated integration schema: %s", scheduleTestDatabaseError(err, parsed))
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop owned integration schema: %s", scheduleTestDatabaseError(err, parsed))
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	migration, err := migrate.New("file://../migrations", parsed.String())
	if err != nil {
		t.Fatalf("initialize isolated migrations: %s", scheduleTestDatabaseError(err, parsed))
	}
	err = migration.Up()
	sourceErr, databaseErr := migration.Close()
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("migrate isolated schema: %s", scheduleTestDatabaseError(err, parsed))
	}
	if sourceErr != nil || databaseErr != nil {
		t.Fatal("close isolated migrations")
	}
	poolConfig, err := pgxpool.ParseConfig(parsed.String())
	if err != nil {
		t.Fatal("parse isolated database configuration")
	}
	poolConfig.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatalf("connect isolated database: %s", scheduleTestDatabaseError(err, parsed))
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping isolated database: %s", scheduleTestDatabaseError(err, parsed))
	}
	return db.NewDBRepo(pool)
}

func scheduleTestDatabaseError(err error, databaseURL *url.URL) string {
	text := strings.ReplaceAll(err.Error(), databaseURL.String(), "<test database>")
	if databaseURL.User != nil {
		if password, exists := databaseURL.User.Password(); exists && password != "" {
			text = strings.ReplaceAll(text, password, "***")
		}
	}
	return text
}
