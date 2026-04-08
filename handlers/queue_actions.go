package handlers

import (
	"context"
	"fmt"
	"queuebot/db"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5"
)

func (h *BotHandler) JoinToPosition(ctx context.Context, b *bot.Bot, update *models.Update) {
	query := update.CallbackQuery
	data := query.Data

	userID := query.From.ID
	username := query.From.Username

	scheduleID, err := h.parseScheduleID(ctx, b, query, data, username)
	if err != nil {
		return
	}

	slot, err := h.parseSlot(ctx, b, query, data, username)
	if err != nil {
		return
	}

	isInQueue, err := h.checkIsUserInQueue(ctx, b, userID, scheduleID, query, username)

	if err != nil {
		return
	}

	if isInQueue {
		return
	}

	entry := db.QueueEntry{
		UserID:     userID,
		Username:   username,
		Position:   slot,
		ScheduleID: scheduleID,
	}

	err = h.db.JoinQueue(ctx, entry)
	if err != nil {
		msg := fmt.Sprintf("Ошибка при попытке встать в очередь (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	msg := query.Message.Message
	chatID := msg.Chat.ID

	task := &updateTask{
		ctx:        ctx,
		b:          b,
		chatID:     chatID,
		scheduleID: scheduleID,
	}

	h.updateQueue <- *task

	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		Text:            fmt.Sprintf("Вы успешно заняли %d в очереди\nДа прибудет с вами сила", slot),
		CallbackQueryID: query.ID,
	})

}

func (h *BotHandler) JoinClosestFreeSlot(ctx context.Context, b *bot.Bot, update *models.Update) {
	query := update.CallbackQuery
	data := query.Data

	userID := query.From.ID
	username := query.From.Username

	scheduleID, err := h.parseScheduleID(ctx, b, query, data, username)
	if err != nil {
		return
	}

	isInQueue, err := h.checkIsUserInQueue(ctx, b, userID, scheduleID, query, username)

	if err != nil {
		return
	}

	if isInQueue {
		return
	}

	slot, err := h.db.JoinFirstFreeSlot(ctx, userID, username, scheduleID, h.totalSlotsInQueue)
	if err != nil {
		if err == pgx.ErrNoRows {
			b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
				Text:            "Все места в очереди заняты",
				CallbackQueryID: query.ID,
			})

			return
		} else {
			msg := fmt.Sprintf("Ошибка при попытке занять ближайшее место в очереди (пользователь %s). %v", username, err)
			h.handleError(ctx, b, query.ID, msg)
			return
		}
	}

	msg := query.Message.Message
	chatID := msg.Chat.ID

	task := &updateTask{
		ctx:        ctx,
		b:          b,
		chatID:     chatID,
		scheduleID: scheduleID,
	}

	h.updateQueue <- *task

	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		Text:            fmt.Sprintf("Вы успешно заняли %d в очереди\nДа прибудет с вами сила", slot),
		CallbackQueryID: query.ID,
	})
}

func (h *BotHandler) JoinBusySlot(ctx context.Context, b *bot.Bot, update *models.Update) {
	query := update.CallbackQuery
	data := query.Data

	userID := query.From.ID
	username := query.From.Username

	scheduleID, err := h.parseScheduleID(ctx, b, query, data, username)
	if err != nil {
		return
	}

	slot, err := h.parseSlot(ctx, b, query, data, username)
	if err != nil {
		return
	}

	isUserInQueue, err := h.db.IsUserInQueue(ctx, userID, scheduleID)
	if err != nil {
		msg := fmt.Sprintf("Ошибка проверки стоит ли пользователь в очереди (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	if isUserInQueue {
		b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			Text:            "Вы уже заняли место в очереди",
			CallbackQueryID: query.ID,
		})

		return
	}

	taken, err := h.db.IsPositionTaken(ctx, slot, scheduleID)
	if err != nil {
		msg := fmt.Sprintf("Ошибка проверки занята ли позиция (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	if !taken {
		b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			Text:            "Позиция не занята, но считается закрытой (крестик на кнопке), повторите попытку",
			CallbackQueryID: query.ID,
		})
		return
	}

	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		Text:            "Данное место уже занято, выберите другое",
		CallbackQueryID: query.ID,
	})

}

func (h *BotHandler) LeaveQueue(ctx context.Context, b *bot.Bot, update *models.Update) {
	query := update.CallbackQuery
	data := query.Data

	userID := query.From.ID
	username := query.From.Username

	scheduleID, err := h.parseScheduleID(ctx, b, query, data, username)
	if err != nil {
		return
	}

	isUserInQueue, err := h.db.IsUserInQueue(ctx, userID, scheduleID)
	if err != nil {
		msg := fmt.Sprintf("Ошибка проверки стоит ли пользователь в очереди (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	if !isUserInQueue {
		b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			Text:            "Вы не находитесь в очереди",
			CallbackQueryID: query.ID,
		})

		return
	}

	err = h.db.LeaveFromQueue(ctx, userID, scheduleID)

	if err != nil {
		msg := fmt.Sprintf("Ошибка выхода из очереди (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	msg := query.Message.Message
	chatID := msg.Chat.ID

	task := &updateTask{
		ctx:        ctx,
		b:          b,
		chatID:     chatID,
		scheduleID: scheduleID,
	}

	h.updateQueue <- *task

	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		Text:            "Вы успешно покинули очередь. Удачного дня",
		CallbackQueryID: query.ID,
	})
}
