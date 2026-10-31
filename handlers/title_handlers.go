package handlers

import (
	"context"
	"log"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (h *BotHandler) EditTitle(ctx context.Context, b *bot.Bot, update *models.Update) {
	query, data, chatID, userID, _ := h.getAllReplyInfo(update)
	parts := strings.Split(data, ":")
	if len(parts) != 2 {
		h.answer(ctx, b, query, "Некорректная кнопка")
		return
	}
	scheduleID, err := strconv.Atoi(parts[1])
	if err != nil {
		h.answer(ctx, b, query, "Некорректная кнопка")
		return
	}
	h.stateMu.Lock()
	session := h.userState[userID]
	session.state = "edit_title"
	session.scheduleID = scheduleID
	h.userState[userID] = session
	h.stateMu.Unlock()
	h.editMu.RLock()
	messageID := h.editMessages[userID]
	h.editMu.RUnlock()
	h.EditMesWithError(ctx, b, chatID, messageID, "Введите заголовок очереди (до 200 символов). Для удаления отправьте один дефис: -", GetBackBtnEditSch(scheduleID))
	h.answer(ctx, b, query, "")
}

func (h *BotHandler) HandleNewTitle(ctx context.Context, b *bot.Bot, update *models.Update) {
	msg := update.Message
	h.stateMu.RLock()
	session := h.userState[msg.From.ID]
	h.stateMu.RUnlock()
	h.editMu.RLock()
	messageID := h.editMessages[msg.From.ID]
	h.editMu.RUnlock()
	title := strings.TrimSpace(msg.Text)
	if title == "-" {
		title = ""
	}
	if len([]rune(title)) > 200 {
		h.EditMesWithError(ctx, b, msg.Chat.ID, messageID, "Заголовок должен быть не длиннее 200 символов", GetBackBtnEditSch(session.scheduleID))
		return
	}
	if err := h.db.SetTitle(ctx, session.scheduleID, title); err != nil {
		h.EditMesWithError(ctx, b, msg.Chat.ID, messageID, "Не удалось сохранить заголовок", GetBackBtnEditSch(session.scheduleID))
		return
	}
	schedule, err := h.db.GetScheduleEntry(ctx, session.scheduleID)
	if err != nil {
		return
	}
	h.queues.Changed(schedule.ID)
	h.stateMu.Lock()
	session.state = ""
	h.userState[msg.From.ID] = session
	h.stateMu.Unlock()
	text, markup := h.GenerateEditScheduleMenu(schedule)
	if _, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID: msg.Chat.ID, MessageID: messageID, Text: text, ReplyMarkup: markup}); err != nil {
		log.Printf("update title menu: %v", err)
	}
}
