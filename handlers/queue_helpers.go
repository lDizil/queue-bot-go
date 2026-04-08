package handlers

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (h *BotHandler) parseScheduleID(ctx context.Context, b *bot.Bot, query *models.CallbackQuery, data string, username string) (int, error) {
	scheduleID, err := strconv.Atoi(strings.Split(data, ":")[1])
	if err != nil {
		msg := fmt.Sprintf("Ошибка во время преобразования scheduleID в int (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return -1, err
	}

	return scheduleID, err
}

func (h *BotHandler) parseSlot(ctx context.Context, b *bot.Bot, query *models.CallbackQuery, data string, username string) (int, error) {
	slot, err := strconv.Atoi(strings.Split(data, ":")[2])
	if err != nil {
		msg := fmt.Sprintf("Ошибка во время преобразования slot в int (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return -1, err
	}

	return slot, nil
}

func (h *BotHandler) checkIsUserInQueue(ctx context.Context, b *bot.Bot, userID int64, scheduleID int, query *models.CallbackQuery, username string) (bool, error) {
	isUserInQueue, err := h.db.IsUserInQueue(ctx, userID, scheduleID)
	if err != nil {
		msg := fmt.Sprintf("Ошибка проверки стоит ли пользователь в очереди (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return false, err
	}

	if isUserInQueue {
		b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			Text:            "Вы уже заняли место в очереди",
			CallbackQueryID: query.ID,
		})

		return true, nil
	}

	return false, nil
}
