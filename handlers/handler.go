package handlers

import (
	"context"
	"log"
	"queuebot/chats"
	"queuebot/db"
	"queuebot/dispatch"
	"queuebot/queue"
	"queuebot/settings"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type BotHandler struct {
	db *db.DBRepository

	pendingSwaps     map[string]swapRequest
	userState        map[int64]userState
	editMessages     map[int64]int
	editMessageChats map[int64]int64

	swapMu  sync.RWMutex
	stateMu sync.RWMutex
	editMu  sync.RWMutex

	configMu     sync.Mutex
	editActionMu sync.Mutex
	settings     *settings.Manager
	queues       *queue.Service
	developerID  int64
	adminIDs     map[int64]bool
	chats        *chats.Service
	ctx          context.Context

	totalSlotsInQueue  int
	amountOfSlotsInRow int

	b *bot.Bot

	scheduler  SchedulerManager
	dispatcher *dispatch.Dispatcher
}

type SchedulerManager interface {
	ScheduleNext(schedule db.Schedule)
	RemoveSchedule(scheduleID int)
	RunInstant(ctx context.Context, chatID int64, threadID int, title string) error
	Reconfigure() error
}

type swapRequest struct {
	ID                string
	ScheduleID        int
	ChatID            int64
	ThreadID          int
	MessageID         int
	RequesterUserID   int64
	RequesterUsername string
	RequesterPosition int
	RequesterEntryID  int
	TargetUserID      int64
	TargetUsername    string
	TargetPosition    int
	TargetEntryID     int
	QueueMessageID    int
	ExpiresAt         time.Time
}

func (h *BotHandler) SetScheduler(s SchedulerManager) {
	h.scheduler = s
}

func (h *BotHandler) SetBot(b *bot.Bot) {
	h.b = b
	go h.expireSessions(h.ctx)
}

type userState struct {
	state        string
	scheduleID   int
	enteredAt    time.Time
	chatID       int64
	threadID     int
	menu         string
	configKey    string
	targetChatID int64
	chatSelected bool
}

func NewBotHandler(ctx context.Context, database *db.DBRepository, cfg *settings.Manager, developerID int64, adminIDs []int64) *BotHandler {
	values := cfg.Values()
	h := &BotHandler{
		db:                 database,
		pendingSwaps:       make(map[string]swapRequest),
		userState:          make(map[int64]userState),
		editMessages:       make(map[int64]int),
		editMessageChats:   make(map[int64]int64),
		totalSlotsInQueue:  values.TotalSlotsInQueue,
		amountOfSlotsInRow: values.AmountOfButtonsInRow,
		settings:           cfg,
		developerID:        developerID,
		adminIDs:           make(map[int64]bool),
		ctx:                ctx,
	}
	for _, id := range adminIDs {
		h.adminIDs[id] = true
	}
	h.adminIDs[developerID] = true
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
	h.editActionMu.Lock()
	defer h.editActionMu.Unlock()
	if update == nil {
		return
	}

	if update.Message == nil || update.Message.From == nil || update.Message.Text == "" || strings.HasPrefix(update.Message.Text, "/") {
		return
	}

	userID := update.Message.From.ID

	h.stateMu.RLock()
	curState := h.userState[userID]
	h.stateMu.RUnlock()
	if curState.chatID != update.Message.Chat.ID || curState.threadID != update.Message.MessageThreadID || time.Since(curState.enteredAt) > h.settings.Values().TimeForExpiredEditSes {
		return
	}
	if !h.adminIDs[userID] {
		return
	}
	h.stateMu.Lock()
	curState.enteredAt = time.Now()
	h.userState[userID] = curState
	h.stateMu.Unlock()

	switch curState.state {
	case "edit_start_time", "edit_end_time", "edit_thread_id", "edit_description", "edit_title":
		if _, err := h.selectedSchedule(ctx, userID, curState.scheduleID); err != nil {
			h.stateMu.Lock()
			curState.state, curState.scheduleID = "", 0
			h.userState[userID] = curState
			h.stateMu.Unlock()
			return
		}
	}

	switch curState.state {
	case "add_chat", "rename_chat":
		h.HandleChatInput(ctx, b, update)
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
	case "edit_title":
		h.HandleNewTitle(ctx, b, update)
	case "edit_config":
		h.HandleConfigInput(ctx, b, update)
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

func (h *BotHandler) SetQueueService(service *queue.Service) {
	h.queues = service
}

func (h *BotHandler) SerializeEdit(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		h.editActionMu.Lock()
		defer h.editActionMu.Unlock()
		if update.CallbackQuery != nil {
			q := update.CallbackQuery
			if q.Message.Message == nil || !h.ValidateEditSession(q.From.ID, q.Message.Message.Chat.ID, q.Message.Message.MessageThreadID, q.Message.Message.ID) || !h.validEditCallback(ctx, update) {
				h.answer(ctx, b, q, "Кнопка устарела. Откройте меню заново")
				return
			}
		}
		next(ctx, b, update)
	}
}

func (h *BotHandler) validEditCallback(ctx context.Context, update *models.Update) bool {
	query := update.CallbackQuery
	if query.Message.Message == nil {
		return false
	}
	parts := strings.Split(query.Data, ":")
	if len(parts) == 0 {
		return false
	}
	h.stateMu.RLock()
	session := h.userState[query.From.ID]
	h.stateMu.RUnlock()
	menu := "schedule"
	if parts[0] == "EditConfig" || parts[0] == "ConfigBack" || parts[0] == "ConfigLeave" {
		menu = "config"
	}
	if session.menu != menu {
		return false
	}
	var index int
	switch parts[0] {
	case "LeaveEditSchedule", "EditConfig", "ConfigBack", "ConfigLeave":
		return len(parts) == 1 || parts[0] == "EditConfig"
	case "AddChat", "ChatList":
		return len(parts) == 1
	case "ChatPage":
		if len(parts) != 2 {
			return false
		}
		page, err := strconv.Atoi(parts[1])
		return err == nil && page >= 0
	case "SelectChat", "AddNewSchedule", "RenameChat", "SetDefaultChat":
		if len(parts) != 2 {
			return false
		}
		target, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return false
		}
		if parts[0] == "SelectChat" {
			return true
		}
		return session.chatSelected && target != 0 && target == session.targetChatID && (parts[0] != "SetDefaultChat" || query.From.ID == h.developerID)
	case "SetCurrentWeek":
		return session.chatSelected && len(parts) == 3
	case "Back":
		if len(parts) == 2 && parts[1] == "mainmenu" {
			return session.chatSelected
		}
		if len(parts) != 3 {
			return false
		}
		index = 2
	default:
		if len(parts) < 2 {
			return false
		}
		index = 1
	}
	id, err := strconv.Atoi(parts[index])
	if err != nil || id <= 0 {
		return false
	}
	if (parts[0] == "EditTypeOfWeek" || parts[0] == "EditDayOfWeek" || parts[0] == "ChWeekDay") && len(parts) != 3 {
		return false
	}
	schedule, err := h.db.GetScheduleEntry(ctx, id)
	if err != nil {
		return false
	}
	return session.chatSelected && schedule.ChatID == session.targetChatID
}

func (h *BotHandler) expireSessions(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.isExpiredUserEditSes(h.settings.Values().TimeForExpiredEditSes)
		}
	}
}
