package handlers

import (
	"context"
	"log"
	"queuebot/db"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type BotHandler struct {
	db *db.DBRepository

	queueMessages map[int]int
	userState     map[int64]userState
	editMessages  map[int64]int

	queueMu sync.RWMutex
	stateMu sync.RWMutex
	editMu  sync.RWMutex

	updateQueue chan updateTask

	totalSlotsInQueue  int
	amountOfSlotsInRow int
}

type userState struct {
	state string
	scheduleID int
}

func NewBotHandler(db *db.DBRepository, totalSlots int, slotsInRow int, delay time.Duration) *BotHandler {
	h := &BotHandler{
		db:                 db,
		queueMessages:      make(map[int]int),
		userState:          make(map[int64]userState),
		editMessages:       make(map[int64]int),
		updateQueue:        make(chan updateTask, 100),
		totalSlotsInQueue:  totalSlots,
		amountOfSlotsInRow: slotsInRow,
	}

	go h.updateWorker(delay)

	return h
}

func (h *BotHandler) handleError(ctx context.Context, b *bot.Bot, callbackID string, msg string) {
	log.Println(msg)
	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: callbackID,
		Text:            "Что-то пошло не так, обратитесь к администратору бота",
		ShowAlert:       true,
	})
}

func (h *BotHandler) StateHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update == nil {
		return
	}

	if update.Message == nil {
    	return
	}

	userID := update.Message.From.ID

	h.stateMu.RLock()
	curState := h.userState[userID]
	h.stateMu.RUnlock()
	
	switch curState.state {
	case "awaiting_schedule":
		h.HandleScheduleInput(ctx, b, update)

	case "edit_start_time":
		h.HandleNewTime(ctx, b, update)

	case "edit_end_time":
		h.HandleNewTime(ctx, b, update)
	}	
}
