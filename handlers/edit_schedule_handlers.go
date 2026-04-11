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

func (h *BotHandler) Back(ctx context.Context, b *bot.Bot, update *models.Update) {
	query, data, chatID, userID, username, replyEditMesID := h.getAllReplyInfo(update)

	schedules, err := h.db.GetAllSchedules(ctx)
	if err != nil {
		log.Printf("Ошибка получения очереди (пользователь %s). %v", username, err)
		return
	}

	var markup *models.InlineKeyboardMarkup

	parts := strings.Split(data, ":")
	returnTo := parts[1]

	var text string

	switch returnTo {
	case "mainmenu":
		text = "Вы вернулись к предыдущему меню редактирования.\nМожете продолжить вносить изменения.\n"
		_, markup = h.GenerateEditMessage(schedules)
	case "editschmenu":
		scheduleID, err := strconv.Atoi(parts[2])

		if err != nil {
			msg := fmt.Sprintf("Ошибка преобразования айди записи в число (пользователь %s).\nОшибка: %v", username, err)
			h.handleError(ctx, b, query.ID, msg)
			return
		}

		schedule, err := h.db.GetScheduleEntry(ctx, scheduleID)

		if err != nil {
			msg := fmt.Sprintf("Ошибка при получении записи (пользователь %s).\nОшибка: %v", username, err)
			h.handleError(ctx, b, query.ID, msg)
			return
		}

		text, markup = h.GenerateEditScheduleMenu(schedule)
	case "editschtime":
		scheduleID, err := strconv.Atoi(parts[2])

		if err != nil {
			msg := fmt.Sprintf("Ошибка преобразования айди записи в число (пользователь %s).\nОшибка: %v", username, err)
			h.handleError(ctx, b, query.ID, msg)
			return
		}

		text = "Вы вернулись к выбору времени для редактирования"
		markup = h.GenerateEditTimeMenu(scheduleID)
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
		ChatID:      chatID,
		MessageID:   editMesID,
		Text:        text,
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

	newState := userState{enteredAt: time.Now(), chatID: chatID}

	h.stateMu.Lock()
	h.userState[userID] = newState
	h.stateMu.Unlock()

	log.Printf("Сообщение для редактирования очереди (id: %d) отправлено в чат", msg.ID)
}


func (h *BotHandler) EditScheduleEntry(ctx context.Context, b *bot.Bot, update *models.Update) {
	query, data, chatID, userID, username, replyEditMesID := h.getAllReplyInfo(update)

	scheduleID, err := strconv.Atoi(strings.Split(data, ":")[1])

	if err != nil {
		msg := fmt.Sprintf("Ошибка при преобразовании айди записи в int (пользователь %s).\nОшибка: %v", username, err)
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

	schedule, err := h.db.GetScheduleEntry(ctx, scheduleID)

	if err != nil {
		msg := fmt.Sprintf("Ошибка при получении записи (пользователь %s).\nОшибка: %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
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
		return
	}
}

func (h *BotHandler) LeaveEditSchedule(ctx context.Context, b *bot.Bot, update *models.Update) {
	query, _, chatID, userID, username, replyEditMesID := h.getAllReplyInfo(update)

	h.editMu.RLock()
	editMesID, exists := h.editMessages[userID]
	h.editMu.RUnlock()

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

	h.stateMu.Lock()
	delete(h.userState, userID)
	h.stateMu.Unlock()
}

func (h *BotHandler) AddNewSchedule(ctx context.Context, b *bot.Bot, update *models.Update) {
	query, _, chatID, userID, username, replyEditMesID := h.getAllReplyInfo(update)

	h.editMu.RLock()
	editMesID, exists := h.editMessages[userID]
	h.editMu.RUnlock()

	isUserCanChange := h.validateEditSession(ctx, b, editMesID, replyEditMesID, query, exists)

	if !isUserCanChange {
		return
	}
	
	h.stateMu.Lock()
	session := h.userState[userID]
	session.state = "awaiting_schedule"
	h.userState[userID] = session
	h.stateMu.Unlock()

	_, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: editMesID,
		Text: `Пожалуйста, введите в чат данные новой записи.

Вводить необходимо строго в следующем порядке: день недели, тип недели, время начала (чч:мм:сс), время конца (чч:мм:сс), айди темы чата, любое описание (по желанию).

Пример: понедельник, четная, 11:00:00, 12:00:00, 1243, лабораторное занятие по devops`,

		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: [][]models.InlineKeyboardButton{{backBtnMainMenu}},
		},
	})

	if err != nil {
		msg := fmt.Sprintf("Ошибка при изменении сообщения редактирования очереди (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}
}

func (h *BotHandler) HandleScheduleInput(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message

	data := message.Text
	userID := message.From.ID
	username := message.From.Username
	chatID := message.Chat.ID

	h.editMu.RLock()
	editMesID, _ := h.editMessages[userID]
	h.editMu.RUnlock()

	data = strings.ToLower(data)
	data = strings.Trim(data, " ")

	newSchData := strings.Split(data, ",")

	for i := range newSchData {
		newSchData[i] = strings.TrimSpace(newSchData[i])
	}
	
	if !h.isValidData(newSchData) {
		text := "Неверный формат данных, пожалуйста повторите ввод.\n\nОжидается: <день недели>,<тип недели>,<время начала>,<время окончания>,<ID темы вашего чата>,<Описание (необязательно)>."
		h.EditMesWithError(ctx, b, chatID, editMesID, text, backBtnMainMenu)
		return
	}

	startTime, err := time.Parse("15:04:05", newSchData[2])
	if err != nil {
		startTime, err = time.Parse("15:04", newSchData[2])
		if err != nil {
			text := "Неверно задано время начала, повторите ввод.\n\nОжидается: <день недели>,<тип недели>,<время начала>,<время окончания>,<ID темы вашего чата>,<Описание (необязательно)>."
			h.EditMesWithError(ctx, b, chatID, editMesID, text, backBtnMainMenu)
			return
		}
	}

	endTime, err := time.Parse("15:04:05", newSchData[3])
	if err != nil {
		endTime, err = time.Parse("15:04", newSchData[3])
		if err != nil {
			text := "Неверно задано время конца, повторите ввод.\n\nОжидается: <день недели>,<тип недели>,<время начала>,<время окончания>,<ID темы вашего чата>,<Описание (необязательно)>."
			h.EditMesWithError(ctx, b, chatID, editMesID, text, backBtnMainMenu)
			return
		}
	}
	ThreadID, _ := strconv.Atoi(newSchData[4])

	var description *string
	if len(newSchData) == 6 {
		description = &newSchData[5]
	}

	day, okRu := dayRuToEn[newSchData[0]]
	if !okRu {
		day = newSchData[0]
	}
	weekType, okRu := weekTypeRuToEn[newSchData[1]]
	if !okRu {
		weekType = newSchData[1]
	}
	schedule := db.Schedule{
		DayOfWeek:         day,
		WeekType:          weekType,
		StartTime:         startTime,
		EndTime:           endTime,
		ThreadID:          ThreadID,
		ThreadDescription: description,
	}

	id, err := h.db.AddNewScheduleEntry(ctx, schedule)

	if err != nil {
		log.Printf("Ошибка при добавлении записи в базу данных (пользователь %s). %v", username, err)
		text := "Ошибка во время добавления записи в базу данных. Повторите попытку или обратитесь к модерации"
		h.EditMesWithError(ctx, b, chatID, editMesID, text, backBtnMainMenu)
		return
	}
	
	schedule.ID = id

	if h.scheduler != nil {
		h.scheduler.ScheduleNext(ctx, schedule)
	} else {
		log.Println("Ошибка перепланирования, scheduler не был задан и передан в структуру")
	}

	_, err = b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: editMesID,
		Text:      "Новая запись успешно добавлена в расписание.\nИнтерфейс для редактирования повторно отправлен в чат.",
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
	session := h.userState[userID]
	session.state = ""
	h.userState[userID] = session
	h.stateMu.Unlock()

	log.Printf("Сообщение для редактирования очереди (id: %d) повторно отправлено в чат", msg.ID)
}

func (h *BotHandler) DeleteScheduleEntry(ctx context.Context, b *bot.Bot, update *models.Update) {
	query, data, chatID, userID, username, replyEditMesID := h.getAllReplyInfo(update)

	h.editMu.RLock()
	editMesID, exists := h.editMessages[userID]
	h.editMu.RUnlock()

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

	if h.scheduler != nil {
		h.scheduler.RemoveSchedule(scheduleID)
	} else {
		log.Println("Ошибка перепланирования, scheduler не был задан и передан в структуру")
	}

	schedules, err := h.db.GetAllSchedules(ctx)

	if err != nil {
		msg := fmt.Sprintf("Ошибка при получении записи (пользователь %s).\nОшибка: %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	_, markup := h.GenerateEditMessage(schedules)

	_, err = b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:      chatID,
		MessageID:   editMesID,
		Text:        "Запись успешно удалена.\nМожете продолжить вносить изменения.\n",
		ReplyMarkup: markup,
	})

	if err != nil {
		msg := fmt.Sprintf("Ошибка при возврате в меню после удаления (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}
}

func (h *BotHandler) HandleNewTime(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message

	data := message.Text
	userID := message.From.ID
	username := message.From.Username
	chatID := message.Chat.ID

	h.editMu.RLock()
	editMesID := h.editMessages[userID]
	h.editMu.RUnlock()

	data = strings.Trim(data, " ")

	var newTime time.Time
	var err error

	h.stateMu.RLock()
	curState := h.userState[userID]
	h.stateMu.RUnlock()

	backBtnEditTime := GetBackBtnEditTime(curState.scheduleID)

	if newTime, err = time.Parse("15:04", data); err != nil {
		if newTime, err = time.Parse("15:04:05", data); err != nil {
			log.Printf("Ошибка при time.Parse (пользователь %s). %v", username, err)
			text := "Неверный формат данных, пожалуйста повторите ввод.\n\nОжидается время начала: чч:мм:сс или чч:мм"
			h.EditMesWithError(ctx, b, chatID, editMesID, text, backBtnEditTime)
			return
		}
	}

	switch curState.state {
	case "edit_start_time":
		err = h.db.ChangeStartTime(ctx, curState.scheduleID, newTime)
	case "edit_end_time":
		err = h.db.ChangeEndTime(ctx, curState.scheduleID, newTime) 
	}
	
	if err != nil {
		log.Printf("Ошибка при изменении времени в базе данных (пользователь %s). %v", username, err)
		text := "Ошибка при изменении времени в базе данных. Повторите попытку или обратитесь к модерации"
		h.EditMesWithError(ctx, b, chatID, editMesID, text, backBtnEditTime)
		return
	}

	sch, err := h.db.GetScheduleEntry(ctx, curState.scheduleID)

	if err != nil {
		log.Printf("Ошибка при получении обновлённой записи из базы данных (пользователь %s). %v", username, err)
		text := "Ошибка при получении обновлённой записи из базы данных. Повторите попытку или обратитесь к модерации"
		h.EditMesWithError(ctx, b, chatID, editMesID, text, backBtnEditTime)
		return
	}

	err = h.rescheduleWithSch(ctx, curState.scheduleID, sch) 
	if err != nil {
		log.Printf("Ошибка во время перепланирования записи (пользователь %s). %v", username, err)
		text := "Ошибка при изменении времени в базе данных. Повторите попытку или обратитесь к модерации"
		h.EditMesWithError(ctx, b, chatID, editMesID, text, backBtnEditTime)
		return
	}

	text, markup := h.GenerateEditScheduleMenu(sch)
	
	_, err = b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: editMesID,
		Text:      "Выбранное время успешно изменено.\nИнтерфейс для редактирования повторно отправлен в чат.",
	})

	if err != nil {
		log.Printf("Ошибка изменения сообщения со старым меню редактирования (пользователь %s). %v", username, err)
		text := "Ошибка изменения сообщения со старым меню редактирования. Повторите попытку или обратитесь к модерации"
		h.EditMesWithError(ctx, b, chatID, editMesID, text, backBtnEditTime)
		return
	}

	msg, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ReplyMarkup: markup,
	})

	if err != nil {
		log.Println("Ошибка при отправке нового меню редактирования записи: ", err)
		text := "Ошибка при отправке нового меню редактирования записи. Повторите попытку или обратитесь к модерации"
		h.EditMesWithError(ctx, b, chatID, editMesID, text, backBtnEditTime)
		return 
	}

	h.editMu.Lock()
	h.editMessages[userID] = msg.ID
	h.editMu.Unlock()

	h.stateMu.Lock()
	session := h.userState[userID]
	session.state = ""
	h.userState[userID] = session
	h.stateMu.Unlock()
}

func (h *BotHandler) HandleNewThreadID(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message

	data := message.Text
	userID := message.From.ID
	username := message.From.Username
	chatID := message.Chat.ID

	h.editMu.RLock()
	editMesID := h.editMessages[userID]
	h.editMu.RUnlock()

	data = strings.Trim(data, " ")

	h.stateMu.RLock()
	curState := h.userState[userID]
	h.stateMu.RUnlock()

	backBtnEditSch := GetBackBtnEditSch(curState.scheduleID)
	var err error

	newThread, err := strconv.Atoi(data)
	if err != nil {
		log.Printf("Ошибка при преобразовании айди темы чата в int (пользователь %s). %v", username, err)
		text := "Ошибка при преобразовании айди темы чата в int. Повторите попытку или обратитесь к модерации"
		h.EditMesWithError(ctx, b, chatID, editMesID, text, backBtnEditSch)
		return
	}

	err = h.db.ChangeThreadID(ctx, curState.scheduleID, newThread)
	
	if err != nil {
		log.Printf("Ошибка при изменении времени в базе данных (пользователь %s). %v", username, err)
		text := "Ошибка при изменении времени в базе данных. Повторите попытку или обратитесь к модерации"
		h.EditMesWithError(ctx, b, chatID, editMesID, text, backBtnEditSch)
		return
	}

	sch, err := h.db.GetScheduleEntry(ctx, curState.scheduleID)

	if err != nil {
		log.Printf("Ошибка при получении обновлённой записи из базы данных (пользователь %s). %v", username, err)
		text := "Ошибка при получении обновлённой записи из базы данных. Повторите попытку или обратитесь к модерации"
		h.EditMesWithError(ctx, b, chatID, editMesID, text, backBtnEditSch)
		return
	}

	text, markup := h.GenerateEditScheduleMenu(sch)
	
	_, err = b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: editMesID,
		Text:      "Айди темы чата успешно изменено.\nИнтерфейс для редактирования повторно отправлен в чат.",
	})

	if err != nil {
		log.Printf("Ошибка изменения сообщения со старым меню редактирования (пользователь %s). %v", username, err)
		text := "Ошибка изменения сообщения со старым меню редактирования. Повторите попытку или обратитесь к модерации"
		h.EditMesWithError(ctx, b, chatID, editMesID, text, backBtnEditSch)
		return
	}

	msg, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ReplyMarkup: markup,
	})

	if err != nil {
		log.Println("Ошибка при отправке нового меню редактирования записи: ", err)
		text := "Ошибка при отправке нового меню редактирования записи. Повторите попытку или обратитесь к модерации"
		h.EditMesWithError(ctx, b, chatID, editMesID, text, backBtnEditSch)
		return 
	}

	h.editMu.Lock()
	h.editMessages[userID] = msg.ID
	h.editMu.Unlock()

	h.stateMu.Lock()
	session := h.userState[userID]
	session.state = ""
	h.userState[userID] = session
	h.stateMu.Unlock()
}

func (h *BotHandler) HandleNewDescription(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message

	data := message.Text
	userID := message.From.ID
	username := message.From.Username
	chatID := message.Chat.ID

	h.editMu.RLock()
	editMesID := h.editMessages[userID]
	h.editMu.RUnlock()

	description := strings.Trim(data, " ")

	h.stateMu.RLock()
	curState := h.userState[userID]
	h.stateMu.RUnlock()

	backBtnEditSch := GetBackBtnEditSch(curState.scheduleID)
	var err error

	err = h.db.ChangeDescription(ctx, curState.scheduleID, description)
	
	if err != nil {
		log.Printf("Ошибка при изменении описания в базе данных (пользователь %s). %v", username, err)
		text := "Ошибка при изменении описания в базе данных. Повторите попытку или обратитесь к модерации"
		h.EditMesWithError(ctx, b, chatID, editMesID, text, backBtnEditSch)
		return
	}

	sch, err := h.db.GetScheduleEntry(ctx, curState.scheduleID)

	if err != nil {
		log.Printf("Ошибка при получении обновлённой записи из базы данных (пользователь %s). %v", username, err)
		text := "Ошибка при получении обновлённой записи из базы данных. Повторите попытку или обратитесь к модерации"
		h.EditMesWithError(ctx, b, chatID, editMesID, text, backBtnEditSch)
		return
	}

	text, markup := h.GenerateEditScheduleMenu(sch)
	
	_, err = b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: editMesID,
		Text:      "Описание записи успешно изменено.\nИнтерфейс для редактирования повторно отправлен в чат.",
	})

	if err != nil {
		log.Printf("Ошибка изменения сообщения со старым меню редактирования (пользователь %s). %v", username, err)
		text := "Ошибка изменения сообщения со старым меню редактирования. Повторите попытку или обратитесь к модерации"
		h.EditMesWithError(ctx, b, chatID, editMesID, text, backBtnEditSch)
		return
	}

	msg, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ReplyMarkup: markup,
	})

	if err != nil {
		log.Println("Ошибка при отправке нового меню редактирования записи: ", err)
		text := "Ошибка при отправке нового меню редактирования записи. Повторите попытку или обратитесь к модерации"
		h.EditMesWithError(ctx, b, chatID, editMesID, text, backBtnEditSch)
		return 
	}

	h.editMu.Lock()
	h.editMessages[userID] = msg.ID
	h.editMu.Unlock()

	h.stateMu.Lock()
	session := h.userState[userID]
	session.state = ""
	h.userState[userID] = session
	h.stateMu.Unlock()
}
