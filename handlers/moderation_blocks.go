package handlers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"queuebot/db"
	"queuebot/queue"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (h *BotHandler) SetModerationMode(ctx context.Context, b *bot.Bot, update *models.Update) {
	session, ok := h.moderationSession(ctx, update)
	if !ok || !session.moderationSelected || session.moderationBlocks {
		return
	}
	query := update.CallbackQuery
	target, err := strconv.ParseInt(strings.TrimPrefix(query.Data, "ModerationMode:"), 10, 64)
	if err != nil || target != 0 && time.Duration(target) != session.moderationBlockDuration {
		h.answer(ctx, b, query, "Кнопка устарела. Откройте управление заново")
		return
	}
	snapshot, err := h.queues.InspectRun(ctx, session.moderationRun)
	if err != nil {
		h.answer(ctx, b, query, queue.ErrorText(err))
		return
	}
	session.moderationBlockEnabled = target != 0
	session.enteredAt = time.Now()
	h.stateMu.Lock()
	h.userState[query.From.ID] = session
	h.stateMu.Unlock()
	text, markup := h.moderationParticipants(ctx, snapshot, session)
	_ = h.editMenu(ctx, b, query.From.ID, text, markup)
	h.answer(ctx, b, query, "")
}

func moderationRemovalDuration(session userState) time.Duration {
	if session.moderationBlockEnabled {
		return session.moderationBlockDuration
	}
	return 0
}

func moderationDuration(duration time.Duration) string {
	switch {
	case duration%time.Hour == 0:
		return fmt.Sprintf("%d ч", duration/time.Hour)
	case duration%time.Minute == 0:
		return fmt.Sprintf("%d мин", duration/time.Minute)
	case duration%time.Second == 0:
		return fmt.Sprintf("%d с", duration/time.Second)
	default:
		return configDuration(duration)
	}
}

func (h *BotHandler) ModerationBlocks(ctx context.Context, b *bot.Bot, update *models.Update) {
	session, ok := h.moderationSession(ctx, update)
	if !ok || !session.moderationSelected {
		return
	}
	session.moderationBlocksPage = 0
	h.showModerationBlocks(ctx, b, update, session)
}

func (h *BotHandler) showModerationBlocks(ctx context.Context, b *bot.Bot, update *models.Update, session userState) {
	query := update.CallbackQuery
	snapshot, err := h.queues.InspectRun(ctx, session.moderationRun)
	if err != nil {
		h.answer(ctx, b, query, queue.ErrorText(err))
		return
	}
	blocks, err := h.queues.Blocks(ctx, session.moderationRun, query.From.ID)
	if err != nil {
		h.answer(ctx, b, query, queue.ErrorText(err))
		return
	}
	session.moderationBlocks = true
	session.enteredAt = time.Now()
	text, markup := moderationBlockList(h.moderationHeader(ctx, snapshot.Schedule), blocks, &session)
	h.stateMu.Lock()
	h.userState[query.From.ID] = session
	h.stateMu.Unlock()
	_ = h.editMenu(ctx, b, query.From.ID, text, markup)
	h.answer(ctx, b, query, "")
}

func moderationBlockList(header string, blocks []db.QueueBlock, session *userState) (string, *models.InlineKeyboardMarkup) {
	const pageSize = 10
	pages := max(1, (len(blocks)+pageSize-1)/pageSize)
	session.moderationBlocksPage = max(0, min(session.moderationBlocksPage, pages-1))
	session.moderationBlockIDs = nil
	text := header + "\n\nВременные блокировки в выбранной очереди. Нажмите на участника, чтобы разрешить повторную запись."
	if len(blocks) == 0 {
		text += "\n\nДействующих блокировок нет."
	}
	start := session.moderationBlocksPage * pageSize
	end := min(len(blocks), start+pageSize)
	var keyboard [][]models.InlineKeyboardButton
	moscow := time.FixedZone("МСК", 3*60*60)
	for _, block := range blocks[start:end] {
		name := strings.Join(strings.Fields(block.DisplayName), " ")
		if block.Username != "" {
			name = "@" + block.Username
		}
		if name == "" {
			name = fmt.Sprintf("Участник %d", block.UserID)
		}
		label := queueLabel(name, 30) + " — до " + block.BlockedUntil.In(moscow).Format("02.01 15:04:05") + " МСК"
		keyboard = append(keyboard, []models.InlineKeyboardButton{{Text: label, CallbackData: fmt.Sprintf("ModerationUnblock:%d", block.ID)}})
		session.moderationBlockIDs = append(session.moderationBlockIDs, block.ID)
	}
	if pages > 1 {
		text += fmt.Sprintf("\n\nСтраница %d из %d", session.moderationBlocksPage+1, pages)
		var navigation []models.InlineKeyboardButton
		if session.moderationBlocksPage > 0 {
			navigation = append(navigation, models.InlineKeyboardButton{Text: "← Назад", CallbackData: fmt.Sprintf("ModerationBlockPage:%d", session.moderationBlocksPage-1)})
		}
		if session.moderationBlocksPage+1 < pages {
			navigation = append(navigation, models.InlineKeyboardButton{Text: "Далее →", CallbackData: fmt.Sprintf("ModerationBlockPage:%d", session.moderationBlocksPage+1)})
		}
		keyboard = append(keyboard, navigation)
	}
	keyboard = append(keyboard, []models.InlineKeyboardButton{{Text: "К участникам", CallbackData: "ModerationParticipants"}})
	keyboard = append(keyboard, moderationNavigation()...)
	return text, &models.InlineKeyboardMarkup{InlineKeyboard: keyboard}
}

func (h *BotHandler) ModerationBlocksPage(ctx context.Context, b *bot.Bot, update *models.Update) {
	session, ok := h.moderationSession(ctx, update)
	if !ok || !session.moderationSelected || !session.moderationBlocks {
		return
	}
	page, err := strconv.Atoi(strings.TrimPrefix(update.CallbackQuery.Data, "ModerationBlockPage:"))
	if err != nil || page < 0 {
		h.answer(ctx, b, update.CallbackQuery, "Некорректная страница")
		return
	}
	session.moderationBlocksPage = page
	h.showModerationBlocks(ctx, b, update, session)
}

func (h *BotHandler) ModerationParticipants(ctx context.Context, b *bot.Bot, update *models.Update) {
	session, ok := h.moderationSession(ctx, update)
	if !ok || !session.moderationSelected {
		return
	}
	query := update.CallbackQuery
	snapshot, err := h.queues.InspectRun(ctx, session.moderationRun)
	if err != nil {
		h.answer(ctx, b, query, queue.ErrorText(err))
		return
	}
	session.moderationBlocks = false
	session.moderationBlockIDs = nil
	session.enteredAt = time.Now()
	h.stateMu.Lock()
	h.userState[query.From.ID] = session
	h.stateMu.Unlock()
	text, markup := h.moderationParticipants(ctx, snapshot, session)
	_ = h.editMenu(ctx, b, query.From.ID, text, markup)
	h.answer(ctx, b, query, "")
}

func (h *BotHandler) UnblockParticipant(ctx context.Context, b *bot.Bot, update *models.Update) {
	session, ok := h.moderationSession(ctx, update)
	if !ok || !session.moderationSelected || !session.moderationBlocks {
		return
	}
	query := update.CallbackQuery
	blockID, err := strconv.Atoi(strings.TrimPrefix(query.Data, "ModerationUnblock:"))
	visible := false
	for _, id := range session.moderationBlockIDs {
		visible = visible || id == blockID
	}
	if err != nil || !visible {
		h.answer(ctx, b, query, "Кнопка устарела. Выберите блокировку заново")
		return
	}
	if err := h.queues.Unblock(ctx, session.moderationRun, query.From.ID, blockID); err != nil {
		h.answer(ctx, b, query, queue.ErrorText(err))
		return
	}
	h.showModerationBlocks(ctx, b, update, session)
}
