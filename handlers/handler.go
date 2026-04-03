package handlers

import (
	"context"
	"log"
	"queuebot/db"
	"sync"
	"time"

	"github.com/go-telegram/bot"
)

type BotHandler struct {
	db            *db.DBRepository
	queueMessages map[int]int
	mu            sync.RWMutex
	updateQueue   chan updateTask

	totalSlotsInQueue  int
	amountOfSlotsInRow int
}

func NewBotHandler(db *db.DBRepository, totalSlots int, slotsInRow int, delay time.Duration) *BotHandler {
	h := &BotHandler{
		db:                 db,
		queueMessages:      make(map[int]int),
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
