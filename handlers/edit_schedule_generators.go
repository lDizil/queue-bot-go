package handlers

import (
	"context"
	"fmt"
	"queuebot/db"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (h *BotHandler) GenerateEditMessage(schedules []db.Schedule) (string, *models.InlineKeyboardMarkup) {
	text := "Вы вошли в режим редактирования.\nМожете изменить существующие записи или добавить новые.\n"

	var builder strings.Builder

	builder.Grow(120 + len(schedules)*30)
	builder.WriteString(text)

	keyboard := [][]models.InlineKeyboardButton{}

	if len(schedules) == 0 {
		builder.WriteString("\nНет записей для редактирования")
	} else {
		for _, sch := range schedules {
			var btn models.InlineKeyboardButton
			if sch.ThreadDescription == nil {
				btn = models.InlineKeyboardButton{
					Text:         fmt.Sprintf("%s | %s | %s - %s | %d", dayEnToRu[strings.ToLower(sch.DayOfWeek)], weekTypeEnToRu[strings.ToLower(sch.WeekType)], sch.StartTime.Format("15:04:05"), sch.EndTime.Format("15:04:05"), sch.ThreadID),
					CallbackData: fmt.Sprintf("EditSchedule:%d", sch.ID),
				}
			} else {
				btn = models.InlineKeyboardButton{
					Text:         fmt.Sprintf("%s | %s | %s - %s | %d | %s", dayEnToRu[strings.ToLower(sch.DayOfWeek)], weekTypeEnToRu[strings.ToLower(sch.WeekType)], sch.StartTime.Format("15:04:05"), sch.EndTime.Format("15:04:05"), sch.ThreadID, *sch.ThreadDescription),
					CallbackData: fmt.Sprintf("EditSchedule:%d", sch.ID),
				}
			}

			keyboard = append(keyboard, []models.InlineKeyboardButton{btn})
		}
	}

	btn := models.InlineKeyboardButton{
		Text:         "Добавить новую запись",
		CallbackData: "AddNewSchedule",
	}

	keyboard = append(keyboard, []models.InlineKeyboardButton{btn})

	btn = models.InlineKeyboardButton{
		Text:         "Закончить редактирование",
		CallbackData: "LeaveEditSchedule",
	}

	keyboard = append(keyboard, []models.InlineKeyboardButton{btn})

	return builder.String(), &models.InlineKeyboardMarkup{InlineKeyboard: keyboard}
}

func (h *BotHandler) GenerateEditScheduleMenu(sch db.Schedule) (string, *models.InlineKeyboardMarkup) {
	desc := "(у этой записи его нет)"
	if sch.ThreadDescription != nil {
		desc = *sch.ThreadDescription
	}

	text := fmt.Sprintf(`Вы перешли к редактированию записи в расписании.

День недели: %s
Время (нач-кон): %s-%s
Неделя: %s 
Айди темы чата: %d

Описание: %s`,
		dayEnToRu[sch.DayOfWeek], sch.StartTime.Format("15:04:05"), sch.EndTime.Format("15:04:05"), weekTypeEnToRu[sch.WeekType], sch.ThreadID, desc)

	keyboard := [][]models.InlineKeyboardButton{}
	var btn models.InlineKeyboardButton

	btn = models.InlineKeyboardButton{
		Text:         "Изменить день недели",
		CallbackData: fmt.Sprintf("EditDayOfWeek:%d:%s", sch.ID, sch.DayOfWeek),
	}
	keyboard = append(keyboard, []models.InlineKeyboardButton{btn})

	btn = models.InlineKeyboardButton{
		Text:         "Изменить время",
		CallbackData: fmt.Sprintf("EditTime:%d", sch.ID),
	}
	keyboard = append(keyboard, []models.InlineKeyboardButton{btn})

	btn = models.InlineKeyboardButton{
		Text:         "Изменить тип недели (сразу меняет по нажатию)",
		CallbackData: fmt.Sprintf("EditTypeOfWeek:%d:%s", sch.ID, sch.WeekType),
	}
	keyboard = append(keyboard, []models.InlineKeyboardButton{btn})

	btn = models.InlineKeyboardButton{
		Text:         "Изменить айди темы чата",
		CallbackData: fmt.Sprintf("EditThreadID:%d", sch.ID),
	}
	keyboard = append(keyboard, []models.InlineKeyboardButton{btn})

	btn = models.InlineKeyboardButton{
		Text:         "❌ Удалить запись ❌",
		CallbackData: fmt.Sprintf("DeleteEntry:%d", sch.ID),
	}

	keyboard = append(keyboard, []models.InlineKeyboardButton{btn}, []models.InlineKeyboardButton{BackBtnMainMenu})

	return text, &models.InlineKeyboardMarkup{InlineKeyboard: keyboard}
}

func (h *BotHandler) GenerateChangeWeekDayMenu(ctx context.Context, b *bot.Bot, update *models.Update) {
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

	curDayWeek := dataSl[2]

	keyboard := [][]models.InlineKeyboardButton{}

	days := []struct{ key, name string }{
		{"monday", "Понедельник"},
		{"tuesday", "Вторник"},
		{"wednesday", "Среда"},
		{"thursday", "Четверг"},
		{"friday", "Пятница"},
		{"saturday", "Суббота"},
		{"sunday", "Воскресенье"},
	}

	for _, day := range days {
		if curDayWeek == day.key {
			continue
		}

		keyboard = append(keyboard, []models.InlineKeyboardButton{{
			Text:         day.name,
			CallbackData: fmt.Sprintf("ChWeekDay:%d:%s", scheduleID, day.key),
		}})
	}

	BackBtnEditSch := GetBackBtnEditSch(scheduleID)

	keyboard = append(keyboard, []models.InlineKeyboardButton{BackBtnEditSch})

	text := "Выберите день недели на который хотите сменить текущий"

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

func (h *BotHandler) GenerateEditTimeMenu(scheduleID int) *models.InlineKeyboardMarkup {
	keyboard := [][]models.InlineKeyboardButton{}

	btn := models.InlineKeyboardButton{
		Text:         "Время начала",
		CallbackData: fmt.Sprintf("HandleTimeStart:%d", scheduleID),
	}
	keyboard = append(keyboard, []models.InlineKeyboardButton{btn})

	btn = models.InlineKeyboardButton{
		Text:         "Время конца",
		CallbackData: fmt.Sprintf("HandleTimeEnd:%d", scheduleID),
	}
	keyboard = append(keyboard, []models.InlineKeyboardButton{btn})

	backBtnEditSch := GetBackBtnEditSch(scheduleID)

	keyboard = append(keyboard, []models.InlineKeyboardButton{backBtnEditSch})

	return &models.InlineKeyboardMarkup{InlineKeyboard: keyboard}
}
