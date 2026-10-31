package handlers

import (
	"context"
	"fmt"
	"log"
	"queuebot/db"
	"strconv"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (h *BotHandler) GetEditMessage(userID int64) (int, bool) {
	h.editMu.RLock()
	defer h.editMu.RUnlock()
	id, ok := h.editMessages[userID]
	return id, ok
}

func (h *BotHandler) EditMesWithError(ctx context.Context, b *bot.Bot, chatID int64, editMesID int, text string, backBtn models.InlineKeyboardButton) {
	b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    chatID,
		Text:      text,
		MessageID: editMesID,
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{{backBtn}},
		},
	})
}

func (h *BotHandler) isValidData(data []string) bool {
	if len(data) > 7 || len(data) < 5 {
		return false
	}

	_, okRu := dayRuToEn[data[0]]
	_, okEn := dayEnToRu[data[0]]

	if !okRu && !okEn {
		return false
	}

	_, okRu = weekTypeRuToEn[data[1]]
	_, okEn = weekTypeEnToRu[data[1]]

	if !okRu && !okEn {
		return false
	}

	_, err := time.Parse("15:04:05", data[2])
	if err != nil {
		_, err = time.Parse("15:04", data[2])
		if err != nil {
			return false
		}
	}

	_, err = time.Parse("15:04:05", data[3])
	if err != nil {
		_, err = time.Parse("15:04", data[3])
		if err != nil {
			return false
		}
	}

	_, err = strconv.Atoi(data[4])
	if err != nil {
		return false
	}
	threadID, _ := strconv.Atoi(data[4])
	if threadID < 0 {
		return false
	}
	start, _ := parseScheduleTime(data[2])
	end, _ := parseScheduleTime(data[3])
	if start.Equal(end) {
		return false
	}
	if len(data) >= 6 && len([]rune(data[5])) > 500 {
		return false
	}
	if len(data) == 7 && len([]rune(data[6])) > 200 {
		return false
	}

	return true
}

func (h *BotHandler) ReturnToEditSchMenu(ctx context.Context, b *bot.Bot, scheduleID int, username string, query *models.CallbackQuery, chatID int64, editMesID int) error {
	schedule, err := h.db.GetScheduleEntry(ctx, scheduleID)

	if err != nil {
		msg := fmt.Sprintf("Ошибка при получении записи (пользователь %s).\nОшибка: %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return err
	}

	text, markup := h.GenerateEditScheduleMenu(schedule)

	_, err = b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:      chatID,
		MessageID:   editMesID,
		Text:        text,
		ReplyMarkup: markup,
	})

	if err != nil {
		msg := fmt.Sprintf("Ошибка при изменении сообщения редактирования очереди (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return err
	}

	return nil
}

func (h *BotHandler) isExpiredUserEditSes(timeForExpiredEditSes time.Duration) {
	h.editActionMu.Lock()
	defer h.editActionMu.Unlock()
	h.configMu.Lock()
	defer h.configMu.Unlock()
	type expiredMenu struct {
		chatID    int64
		messageID int
	}
	var expired []expiredMenu
	h.stateMu.Lock()
	h.editMu.Lock()
	for userID, session := range h.userState {
		if time.Since(session.enteredAt) <= timeForExpiredEditSes {
			continue
		}
		expired = append(expired, expiredMenu{session.chatID, h.editMessages[userID]})
		delete(h.userState, userID)
		delete(h.editMessages, userID)
		delete(h.editMessageChats, userID)
	}
	h.editMu.Unlock()
	h.stateMu.Unlock()
	if h.b == nil {
		return
	}
	for _, menu := range expired {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := h.b.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID: menu.chatID, MessageID: menu.messageID, Text: "Сессия редактирования завершена по таймауту"})
		cancel()
		if err != nil {
			log.Printf("expire editing menu: %v", err)
		}
	}
}

func parseScheduleTime(value string) (time.Time, error) {
	parsed, err := time.Parse("15:04:05", value)
	if err != nil {
		return time.Parse("15:04", value)
	}
	return parsed, nil
}

func (h *BotHandler) scheduleChatID(chatID int64) int64 {
	if chatID > 0 {
		return h.defaultChatID
	}
	return chatID
}

func (h *BotHandler) schedulesInChat(ctx context.Context, chatID int64) ([]db.Schedule, error) {
	schedules, err := h.db.GetAllSchedules(ctx)
	if err != nil {
		return nil, err
	}
	if chatID > 0 {
		return schedules, nil
	}
	var filtered []db.Schedule
	for _, schedule := range schedules {
		if schedule.ChatID == chatID {
			filtered = append(filtered, schedule)
		}
	}
	return filtered, nil
}

func (h *BotHandler) ValidateEditSession(userID, chatID int64, threadID, messageID int) bool {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	session, ok := h.userState[userID]
	if !ok || session.chatID != chatID || session.threadID != threadID || time.Since(session.enteredAt) > h.settings.Values().TimeForExpiredEditSes {
		return false
	}
	h.editMu.RLock()
	matches := h.editMessages[userID] == messageID && h.editMessageChats[userID] == chatID
	h.editMu.RUnlock()
	if matches {
		session.enteredAt = time.Now()
		h.userState[userID] = session
	}
	return matches
}
