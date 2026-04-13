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

	queueMessages    map[int]int
	userState        map[int64]userState
	editMessages     map[int64]int
	editMessageChats map[int64]int64

	queueMu sync.RWMutex
	stateMu sync.RWMutex
	editMu  sync.RWMutex

	updateQueue chan updateTask

	totalSlotsInQueue  int
	amountOfSlotsInRow int

	b *bot.Bot

	scheduler SchedulerManager

	week1Date time.Time
	week1Type string
}

type SchedulerManager interface {
	ScheduleNext(schedule db.Schedule)
	RemoveSchedule(scheduleID int)
	RunInstant(ctx context.Context, threadID int)
}

func (h *BotHandler) SetScheduler(s SchedulerManager) {
	h.scheduler = s
}

func (h *BotHandler) SetBot(b *bot.Bot) {
	h.b = b
}

type userState struct {
	state      string
	scheduleID int
	enteredAt  time.Time
	chatID     int64
	threadID   int
}

func NewBotHandler(db *db.DBRepository, totalSlots int, slotsInRow int, delay time.Duration, timeForExpiredEditSes time.Duration, week1Date time.Time, week1Type string) *BotHandler {
	h := &BotHandler{
		db:                 db,
		queueMessages:      make(map[int]int),
		userState:          make(map[int64]userState),
		editMessages:       make(map[int64]int),
		editMessageChats:   make(map[int64]int64),
		updateQueue:        make(chan updateTask, 100),
		totalSlotsInQueue:  totalSlots,
		amountOfSlotsInRow: slotsInRow,
		week1Date:          week1Date,
		week1Type:          week1Type,
	}

	ctx := context.Background()
	schedules, err := db.GetAllSchedules(ctx)
	if err != nil {
		log.Println("Предупреждение: не удалось загрузить queue_message_id из БД:", err)
	} else {
		for _, s := range schedules {
			if s.QueueMesID != nil {
				h.queueMessages[s.ID] = *s.QueueMesID
			}
		}
	}

	go h.updateWorker(delay, timeForExpiredEditSes)

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

	case "edit_thread_id":
		h.HandleNewThreadID(ctx, b, update)

	case "edit_description":
		h.HandleNewDescription(ctx, b, update)
	}
}

func (h *BotHandler) getAllReplyInfo(update *models.Update) (*models.CallbackQuery, string, int64, int64, string) {
	query := update.CallbackQuery
	data := query.Data

	chatID := query.Message.Message.Chat.ID
	username := query.From.Username
	userID := query.From.ID

	return query, data, chatID, userID, username
}

func (h *BotHandler) SetQueueMessage(scheduleID, messageID int) {
	h.queueMu.Lock()
	h.queueMessages[scheduleID] = messageID
	h.queueMu.Unlock()
}
