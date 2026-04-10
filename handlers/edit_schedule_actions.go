package handlers

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (h *BotHandler) ChangeWeekType(ctx context.Context, b *bot.Bot, update *models.Update) {
	query, data, chatID, userID, username, replyEditMesID := h.getAllReplyInfo(update)

	h.editMu.RLock()
	editMesID, exists := h.editMessages[userID]
	h.editMu.RUnlock()

	isUserCanChange := h.validateEditSession(ctx, b, editMesID, replyEditMesID, query, exists)

	if !isUserCanChange {
		return
	}

	dataSl := strings.Split(data, ":")

	scheduleID, err := strconv.Atoi(dataSl[1])
	if err != nil {
		msg := fmt.Sprintf("Ошибка при преобразовании айди записи в int (пользователь %s).\nОшибка: %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	curWeekType := dataSl[2]

	err = h.db.ChangeWeekType(ctx, scheduleID, switchWeekType[curWeekType])
	if err != nil {
		msg := fmt.Sprintf("Ошибка изменения типа недели (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	err = h.ReturnToEditSchMenu(ctx, b, scheduleID, username, query, chatID, editMesID)
	if err != nil {
		return
	}
}

func (h *BotHandler) ChangeWeekDay(ctx context.Context, b *bot.Bot, update *models.Update) {
	query, data, chatID, userID, username, replyEditMesID := h.getAllReplyInfo(update)

	h.editMu.RLock()
	editMesID, exists := h.editMessages[userID]
	h.editMu.RUnlock()

	isUserCanChange := h.validateEditSession(ctx, b, editMesID, replyEditMesID, query, exists)

	if !isUserCanChange {
		return
	}

	params := strings.Split(data, ":")

	scheduleID, err := strconv.Atoi(params[1])
	if err != nil {
		msg := fmt.Sprintf("Ошибка при преобразовании айди записи в int (пользователь %s).\nОшибка: %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	weekDay := params[2]

	err = h.db.ChangeWeekDay(ctx, scheduleID, weekDay)
	if err != nil {
		msg := fmt.Sprintf("Ошибка при изменении дня недели у записи (пользователь %s).\nОшибка: %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	err = h.ReturnToEditSchMenu(ctx, b, scheduleID, username, query, chatID, editMesID)
	if err != nil {
		return
	}
}

func (h *BotHandler) EditTimeMenu(ctx context.Context, b *bot.Bot, update *models.Update) {
	query, data, chatID, userID, username, replyEditMesID := h.getAllReplyInfo(update)

	h.editMu.RLock()
	editMesID, exists := h.editMessages[userID]
	h.editMu.RUnlock()

	isUserCanChange := h.validateEditSession(ctx, b, editMesID, replyEditMesID, query, exists)

	if !isUserCanChange {
		return
	}

	dataSl := strings.Split(data, ":")

	scheduleID, err := strconv.Atoi(dataSl[1])
	if err != nil {
		msg := fmt.Sprintf("Ошибка при преобразовании айди записи в int (пользователь %s).\nОшибка: %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	text := "Выберите какое время хотите изменить"

	markup := h.GenerateEditTimeMenu(scheduleID)

	_, err = b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:      chatID,
		MessageID:   editMesID,
		Text:        text,
		ReplyMarkup: markup,
	})

	if err != nil {
		msg := fmt.Sprintf("Ошибка при изменении сообщения редактирования очереди (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}
}

func (h *BotHandler) EditTime(ctx context.Context, b *bot.Bot, update *models.Update) {
	query, data, chatID, userID, username, replyEditMesID := h.getAllReplyInfo(update)

	h.editMu.RLock()
	editMesID, exists := h.editMessages[userID]
	h.editMu.RUnlock()

	isUserCanChange := h.validateEditSession(ctx, b, editMesID, replyEditMesID, query, exists)

	if !isUserCanChange {
		return
	}

	dataSl := strings.Split(data, ":")

	curState := dataSl[0]
	scheduleID, err := strconv.Atoi(dataSl[1])
	if err != nil {
		msg := fmt.Sprintf("Ошибка при преобразовании айди записи в int (пользователь %s).\nОшибка: %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	var curUserState userState
	
	if strings.HasPrefix(curState, "HandleTimeStart") {
		curUserState = userState{state: "edit_start_time", scheduleID: scheduleID}
	} else if strings.HasPrefix(curState, "HandleTimeEnd") {
		curUserState = userState{state: "edit_end_time", scheduleID: scheduleID}
	}
	
	h.stateMu.Lock()
	h.userState[userID] = curUserState
	h.stateMu.Unlock()

	var text string

	switch curUserState.state {
	case "edit_start_time":
		text = "Введите в чат новое время начала для этой записи.\nФормат чч-мм-сс\nНапример: 14:30:00 или просто 14:30"
	case "edit_end_time":
		text = "Введите в чат новое время конца для этой записи.\nФормат чч-мм-сс\nНапример: 14:30:00 или просто 14:30"
	}
	
	keyboard := [][]models.InlineKeyboardButton{}

	BackBtnEditTime := GetBackBtnEditTime(scheduleID)

	keyboard = append(keyboard, []models.InlineKeyboardButton{BackBtnEditTime})

	_, err = b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:      chatID,
		MessageID:   editMesID,
		Text:        text,
		ReplyMarkup: models.InlineKeyboardMarkup{InlineKeyboard: keyboard},
	})

	if err != nil {
		msg := fmt.Sprintf("Ошибка при изменении сообщения редактирования очереди (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}
}

func (h *BotHandler) EditThreadID(ctx context.Context, b *bot.Bot, update *models.Update) {
	query, data, chatID, userID, username, replyEditMesID := h.getAllReplyInfo(update)

	h.editMu.RLock()
	editMesID, exists := h.editMessages[userID]
	h.editMu.RUnlock()

	isUserCanChange := h.validateEditSession(ctx, b, editMesID, replyEditMesID, query, exists)

	if !isUserCanChange {
		return
	}

	dataSl := strings.Split(data, ":")

	scheduleID, err := strconv.Atoi(dataSl[1])
	if err != nil {
		msg := fmt.Sprintf("Ошибка при преобразовании айди записи в int (пользователь %s).\nОшибка: %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	text := "Напиши в чат новое айди темы чата куда хотите, чтобы бот отправлял очередь.\nФормат: 12327 (просто число).\nНайти его можно в ссылке на вашу тему (последние цифры)"

	_, err = b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:      chatID,
		MessageID:   editMesID,
		Text:        text,
	})

	if err != nil {
		msg := fmt.Sprintf("Ошибка при изменении сообщения редактирования очереди (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	var curState userState

	if dataSl[0] == "EditThreadID" {
		curState = userState{state: "edit_thread_id", scheduleID: scheduleID}
	}

	h.stateMu.Lock()
	h.userState[userID] = curState
	h.stateMu.Unlock()
}