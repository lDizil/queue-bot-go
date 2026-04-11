package handlers

import (
	"maps"
	"context"
	"fmt"
	"log"
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
	if len(data) == 0 || len(data) > 6 || len(data) < 5 {
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
	h.stateMu.RLock()
    snapshot := make(map[int64]userState, len(h.userState))
    maps.Copy(snapshot, h.userState)
    h.stateMu.RUnlock()
	
	for userID, s := range snapshot {
		if time.Since(s.enteredAt) > timeForExpiredEditSes {
			h.editMu.RLock()
			editMesID, _ := h.editMessages[userID]
			h.editMu.RUnlock()

			_, err := h.b.EditMessageText(context.Background(), &bot.EditMessageTextParams{
				ChatID:    s.chatID,
				MessageID: editMesID,
				Text:      "Сессия редактирования завершена по таймауту",
			})

			if err != nil {
				log.Printf("Ошибка завершения сессии редактирования пользователя с id: %d, err: %v", userID, err)
			} else {
				log.Printf("Сессия редактирования пользователя с id: %d завершена", userID)
			}

			h.editMu.Lock()
			delete(h.editMessages, userID)
			h.editMu.Unlock()

			h.stateMu.Lock()
			delete(h.userState, userID)
			h.stateMu.Unlock()
		}
	}
}
