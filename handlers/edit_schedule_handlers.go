package handlers

import (
	"context"
	"fmt"
	"log"
	"queuebot/db"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (h *BotHandler) validateEditSession(ctx context.Context, b *bot.Bot, editMesID int, replyEditMesID int, query *models.CallbackQuery, exists bool) bool {
	if !exists {
		b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			Text:            "Вы не находитесь в режиме редактирования",
			CallbackQueryID: query.ID,
		})

		return false
	}

	if editMesID != replyEditMesID {
		b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			Text:            "Это сообщение редактирования очереди для другого пользователя. Если хотите также изменить расписание, вызовите /edit_schedule",
			CallbackQueryID: query.ID,
		})

		return false
	}

	return true
}

func (h *BotHandler) EditMesWithError(ctx context.Context, b *bot.Bot, chatID int64, editMesID int, text string) {
	b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID: chatID,
		Text:      text,
		MessageID: editMesID,
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{{BackBtnMainMenu}},
		},
	})
}

func (h *BotHandler) Back(ctx context.Context, b *bot.Bot, update *models.Update) {
	query := update.CallbackQuery
	data := query.Data
	
	chatID := query.Message.Message.Chat.ID
	username := query.From.Username
	userID := query.From.ID

	replyEditMesID := query.Message.Message.ID

	schedules, err := h.db.GetAllSchedules(ctx)
	if err != nil {
		log.Printf("Ошибка получения очереди (пользователь %s). %v", username, err)
		return
	}

	var markup *models.InlineKeyboardMarkup
	
	returnTo := strings.Split(data, ":")[1]

	switch returnTo {
	case "mainmenu":
		_, markup = h.GenerateEditMessage(schedules)
	default:
		msg := fmt.Sprintf("Не найден путь возврата %s в меню изменения очереди (пользователь %s). %v", returnTo, username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	h.editMu.RLock()
	editMesID, exists := h.editMessages[userID]
	h.editMu.RUnlock()

	isUserCanChange := h.validateEditSession(ctx, b, editMesID, replyEditMesID, query, exists)

	if !isUserCanChange {
		return
	}

	_, err = b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: editMesID,
		Text: "Вы вернулись к прошлому меню редактирования.\nМожете продолжить вносить изменения. .\n",
		ReplyMarkup: markup,
	})

	if err != nil {
		msg := fmt.Sprintf("Ошибка при возврате в главное меню (кнопка назад, пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}
}

func (h *BotHandler) EditScheduleReplyText(ctx context.Context, b *bot.Bot, update *models.Update) {
	msg := update.Message
	username := msg.From.Username
	userID := msg.From.ID
	chatID := msg.Chat.ID

	schedules, err := h.db.GetAllSchedules(ctx)
	if err != nil {
		log.Printf("Ошибка получения очереди (пользователь %s). %v", username, err)
		return
	}

	h.editMu.RLock()
	oldMesID, exists := h.editMessages[userID]
	h.editMu.RUnlock()

	if exists {
		b.DeleteMessage(ctx, &bot.DeleteMessageParams{
			ChatID:    chatID,
			MessageID: oldMesID,
		})
	}

	text, markup := h.GenerateEditMessage(schedules)

	msg, err = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ReplyMarkup: markup,
	})

	if err != nil {
		log.Printf("Ошибка при попытке отправить сообщения для редактирования расписания (пользователь %s). %v", username, err)
		return
	}

	h.editMu.Lock()
	h.editMessages[userID] = msg.ID
	h.editMu.Unlock()

	log.Printf("Сообщение для редактирования очереди (id: %d) отправлено в чат", msg.ID)
}

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

func (h *BotHandler) EditScheduleEntry(ctx context.Context, b *bot.Bot, update *models.Update) {
	query := update.CallbackQuery
	data := query.Data
	
	chatID := query.Message.Message.Chat.ID
	username := query.From.Username
	userID := query.From.ID

	scheduleID, err := strconv.Atoi(strings.Split(data, ":")[1])
	
	if err != nil {
		msg := fmt.Sprintf("Ошибка при преобразовании айди записи в int (пользователь %s).\nОшибка: %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return	
	}
	log.Printf("data: %s, scheduleID: %d, err: %v", data, scheduleID, err)

	replyEditMesID := query.Message.Message.ID
	
	h.editMu.RLock()
	editMesID, exists := h.editMessages[userID]
	h.editMu.RUnlock()

	isUserCanChange := h.validateEditSession(ctx, b, editMesID, replyEditMesID, query, exists)

	if !isUserCanChange {
		return
	}

	schedule, err := h.db.GetScheduleEntry(ctx, scheduleID) 

	if err != nil {
		msg := fmt.Sprintf("Ошибка при получении записи (пользователь %s).\nОшибка: %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return	
	}

	text, markup := h.GenerateEditScheduleMenu(schedule)

	_, err = b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID: chatID,
		MessageID: editMesID,
		Text: text,
		ReplyMarkup: markup,
	})

	if err != nil {
		msg := fmt.Sprintf("Ошибка при изменении сообщения редактирования очереди (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}
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
		CallbackData: fmt.Sprintf("EditDayOfWeek:%d", sch.ID),
	}
	keyboard = append(keyboard, []models.InlineKeyboardButton{btn})

	btn = models.InlineKeyboardButton{
		Text:         "Изменить время",
		CallbackData: fmt.Sprintf("EditTime:%d", sch.ID),
	}
	keyboard = append(keyboard, []models.InlineKeyboardButton{btn})

	btn = models.InlineKeyboardButton{
		Text:         "Изменить тип недели (сразу меняет по нажатию)",
		CallbackData: fmt.Sprintf("EditTypeOfWeek:%d", sch.ID),
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

	keyboard = append(keyboard, []models.InlineKeyboardButton{btn},  []models.InlineKeyboardButton{BackBtnMainMenu})

	return text, &models.InlineKeyboardMarkup{InlineKeyboard: keyboard} 
}


func (h *BotHandler) LeaveEditSchedule(ctx context.Context, b *bot.Bot, update *models.Update) {
	query := update.CallbackQuery

	userID := query.From.ID
	chatID := query.Message.Message.Chat.ID
	username := query.From.Username

	h.editMu.RLock()
	editMesID, exists := h.editMessages[userID]
	h.editMu.RUnlock()

	replyEditMesID := query.Message.Message.ID

	isUserCanChange := h.validateEditSession(ctx, b, editMesID, replyEditMesID, query, exists)

	if !isUserCanChange {
		return
	}

	_, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: editMesID,
		Text:      "Сессия редактирования завершена",
	})

	if err != nil {
		msg := fmt.Sprintf("Ошибка при выходе из редактирования очереди (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	h.editMu.Lock()
	delete(h.editMessages, userID)
	h.editMu.Unlock()
}

func (h *BotHandler) AddNewSchedule(ctx context.Context, b *bot.Bot, update *models.Update) {
	query := update.CallbackQuery

	userID := query.From.ID
	chatID := query.Message.Message.Chat.ID
	username := query.From.Username

	h.editMu.RLock()
	editMesID, exists := h.editMessages[userID]
	h.editMu.RUnlock()

	replyEditMesID := query.Message.Message.ID

	isUserCanChange := h.validateEditSession(ctx, b, editMesID, replyEditMesID, query, exists)

	if !isUserCanChange {
		return
	}

	h.stateMu.Lock()
	h.userState[userID] = "awaiting_schedule"
	h.stateMu.Unlock()

	_, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: editMesID,
		Text: `Пожалуйста, введите в чат данные новой записи.

Вводить необходимо строго в следующем порядке: день недели, тип недели, время начала (чч:мм:сс), время конца (чч:мм:сс), айди темы чата, любое описание (по желанию).

Пример: понедельник, четная, 11:00:00, 12:00:00, 1243, лабораторное занятие по devops`,

		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{{BackBtnMainMenu}},
		},
	})

	if err != nil {
		msg := fmt.Sprintf("Ошибка при изменении сообщения редактирования очереди (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}
}

func (h *BotHandler) HandleScheduleInput(ctx context.Context, b *bot.Bot, update *models.Update) {
	newSchData := update.Message.Text
	userID := update.Message.From.ID
	username := update.Message.From.Username
	chatID := update.Message.Chat.ID

	h.editMu.RLock()
	editMesID, _ := h.editMessages[userID]
	h.editMu.RUnlock()

	newSchData = strings.ToLower(newSchData)
	newSchData = strings.Trim(newSchData, " ")

	data := strings.Split(newSchData, ",")

	for i := range data {
		data[i] = strings.TrimSpace(data[i])
	}

	if !h.isValidData(data) {
		text := "Неверный формат данных, пожалуйста повторите ввод.\n\nОжидается: <день недели>,<тип недели>,<время начала>,<время окончания>,<ID темы вашего чата>,<Описание (необязательно)>."
		h.EditMesWithError(ctx, b, chatID, editMesID, text)
		return
	}

	startTime, _ := time.Parse("15:04:05", data[2])
	endTime, _ := time.Parse("15:04:05", data[3])
	ThreadID, _ := strconv.Atoi(data[4])
	
	var description *string
	if len(data) == 6 {
		description = &data[5]
	}

	day, okRu := dayRuToEn[data[0]]
	if !okRu {
		day = data[0]
	}
	weekType, okRu := weekTypeRuToEn[data[1]]
	if !okRu {
		weekType = data[1]
	}
	schedule := db.Schedule{
		DayOfWeek:         day,
		WeekType:          weekType,
		StartTime:         startTime,
		EndTime:           endTime,
		ThreadID:          ThreadID,
		ThreadDescription: description,
	}

	err := h.db.AddNewScheduleEntry(ctx, schedule)

	if err != nil {
		log.Printf("Ошибка при добавлении записи в базу данных (пользователь %s). %v", username, err)
		text := "Ошибка во время добавления записи в базу данных. Повторите попытку или обратитесь к модерации"
		h.EditMesWithError(ctx, b, chatID, editMesID, text)
		return
	}

	_, err = b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: editMesID,
		Text: "Новая запись успешно добавлена в расписание.\nИнтерфейс для редактирования повторно отправлен в чат.",
	})

	if err != nil {
		log.Printf("Ошибка редактирования сообщения со старым интерфейсом редактирования (пользователь %s). %v", username, err)
		return
	}

	schedules, err := h.db.GetAllSchedules(ctx)
	if err != nil {
		log.Printf("Ошибка получения очереди (пользователь %s). %v", username, err)
		return
	}

	text, markup := h.GenerateEditMessage(schedules)

	msg, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ReplyMarkup: markup,
	})

	if err != nil {
		log.Printf("Ошибка при попытке повторно отправить сообщения для редактирования расписания (пользователь %s). %v", username, err)
		return
	}

	h.editMu.Lock()
	h.editMessages[userID] = msg.ID
	h.editMu.Unlock()

	h.stateMu.Lock()
	delete(h.userState, userID)
	h.stateMu.Unlock()

	log.Printf("Сообщение для редактирования очереди (id: %d) повторно отправлено в чат", msg.ID)
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
		return false
	}

	_, err = time.Parse("15:04:05", data[3])
	if err != nil {
		return false
	}

	_, err = strconv.Atoi(data[4])
	if err != nil {
		return false
	}

	return true
}

func (h *BotHandler) DeleteScheduleEntry(ctx context.Context, b *bot.Bot, update *models.Update) {
	query := update.CallbackQuery
	data := query.Data

	userID := query.From.ID
	chatID := query.Message.Message.Chat.ID
	username := query.From.Username

	h.editMu.RLock()
	editMesID, exists := h.editMessages[userID]
	h.editMu.RUnlock()

	replyEditMesID := query.Message.Message.ID

	isUserCanChange := h.validateEditSession(ctx, b, editMesID, replyEditMesID, query, exists)

	if !isUserCanChange {
		return
	}

	scheduleID, err := strconv.Atoi(strings.Split(data, ":")[1])
	
	if err != nil {
		msg := fmt.Sprintf("Ошибка при преобразовании айди записи в int (пользователь %s).\nОшибка: %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return	
	}

	err = h.db.DeleteScheduleEntry(ctx, scheduleID)

	if err != nil {
		msg := fmt.Sprintf("Ошибка при удалении записи из расписания (пользователь %s).\n%v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return	
	}
	
	schedules, err := h.db.GetAllSchedules(ctx)

	if err != nil {
		msg := fmt.Sprintf("Ошибка при получении записи (пользователь %s).\nОшибка: %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return	
	}

	_, markup := h.GenerateEditMessage(schedules)
	
	_, err = b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: editMesID,
		Text: "Запись успешно удалена.\nМожете продолжить вносить изменения.\n",
		ReplyMarkup: markup,
	})

	if err != nil {
		msg := fmt.Sprintf("Ошибка при возврате в меню после удаления (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}
}