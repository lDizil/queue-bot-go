package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"queuebot/access"
	"queuebot/db"
	"queuebot/queue"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (h *BotHandler) ManageQueue(ctx context.Context, b *bot.Bot, update *models.Update) {
	userID := updateUserID(update)
	if userID == 0 || h.queues == nil {
		return
	}
	snapshots, err := h.queues.OpenQueues(ctx)
	if err != nil {
		snapshots = nil
	}
	text, markup := h.moderationList(ctx, snapshots)
	if err != nil {
		text = "Не удалось получить открытые очереди. Откройте меню заново."
	}
	if _, err := h.sendMenu(ctx, b, update, "moderation", text, markup); err != nil {
		return
	}
	h.stateMu.Lock()
	session := h.userState[userID]
	session.moderationRuns = moderationReferences(snapshots)
	h.userState[userID] = session
	h.stateMu.Unlock()
	generation := h.MenuGeneration(userID)
	if generation != "" {
		go h.refreshModerationMenu(b, userID, generation, moderationContent(text, markup))
	}
	h.answer(ctx, b, update.CallbackQuery, "")
}

func moderationReferences(snapshots []queue.Snapshot) []queue.RunReference {
	var refs []queue.RunReference
	for _, snapshot := range snapshots {
		schedule := snapshot.Schedule
		if schedule.ActiveOpensAt != nil {
			refs = append(refs, queue.RunReference{ScheduleID: schedule.ID, ChatID: schedule.ChatID, OpensAt: *schedule.ActiveOpensAt})
		}
	}
	return refs
}

func (h *BotHandler) moderationList(ctx context.Context, snapshots []queue.Snapshot) (string, *models.InlineKeyboardMarkup) {
	var keyboard [][]models.InlineKeyboardButton
	for _, snapshot := range snapshots {
		schedule := snapshot.Schedule
		if schedule.ActiveOpensAt == nil {
			continue
		}
		ref := queue.RunReference{ScheduleID: schedule.ID, ChatID: schedule.ChatID, OpensAt: *schedule.ActiveOpensAt}
		keyboard = append(keyboard, []models.InlineKeyboardButton{{Text: h.moderationQueueLabel(ctx, schedule), CallbackData: moderationSelectData(ref)}})
	}
	keyboard = append(keyboard, []models.InlineKeyboardButton{{Text: "Закрыть управление", CallbackData: "ModerationLeave"}})
	text := "Управление участниками. Выберите открытую очередь."
	if len(snapshots) == 0 {
		text = "Сейчас нет открытых очередей. Меню обновляется автоматически."
	}
	return text, &models.InlineKeyboardMarkup{InlineKeyboard: keyboard}
}

func moderationSelectData(ref queue.RunReference) string {
	return fmt.Sprintf("ModerationSelect:%d:%d", ref.ScheduleID, ref.OpensAt.UnixMicro())
}

func (h *BotHandler) moderationQueueLabel(ctx context.Context, schedule db.Schedule) string {
	group := fmt.Sprintf("Чат %d", schedule.ChatID)
	if h.db != nil {
		if chat, err := h.db.GetChat(ctx, schedule.ChatID); err == nil {
			group = chatName(chat)
		}
	}
	threadID := schedule.ThreadID
	if schedule.ActiveThreadID != nil {
		threadID = *schedule.ActiveThreadID
	}
	topic := fmt.Sprintf("Тема %d", threadID)
	if schedule.ThreadDescription != nil && strings.TrimSpace(*schedule.ThreadDescription) != "" {
		topic = strings.Join(strings.Fields(*schedule.ThreadDescription), " ")
	}
	title := strings.Join(strings.Fields(schedule.Title), " ")
	if title == "" {
		title = "Очередь без заголовка"
	}
	window := ""
	if schedule.ActiveOpensAt != nil && schedule.ActiveClosesAt != nil {
		moscow := time.FixedZone("МСК", 3*60*60)
		window = schedule.ActiveOpensAt.In(moscow).Format("02.01 15:04") + "–" + schedule.ActiveClosesAt.In(moscow).Format("15:04") + " МСК"
	}
	return fmt.Sprintf("%s · %s · %s · %s", queueLabel(group, 40), queueLabel(topic, 32), queueLabel(title, 48), window)
}

func (h *BotHandler) SelectModerationQueue(ctx context.Context, b *bot.Bot, update *models.Update) {
	session, ok := h.moderationSession(ctx, update)
	if !ok {
		return
	}
	query := update.CallbackQuery
	var selected queue.RunReference
	for _, ref := range session.moderationRuns {
		if moderationSelectData(ref) == query.Data {
			selected = ref
			break
		}
	}
	if selected.ScheduleID == 0 {
		h.answer(ctx, b, query, "Кнопка устарела. Выберите очередь заново")
		return
	}
	snapshot, err := h.queues.InspectRun(ctx, selected)
	if err != nil {
		h.answer(ctx, b, query, queue.ErrorText(err))
		return
	}
	session.moderationRun, session.moderationSelected = selected, true
	session.enteredAt = time.Now()
	h.stateMu.Lock()
	h.userState[query.From.ID] = session
	h.stateMu.Unlock()
	text, markup := h.moderationParticipants(ctx, snapshot)
	_ = h.editMenu(ctx, b, query.From.ID, text, markup)
	h.answer(ctx, b, query, "")
}

func (h *BotHandler) moderationSession(ctx context.Context, update *models.Update) (userState, bool) {
	if update == nil || update.CallbackQuery == nil || !h.menuCallbackMatches(ctx, update) {
		return userState{}, false
	}
	h.stateMu.RLock()
	session := h.userState[update.CallbackQuery.From.ID]
	h.stateMu.RUnlock()
	return session, session.menu == "moderation"
}

func (h *BotHandler) moderationParticipants(ctx context.Context, snapshot queue.Snapshot) (string, *models.InlineKeyboardMarkup) {
	text := h.moderationQueueLabel(ctx, snapshot.Schedule) + "\n\nНажмите на участника, чтобы убрать его из очереди. Повторная запись разрешена."
	if len(snapshot.Entries) == 0 {
		text += "\n\nОчередь пуста."
	}
	columns := snapshot.Schedule.ActiveColumns
	if columns < 1 {
		columns = 3
	}
	columns = min(columns, 3)
	var keyboard [][]models.InlineKeyboardButton
	var row []models.InlineKeyboardButton
	for _, entry := range snapshot.Entries {
		name := strings.Join(strings.Fields(entry.DisplayName), " ")
		if name == "" && entry.Username != "" {
			name = "@" + entry.Username
		}
		if name == "" {
			name = fmt.Sprintf("Участник %d", entry.UserID)
		}
		mark := ""
		if entry.Completed {
			mark = " ✅"
		}
		row = append(row, models.InlineKeyboardButton{Text: fmt.Sprintf("%d. %s%s", entry.Position, queueLabel(name, 30), mark), CallbackData: fmt.Sprintf("ModerationRemove:%d", entry.ID)})
		if len(row) == columns {
			keyboard = append(keyboard, row)
			row = nil
		}
	}
	if len(row) > 0 {
		keyboard = append(keyboard, row)
	}
	keyboard = append(keyboard, moderationNavigation()...)
	return text, &models.InlineKeyboardMarkup{InlineKeyboard: keyboard}
}

func moderationNavigation() [][]models.InlineKeyboardButton {
	return [][]models.InlineKeyboardButton{
		{{Text: "К списку очередей", CallbackData: "ModerationBack"}},
		{{Text: "Закрыть управление", CallbackData: "ModerationLeave"}},
	}
}

func (h *BotHandler) RemoveParticipant(ctx context.Context, b *bot.Bot, update *models.Update) {
	session, ok := h.moderationSession(ctx, update)
	if !ok || !session.moderationSelected {
		return
	}
	query := update.CallbackQuery
	entryID, err := strconv.Atoi(strings.TrimPrefix(query.Data, "ModerationRemove:"))
	if err != nil || entryID <= 0 {
		h.answer(ctx, b, query, "Некорректная кнопка участника")
		return
	}
	if err := h.queues.Remove(ctx, session.moderationRun, query.From.ID, entryID); err != nil {
		h.answer(ctx, b, query, queue.ErrorText(err))
		return
	}
	h.stateMu.Lock()
	session.enteredAt = time.Now()
	h.userState[query.From.ID] = session
	h.stateMu.Unlock()
	h.answer(ctx, b, query, "Участник убран из очереди")
}

func (h *BotHandler) ModerationBack(ctx context.Context, b *bot.Bot, update *models.Update) {
	session, ok := h.moderationSession(ctx, update)
	if !ok {
		return
	}
	snapshots, err := h.queues.OpenQueues(ctx)
	if err != nil {
		h.answer(ctx, b, update.CallbackQuery, "Не удалось получить открытые очереди")
		return
	}
	session.moderationSelected = false
	session.moderationRun = queue.RunReference{}
	session.moderationRuns = moderationReferences(snapshots)
	session.enteredAt = time.Now()
	h.stateMu.Lock()
	h.userState[update.CallbackQuery.From.ID] = session
	h.stateMu.Unlock()
	text, markup := h.moderationList(ctx, snapshots)
	_ = h.editMenu(ctx, b, update.CallbackQuery.From.ID, text, markup)
	h.answer(ctx, b, update.CallbackQuery, "")
}

func (h *BotHandler) LeaveModeration(ctx context.Context, b *bot.Bot, update *models.Update) {
	if _, ok := h.moderationSession(ctx, update); !ok {
		return
	}
	_ = h.closeMenu(ctx, b, update.CallbackQuery.From.ID)
	h.answer(ctx, b, update.CallbackQuery, "Управление закрыто")
}

func (h *BotHandler) refreshModerationMenu(b *bot.Bot, userID int64, generation, lastContent string) {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	ctx := h.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		h.editActionMu.Lock()
		content, active := h.refreshModerationSession(ctx, b, userID, generation, lastContent)
		h.editActionMu.Unlock()
		if !active {
			return
		}
		lastContent = content
		timer.Reset(time.Second)
	}
}

func (h *BotHandler) refreshModerationSession(ctx context.Context, b *bot.Bot, userID int64, generation, lastContent string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if h.MenuGeneration(userID) != generation {
		return lastContent, false
	}
	h.stateMu.RLock()
	session := h.userState[userID]
	h.stateMu.RUnlock()
	if session.menu != "moderation" {
		return lastContent, false
	}
	if time.Since(session.enteredAt) > h.settings.Values().TimeForExpiredEditSes || !h.allowed(ctx, userID, access.RemoveParticipants) {
		_ = h.closeMenu(ctx, b, userID)
		return lastContent, false
	}
	var text string
	var markup *models.InlineKeyboardMarkup
	if session.moderationSelected {
		snapshot, err := h.queues.InspectRun(ctx, session.moderationRun)
		switch {
		case err == nil:
			text, markup = h.moderationParticipants(ctx, snapshot)
		case errors.Is(err, queue.ErrClosed), errors.Is(err, queue.ErrStale):
			text = "Эта очередь закрыта. Выберите другую открытую очередь."
			markup = &models.InlineKeyboardMarkup{InlineKeyboard: moderationNavigation()}
		default:
			text = "Не удалось обновить очередь. Повторяем попытку."
			markup = &models.InlineKeyboardMarkup{InlineKeyboard: moderationNavigation()}
		}
	} else {
		snapshots, err := h.queues.OpenQueues(ctx)
		if err != nil {
			return lastContent, true
		}
		session.moderationRuns = moderationReferences(snapshots)
		h.stateMu.Lock()
		h.userState[userID] = session
		h.stateMu.Unlock()
		text, markup = h.moderationList(ctx, snapshots)
	}
	content := moderationContent(text, markup)
	if content == lastContent {
		return lastContent, true
	}
	if err := h.editMenuGeneration(ctx, b, userID, generation, text, markup); err != nil {
		return lastContent, true
	}
	return content, true
}

func moderationContent(text string, markup *models.InlineKeyboardMarkup) string {
	encoded, _ := json.Marshal(markup)
	return text + string(encoded)
}
