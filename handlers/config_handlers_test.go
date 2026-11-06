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
	"testing"
	"time"

	"queuebot/settings"
	"queuebot/utils"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func TestConfigHandlersRequireDeveloperIdentity(t *testing.T) {
	handler, telegram, api, store := newConfigTestHandler(t, "config")
	const attackerID = int64(123)
	handler.userState[attackerID] = userState{
		menu: "config", state: "edit_config", configKey: "total_slots_in_queue",
		enteredAt: time.Now(), chatID: -100, threadID: 7,
	}
	handler.editMessages[attackerID] = 500
	handler.editMessageChats[attackerID] = -100
	message := configTestMessage(attackerID, "31")
	handler.EditConfigReplyText(context.Background(), telegram, &models.Update{Message: message})
	handler.HandleConfigInput(context.Background(), telegram, &models.Update{Message: message})
	handler.EditConfigField(context.Background(), telegram, configTestCallback(attackerID, "EditConfig:total_slots_in_queue"))
	handler.ConfigBack(context.Background(), telegram, configTestCallback(attackerID, "ConfigBack"))
	handler.LeaveEditConfig(context.Background(), telegram, configTestCallback(attackerID, "ConfigLeave"))
	if store.saves != 0 || len(api.requests) != 0 {
		t.Fatal("non-developer could use configuration handlers")
	}
}

func TestOpeningConfigReplacesPreviousSessionInTheSameTopic(t *testing.T) {
	handler, telegram, api, _ := newConfigTestHandler(t, "schedule")
	session := handler.userState[handler.developerID]
	session.state = "edit_description"
	handler.userState[handler.developerID] = session
	handler.EditConfigReplyText(context.Background(), telegram, &models.Update{Message: configTestMessage(handler.developerID, "/edit_config")})
	session = handler.userState[handler.developerID]
	if session.menu != "config" || session.state != "" || session.chatID != -100 || session.threadID != 7 {
		t.Fatalf("old edit state survived opening configuration: %+v", session)
	}
	if len(api.requests) != 3 || api.requests[0].method != "deleteMessage" || api.requests[1].method != "sendMessage" || api.requests[1].values["message_thread_id"] != "7" || api.requests[2].method != "editEphemeralMessageText" {
		t.Fatalf("configuration did not replace the previous menu in its topic: %+v", api.requests)
	}
}

func TestConfigInputStaysWithinItsLiveChatAndTopic(t *testing.T) {
	tests := []struct {
		name   string
		change func(*BotHandler, *models.Message)
	}{
		{"different chat", func(_ *BotHandler, message *models.Message) { message.Chat.ID = -200 }},
		{"different topic", func(_ *BotHandler, message *models.Message) { message.MessageThreadID = 8 }},
		{"expired session", func(handler *BotHandler, _ *models.Message) {
			session := handler.userState[handler.developerID]
			session.enteredAt = time.Now().Add(-time.Hour)
			handler.userState[handler.developerID] = session
		}},
		{"schedule menu", func(handler *BotHandler, _ *models.Message) {
			session := handler.userState[handler.developerID]
			session.menu = "schedule"
			handler.userState[handler.developerID] = session
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, telegram, api, store := newConfigTestHandler(t, "config")
			beginConfigInput(handler, "total_slots_in_queue")
			message := configTestMessage(handler.developerID, "31")
			test.change(handler, message)
			handler.HandleConfigInput(context.Background(), telegram, &models.Update{Message: message})
			if store.saves != 0 || len(api.requests) != 0 {
				t.Fatal("input outside the live session changed configuration")
			}
		})
	}
}

func TestConfigInputValidatesBeforeSavingAndEditsExistingMenu(t *testing.T) {
	handler, telegram, api, store := newConfigTestHandler(t, "config")
	beginConfigInput(handler, "total_slots_in_queue")
	handler.HandleConfigInput(context.Background(), telegram, &models.Update{Message: configTestMessage(handler.developerID, "0")})
	if store.saves != 0 || handler.settings.Values().TotalSlotsInQueue != 30 {
		t.Fatal("invalid slot count reached storage or published settings")
	}
	if handler.userState[handler.developerID].state != "edit_config" {
		t.Fatal("invalid input unexpectedly ended field editing")
	}
	handler.HandleConfigInput(context.Background(), telegram, &models.Update{Message: configTestMessage(handler.developerID, "31")})
	if store.saves != 1 || handler.settings.Values().TotalSlotsInQueue != 31 {
		t.Fatal("valid slot count was not saved and published")
	}
	if handler.userState[handler.developerID].state != "" || handler.userState[handler.developerID].configKey != "" {
		t.Fatal("saved setting left pending input enabled")
	}
	for _, request := range api.requests {
		if request.method != "editMessageText" || request.values["message_id"] != "500" {
			t.Fatalf("configuration spawned another message instead of editing its menu: %+v", request)
		}
	}
}

func TestConfigSaveFailureKeepsPreviousValueAndHidesStorageDetails(t *testing.T) {
	handler, telegram, api, store := newConfigTestHandler(t, "config")
	beginConfigInput(handler, "total_slots_in_queue")
	const sensitiveDetails = "postgres://user:private-password@database/internal"
	store.saveError = errors.New(sensitiveDetails)
	handler.HandleConfigInput(context.Background(), telegram, &models.Update{Message: configTestMessage(handler.developerID, "31")})
	if handler.settings.Values().TotalSlotsInQueue != 30 || handler.userState[handler.developerID].state != "edit_config" {
		t.Fatal("storage failure changed settings or closed the retry prompt")
	}
	for _, request := range api.requests {
		for _, value := range request.values {
			if strings.Contains(value, sensitiveDetails) || strings.Contains(value, "private-password") {
				t.Fatal("storage details were sent to Telegram")
			}
		}
	}
}

func TestConfigBackCancelsPendingInputAndRejectsOldMessage(t *testing.T) {
	handler, telegram, _, store := newConfigTestHandler(t, "config")
	beginConfigInput(handler, "total_slots_in_queue")
	stale := configTestCallback(handler.developerID, "EditConfig:amount_of_buttons_in_row")
	stale.CallbackQuery.Message.Message.ID = 499
	handler.EditConfigField(context.Background(), telegram, stale)
	if got := handler.userState[handler.developerID].configKey; got != "total_slots_in_queue" {
		t.Fatalf("stale menu changed selected field to %q", got)
	}
	handler.ConfigBack(context.Background(), telegram, configTestCallback(handler.developerID, "ConfigBack"))
	handler.HandleConfigInput(context.Background(), telegram, &models.Update{Message: configTestMessage(handler.developerID, "31")})
	if store.saves != 0 || handler.userState[handler.developerID].state != "" {
		t.Fatal("back did not cancel pending configuration input")
	}
}

func TestConfigWhitelistRejectsSecretsAccessAndStartupOnlyKeys(t *testing.T) {
	handler, telegram, _, store := newConfigTestHandler(t, "config")
	for _, key := range []string{"bot_token", "db_password", "authorized_user_id", "developer_user_id", "update_workers"} {
		callback := configTestCallback(handler.developerID, "EditConfig:"+key)
		handler.EditConfigField(context.Background(), telegram, callback)
		if handler.userState[handler.developerID].state != "" || store.saves != 0 {
			t.Fatalf("restricted key %q is editable", key)
		}
		if _, exists := findConfigField(key); exists {
			t.Fatalf("restricted key %q is displayed", key)
		}
	}
}

func TestCurrentWeekTargetIsIdempotentAndRejectsStaleMonday(t *testing.T) {
	var (
		values = settings.Defaults()
		now    = time.Date(2026, time.April, 12, 21, 0, 0, 0, time.UTC)
	)
	for range 2 {
		if err := setCurrentWeek(&values, "2026-04-13", "odd", now); err != nil {
			t.Fatal(err)
		}
		anchor, err := time.Parse("2006-01-02", values.Week1Date)
		if err != nil {
			t.Fatal(err)
		}
		if got := utils.WeekTypeForDate(now, anchor, values.Week1Type); got != "odd" {
			t.Fatalf("explicit target changed on repeated click: %s", got)
		}
		if got := utils.WeekTypeForDate(now.AddDate(0, 0, 7), anchor, values.Week1Type); got != "even" {
			t.Fatalf("following week did not alternate: %s", got)
		}
	}
	original := values
	for _, target := range []struct{ monday, parity string }{{"2026-04-06", "even"}, {"2026-04-13", "unknown"}} {
		if err := setCurrentWeek(&values, target.monday, target.parity, now); !errors.Is(err, errInvalidConfigInput) {
			t.Fatalf("invalid target accepted: %+v", target)
		}
		if values != original {
			t.Fatal("invalid target mutated calendar")
		}
	}
}

func TestToggleCurrentWeekEditsMenuAndUsesExplicitTarget(t *testing.T) {
	handler, telegram, api, store := newConfigTestHandler(t, "schedule")
	monday := configWeekMonday(time.Now()).Format("2006-01-02")
	callback := configTestCallback(handler.developerID, "SetCurrentWeek:"+monday+":even")
	for range 2 {
		handler.ToggleCurrentWeek(context.Background(), telegram, callback)
		if got := handler.settings.Values(); got.Week1Date != monday || got.Week1Type != "even" {
			t.Fatalf("explicit current-week target not persisted: %+v", got)
		}
	}
	if store.saves != 2 {
		t.Fatalf("calendar writes = %d, want 2", store.saves)
	}
	for _, request := range api.requests {
		if request.method == "sendMessage" {
			t.Fatal("week toggle sent an extra message")
		}
	}
}

func beginConfigInput(handler *BotHandler, key string) {
	session := handler.userState[handler.developerID]
	session.state = "edit_config"
	session.configKey = key
	handler.userState[handler.developerID] = session
}

func newConfigTestHandler(t *testing.T, menu string) (*BotHandler, *bot.Bot, *configTestAPI, *configTestStore) {
	t.Helper()
	store := &configTestStore{values: settings.Defaults()}
	manager, err := settings.NewManager(context.Background(), store, settings.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	api := &configTestAPI{}
	telegram, err := bot.New("1:test", bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, api))
	if err != nil {
		t.Fatal(err)
	}
	const developerID = int64(789915524)
	handler := &BotHandler{
		settings:         manager,
		developerID:      developerID,
		access:           &testAccessControl{admins: map[int64]bool{developerID: true}},
		userState:        map[int64]userState{developerID: {menu: menu, enteredAt: time.Now(), chatID: -100, threadID: 7}},
		editMessages:     map[int64]int{developerID: 500},
		editMessageChats: map[int64]int64{developerID: -100},
	}
	return handler, telegram, api, store
}

func configTestMessage(userID int64, text string) *models.Message {
	return &models.Message{ID: 500, From: &models.User{ID: userID}, Chat: models.Chat{ID: -100}, MessageThreadID: 7, Text: text}
}

func configTestCallback(userID int64, data string) *models.Update {
	return &models.Update{CallbackQuery: &models.CallbackQuery{
		ID: "test-callback", From: models.User{ID: userID}, Data: data,
		Message: models.MaybeInaccessibleMessage{Message: configTestMessage(userID, "")},
	}}
}

type configTestStore struct {
	values    settings.Values
	saves     int
	saveError error
}

func (s *configTestStore) LoadSettings(context.Context) (settings.Values, bool, error) {
	return s.values, true, nil
}

func (s *configTestStore) SaveSettings(_ context.Context, values settings.Values) error {
	s.saves++
	if s.saveError != nil {
		return s.saveError
	}
	s.values = values
	return nil
}

type configTestRequest struct {
	method string
	values map[string]string
}

type configTestAPI struct {
	requests []configTestRequest
}

func (api *configTestAPI) Do(request *http.Request) (*http.Response, error) {
	if err := request.ParseMultipartForm(64 << 10); err != nil {
		return nil, err
	}
	values := make(map[string]string)
	for key, fieldValues := range request.MultipartForm.Value {
		values[key] = fieldValues[0]
	}
	method := path.Base(request.URL.Path)
	api.requests = append(api.requests, configTestRequest{method: method, values: values})
	var result any = true
	if method == "sendMessage" || method == "editMessageText" {
		messageID, _ := strconv.Atoi(values["message_id"])
		if messageID == 0 {
			messageID = 500
		}
		message := map[string]any{"message_id": messageID, "chat": map[string]any{"id": -100}, "message_thread_id": 7}
		if values["ephemeral_message_parameters"] != "" {
			message["message_id"] = 0
			message["ephemeral_message_id"] = messageID
		}
		result = message
	}
	body, err := json.Marshal(map[string]any{"ok": true, "result": result})
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
}
