package handlers

import (
	"context"
	"fmt"
	u "queuebot/utils"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (h *BotHandler) ChangeWeekType(ctx context.Context, b *bot.Bot, update *models.Update) {
	query, data, chatID, userID, username := h.getAllReplyInfo(update)

	h.editMu.RLock()
	editMesID, _ := h.editMessages[userID]
	h.editMu.RUnlock()
	
	dataSl := strings.Split(data, ":")

	scheduleID, err := strconv.Atoi(dataSl[1])
	if err != nil {
		msg := fmt.Sprintf("Ошибка при преобразовании айди записи в int (пользователь %s).\nОшибка: %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	curWeekType := dataSl[2]

	err = h.db.ChangeWeekType(ctx, scheduleID, u.SwitchWeekType[curWeekType])
	if err != nil {
		msg := fmt.Sprintf("Ошибка изменения типа недели (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	err = h.reschedule(ctx, scheduleID) 
	if err != nil {
		msg := fmt.Sprintf("Ошибка во время перепланирования записи (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}
	
	err = h.ReturnToEditSchMenu(ctx, b, scheduleID, username, query, chatID, editMesID)
	if err != nil {
		return
	}
}

func (h *BotHandler) ChangeWeekDay(ctx context.Context, b *bot.Bot, update *models.Update) {
	query, data, chatID, userID, username := h.getAllReplyInfo(update)

	h.editMu.RLock()
	editMesID, _ := h.editMessages[userID]
	h.editMu.RUnlock()

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
	
	err = h.reschedule(ctx, scheduleID) 
	if err != nil {
		msg := fmt.Sprintf("Ошибка во время перепланирования записи (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	err = h.ReturnToEditSchMenu(ctx, b, scheduleID, username, query, chatID, editMesID)
	if err != nil {
		return
	}
}

func (h *BotHandler) EditTimeMenu(ctx context.Context, b *bot.Bot, update *models.Update) {
	query, data, chatID, userID, username := h.getAllReplyInfo(update)

	h.editMu.RLock()
	editMesID, _ := h.editMessages[userID]
	h.editMu.RUnlock()
	
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
	query, data, chatID, userID, username := h.getAllReplyInfo(update)

	h.editMu.RLock()
	editMesID, _ := h.editMessages[userID]
	h.editMu.RUnlock()

	dataSl := strings.Split(data, ":")

	curState := dataSl[0]
	scheduleID, err := strconv.Atoi(dataSl[1])
	if err != nil {
		msg := fmt.Sprintf("Ошибка при преобразовании айди записи в int (пользователь %s).\nОшибка: %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	var curUserState string

	if strings.HasPrefix(curState, "HandleTimeStart") {
		curUserState = "edit_start_time"
	} else if strings.HasPrefix(curState, "HandleTimeEnd") {
		curUserState = "edit_end_time"
	}

	h.stateMu.Lock()
	session := h.userState[userID]
	session.state = curUserState
	session.scheduleID = scheduleID
	h.userState[userID] = session
	h.stateMu.Unlock()

	var text string

	switch curUserState {
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
	query, data, chatID, userID, username := h.getAllReplyInfo(update)

	h.editMu.RLock()
	editMesID, _ := h.editMessages[userID]
	h.editMu.RUnlock()

	dataSl := strings.Split(data, ":")

	scheduleID, err := strconv.Atoi(dataSl[1])
	if err != nil {
		msg := fmt.Sprintf("Ошибка при преобразовании айди записи в int (пользователь %s).\nОшибка: %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	text := "Напиши в чат новое айди темы чата куда хотите, чтобы бот отправлял очередь.\nФормат: 12327 (просто число).\nНайти его можно в ссылке на вашу тему (последние цифры)"

	keyboard := [][]models.InlineKeyboardButton{}
	BackBtnEditTime := GetBackBtnEditSch(scheduleID)
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

	var curState string

	if dataSl[0] == "EditThreadID" {
		curState = "edit_thread_id"
	}

	h.stateMu.Lock()
	session := h.userState[userID]
	session.state = curState
	session.scheduleID = scheduleID
	h.userState[userID] = session
	h.stateMu.Unlock()
}

func (h *BotHandler) EditDescription(ctx context.Context, b *bot.Bot, update *models.Update) {
	query, data, chatID, userID, username := h.getAllReplyInfo(update)

	h.editMu.RLock()
	editMesID, _ := h.editMessages[userID]
	h.editMu.RUnlock()

	dataSl := strings.Split(data, ":")

	scheduleID, err := strconv.Atoi(dataSl[1])
	if err != nil {
		msg := fmt.Sprintf("Ошибка при преобразовании айди записи в int (пользователь %s).\nОшибка: %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	text := "Напишите в чат новое описание для текущей записи.\nНапример: Занятие по devops"

	keyboard := [][]models.InlineKeyboardButton{}
	BackBtnEditTime := GetBackBtnEditSch(scheduleID)
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

	var curState string

	if dataSl[0] == "EditDescription" {
		curState = "edit_description"
	}

	h.stateMu.Lock()
	session := h.userState[userID]
	session.state = curState
	session.scheduleID = scheduleID
	h.userState[userID] = session
	h.stateMu.Unlock()
}
