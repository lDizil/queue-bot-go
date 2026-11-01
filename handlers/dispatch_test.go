package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func TestTelegramDispatchKeepsMenuAndFieldInputInOrder(t *testing.T) {
	handler, telegram, _, store := newTelegramAdapter(t)
	for i := range 32 {
		field := adapterCallback(handler.developerID, "EditConfig:total_slots_in_queue")
		field.CallbackQuery.Message.Message.ID += i
		processTelegramUpdates(t, handler, telegram,
			&models.Update{Message: adapterMessage(handler.developerID, "/edit_config@QueueBot")},
			field,
			&models.Update{Message: adapterMessage(handler.developerID, "31")})
		if got := handler.settings.Values().TotalSlotsInQueue; got != 31 {
			t.Fatalf("field input overtook opening the menu: slots=%d", got)
		}
	}
	if store.saves != 32 {
		t.Fatalf("field input was lost or duplicated: saves=%d, want 32", store.saves)
	}
}

func TestTelegramDispatchCancelsInputBeforeFollowingOrdinaryText(t *testing.T) {
	handler, telegram, _, store := newTelegramAdapter(t)
	for i := range 32 {
		field := adapterCallback(handler.developerID, "EditConfig:total_slots_in_queue")
		back := adapterCallback(handler.developerID, "ConfigBack")
		field.CallbackQuery.Message.Message.ID += i
		back.CallbackQuery.Message.Message.ID += i
		processTelegramUpdates(t, handler, telegram,
			&models.Update{Message: adapterMessage(handler.developerID, "/edit_config")},
			field,
			back,
			&models.Update{Message: adapterMessage(handler.developerID, "31")})
	}
	if store.saves != 0 || handler.settings.Values().TotalSlotsInQueue != 30 {
		t.Fatalf("ordinary text changed configuration after Back: slots=%d saves=%d", handler.settings.Values().TotalSlotsInQueue, store.saves)
	}
}

func TestTelegramDispatchSharesTopicLaneAcrossParticipants(t *testing.T) {
	handler, telegram, api, _ := newTelegramAdapter(t)
	var (
		started = make(chan struct{})
		release = make(chan struct{})
		key     = "topic:-100:7"
	)
	defer close(release)
	if err := handler.dispatcher.Submit(t.Context(), key, func(ctx context.Context) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("topic lane did not start")
	}
	telegram.ProcessUpdate(t.Context(), adapterCallback(123, "CloseQueue"))
	telegram.ProcessUpdate(t.Context(), adapterCallback(456, "CloseQueue"))

	for _, topic := range []struct {
		chatID   int64
		threadID int
	}{{-100, 8}, {-200, 7}} {
		update := adapterCallback(123, "CloseQueue")
		update.CallbackQuery.Message.Message.Chat.ID = topic.chatID
		update.CallbackQuery.Message.Message.MessageThreadID = topic.threadID
		processTelegramUpdates(t, handler, telegram, update)
	}
	if requests := api.history(); len(requests) != 2 {
		t.Fatalf("same-topic actions ran before their predecessor or another topic was blocked: requests=%+v", requests)
	}
}

func adapterCallback(userID int64, data string) *models.Update {
	message := adapterMessage(userID, "")
	message.ID = 1001
	return &models.Update{CallbackQuery: &models.CallbackQuery{
		ID: data, From: models.User{ID: userID}, Data: data,
		Message: models.MaybeInaccessibleMessage{Message: message},
	}}
}

func processTelegramUpdates(t *testing.T, handler *BotHandler, telegram *bot.Bot, updates ...*models.Update) {
	t.Helper()
	keys := make(map[string]bool)
	for _, update := range updates {
		telegram.ProcessUpdate(t.Context(), update)
		keys[updateKey(update)] = true
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for key := range keys {
		completed := make(chan struct{})
		if err := handler.dispatcher.Submit(ctx, key, func(context.Context) { close(completed) }); err != nil {
			t.Fatalf("enqueue update barrier: %v", err)
		}
		select {
		case <-completed:
		case <-ctx.Done():
			t.Fatalf("updates did not finish: %v", ctx.Err())
		}
	}
}
