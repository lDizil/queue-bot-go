package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

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
	var (
		snapshots []queue.Snapshot
		selected  queue.RunReference
		text      string
		markup    *models.InlineKeyboardMarkup
		page      int
	)
	if query := update.CallbackQuery; query != nil {
		var ref queue.Reference
		if query.Data == "ManageQueue" {
			message := query.Message.Message
			if message == nil || h.db == nil {
				return
			}
			schedule, err := h.db.GetActiveScheduleByMessage(ctx, message.Chat.ID, message.ID)
			if err != nil {
				h.answer(ctx, b, query, queue.ErrorText(err))
				return
			}
			ref = queue.Reference{ScheduleID: schedule.ID, ChatID: message.Chat.ID, MessageID: message.ID}
		} else {
			parts := strings.Split(query.Data, ":")
			if len(parts) != 2 || parts[0] != "ManageQueue" {
				h.answer(ctx, b, query, "Некорректная кнопка очереди")
				return
			}
			var ok bool
			ref, ok = h.queueReference(ctx, b, update)
			if !ok {
				return
			}
		}
		if _, err := h.queues.Active(ctx, ref); err != nil {
			h.answer(ctx, b, query, queue.ErrorText(err))
			return
		}
		if h.chats != nil && query.Message.Message.Chat.Title != "" {
			_ = h.chats.Remember(ctx, ref.ChatID, query.Message.Message.Chat.Title)
		}
		h.enrichModerationChats(ctx)
		schedule, err := h.queues.Active(ctx, ref)
		if err != nil {
			h.answer(ctx, b, query, queue.ErrorText(err))
			return
		}
		selected = queue.RunReference{ScheduleID: schedule.ID, ChatID: schedule.ChatID, OpensAt: *schedule.ActiveOpensAt}
		snapshot, err := h.queues.InspectRun(ctx, selected)
		if err != nil {
			h.answer(ctx, b, query, queue.ErrorText(err))
			return
		}
		snapshots = []queue.Snapshot{snapshot}
		text, markup = h.moderationParticipants(ctx, snapshot)
	} else {
		var err error
		h.enrichModerationChats(ctx)
		snapshots, err = h.queues.OpenQueues(ctx)
		if err != nil {
			snapshots = nil
		}
		text, markup, page = h.moderationList(ctx, snapshots, 0)
		if err != nil {
			text = "Не удалось получить открытые очереди. Откройте меню заново."
		}
	}
	if _, err := h.sendMenu(ctx, b, update, "moderation", text, markup); err != nil {
		return
	}
	h.stateMu.Lock()
	session := h.userState[userID]
	session.moderationRuns = moderationReferences(snapshots)
	session.moderationRun, session.moderationSelected, session.moderationPage = selected, update.CallbackQuery != nil, page
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

func (h *BotHandler) enrichModerationChats(ctx context.Context) {
	if h.chats == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, _ = h.chats.List(ctx)
}

func (h *BotHandler) moderationList(ctx context.Context, snapshots []queue.Snapshot, page int) (string, *models.InlineKeyboardMarkup, int) {
	const (
		pageLimit = 5
		textLimit = 3500
		prefix    = "Управление участниками. Выберите открытую очередь."
	)
	var (
		blocks  []string
		pages   [][]int
		current []int
		size    = len(utf16.Encode([]rune(prefix))) + 100
	)
	names := make(map[int64]string)
	if h.db != nil {
		if known, err := h.db.ListChats(ctx); err == nil {
			for _, chat := range known {
				names[chat.ID] = chat.Name
			}
		}
	}
	for index, snapshot := range snapshots {
		block := fmt.Sprintf("№%d. %s", index+1, moderationQueueDetails(snapshot.Schedule, names[snapshot.Schedule.ChatID]))
		blockSize := len(utf16.Encode([]rune(block))) + 2
		if len(current) > 0 && (len(current) == pageLimit || size+blockSize > textLimit) {
			pages = append(pages, current)
			current = nil
			size = len(utf16.Encode([]rune(prefix))) + 100
		}
		blocks = append(blocks, block)
		current = append(current, index)
		size += blockSize
	}
	if len(current) > 0 {
		pages = append(pages, current)
	}
	if len(pages) == 0 {
		return "Сейчас нет открытых очередей. Меню обновляется автоматически.", &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "Закрыть управление", CallbackData: "ModerationLeave"}}}}, 0
	}
	page = max(0, min(page, len(pages)-1))
	var keyboard [][]models.InlineKeyboardButton
	text := prefix
	for _, index := range pages[page] {
		text += "\n\n" + blocks[index]
		schedule := snapshots[index].Schedule
		ref := queue.RunReference{ScheduleID: schedule.ID, ChatID: schedule.ChatID, OpensAt: *schedule.ActiveOpensAt}
		keyboard = append(keyboard, []models.InlineKeyboardButton{{Text: fmt.Sprintf("Открыть №%d", index+1), CallbackData: moderationSelectData(ref)}})
	}
	if len(pages) > 1 {
		text += fmt.Sprintf("\n\nСтраница %d из %d", page+1, len(pages))
		var navigation []models.InlineKeyboardButton
		if page > 0 {
			navigation = append(navigation, models.InlineKeyboardButton{Text: "← Назад", CallbackData: fmt.Sprintf("ModerationPage:%d", page-1)})
		}
		if page+1 < len(pages) {
			navigation = append(navigation, models.InlineKeyboardButton{Text: "Далее →", CallbackData: fmt.Sprintf("ModerationPage:%d", page+1)})
		}
		keyboard = append(keyboard, navigation)
	}
	keyboard = append(keyboard, []models.InlineKeyboardButton{{Text: "Закрыть управление", CallbackData: "ModerationLeave"}})
	return text, &models.InlineKeyboardMarkup{InlineKeyboard: keyboard}, page
}

func moderationSelectData(ref queue.RunReference) string {
	return fmt.Sprintf("ModerationSelect:%d:%d", ref.ScheduleID, ref.OpensAt.UnixMicro())
}

func moderationQueueDetails(schedule db.Schedule, groupName string) string {
	group := fmt.Sprintf("ID %d", schedule.ChatID)
	if groupName != "" {
		group = fmt.Sprintf("%s (ID %d)", queueLabel(groupName, 400), schedule.ChatID)
	}
	threadID := schedule.ThreadID
	if schedule.ActiveThreadID != nil {
		threadID = *schedule.ActiveThreadID
	}
	topic := fmt.Sprintf("ID %d", threadID)
	if schedule.ThreadDescription != nil && strings.TrimSpace(*schedule.ThreadDescription) != "" {
		topic = fmt.Sprintf("%s (ID %d)", queueLabel(strings.Join(strings.Fields(*schedule.ThreadDescription), " "), 1000), threadID)
	}
	kind := "Очередь по расписанию"
	if schedule.IsTemporary {
		kind = "Разовая очередь"
	}
	text := fmt.Sprintf("%s\nГруппа: %s\nТема: %s", kind, group, topic)
	if title := strings.Join(strings.Fields(schedule.Title), " "); title != "" {
		text += "\nНазвание: " + queueLabel(title, 400)
	}
	if schedule.ActiveOpensAt != nil && schedule.ActiveClosesAt != nil {
		moscow := time.FixedZone("МСК", 3*60*60)
		opensAt, closesAt := schedule.ActiveOpensAt.In(moscow), schedule.ActiveClosesAt.In(moscow)
		closing := closesAt.Format("15:04")
		if opensAt.Format("02.01.2006") != closesAt.Format("02.01.2006") {
			closing = closesAt.Format("02.01.2006 15:04")
		}
		text += "\nВремя: " + opensAt.Format("02.01.2006 15:04") + "–" + closing + " МСК"
	}
	return text
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
	var groupName string
	if h.db != nil {
		if chat, err := h.db.GetChat(ctx, snapshot.Schedule.ChatID); err == nil {
			groupName = chat.Name
		}
	}
	text := moderationQueueDetails(snapshot.Schedule, groupName) + "\n\nНажмите на участника, чтобы убрать его из очереди. Повторная запись разрешена."
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
		if entry.Username != "" {
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
	h.enrichModerationChats(ctx)
	h.showModerationList(ctx, b, update, session, 0)
}

func (h *BotHandler) ModerationPage(ctx context.Context, b *bot.Bot, update *models.Update) {
	session, ok := h.moderationSession(ctx, update)
	if !ok || session.moderationSelected {
		return
	}
	page, err := strconv.Atoi(strings.TrimPrefix(update.CallbackQuery.Data, "ModerationPage:"))
	if err != nil || page < 0 {
		h.answer(ctx, b, update.CallbackQuery, "Некорректная страница")
		return
	}
	h.showModerationList(ctx, b, update, session, page)
}

func (h *BotHandler) showModerationList(ctx context.Context, b *bot.Bot, update *models.Update, session userState, page int) {
	snapshots, err := h.queues.OpenQueues(ctx)
	if err != nil {
		h.answer(ctx, b, update.CallbackQuery, "Не удалось получить открытые очереди")
		return
	}
	text, markup, page := h.moderationList(ctx, snapshots, page)
	session.moderationSelected = false
	session.moderationRun = queue.RunReference{}
	session.moderationRuns = moderationReferences(snapshots)
	session.moderationPage = page
	session.enteredAt = time.Now()
	h.stateMu.Lock()
	h.userState[update.CallbackQuery.From.ID] = session
	h.stateMu.Unlock()
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
		text, markup, session.moderationPage = h.moderationList(ctx, snapshots, session.moderationPage)
		session.moderationRuns = moderationReferences(snapshots)
		h.stateMu.Lock()
		h.userState[userID] = session
		h.stateMu.Unlock()
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
