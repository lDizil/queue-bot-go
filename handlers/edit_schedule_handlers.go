package handlers

import (
	"context"
	"encoding/csv"
	"fmt"
	"log"
	"queuebot/access"
	"queuebot/db"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (h *BotHandler) Back(ctx context.Context, b *bot.Bot, update *models.Update) {
	query, data, chatID, userID, username := h.getAllReplyInfo(update)
	h.stateMu.Lock()
	session := h.userState[userID]
	session.state, session.scheduleID = "", 0
	h.userState[userID] = session
	h.stateMu.Unlock()

	var text string
	var markup *models.InlineKeyboardMarkup
	var err error
	parts := strings.Split(data, ":")
	switch parts[1] {
	case "mainmenu":
		text, markup, err = h.selectedChatMenu(ctx, userID)
	case "editschmenu":
		scheduleID, parseErr := strconv.Atoi(parts[2])
		if parseErr != nil {
			return
		}
		var schedule db.Schedule
		schedule, err = h.selectedSchedule(ctx, userID, scheduleID)
		if err == nil {
			text, markup = h.GenerateEditScheduleMenu(schedule)
		}
	case "editschtime":
		scheduleID, parseErr := strconv.Atoi(parts[2])
		if parseErr != nil {
			return
		}
		text = "Выберите время для редактирования"
		markup = h.GenerateEditTimeMenu(scheduleID)
	default:
		return
	}
	if err != nil {
		h.handleError(ctx, b, query.ID, fmt.Sprintf("return to selected chat menu (user %s): %v", username, err))
		return
	}
	h.editConfigText(ctx, b, chatID, query.Message.Message.ID, text, markup)
	h.answer(ctx, b, query, "")
}

func (h *BotHandler) EditScheduleReplyText(ctx context.Context, b *bot.Bot, update *models.Update) {
	msg := update.Message
	if err := h.closeMenu(ctx, b, msg.From.ID); err != nil {
		log.Print("Не удалось удалить предыдущее меню")
	}
	if h.chats != nil && msg.Chat.ID < 0 && h.allowed(ctx, msg.From.ID, access.ManageChats) {
		if err := h.chats.Remember(ctx, msg.Chat.ID, msg.Chat.Title); err != nil {
			log.Print("Не удалось сохранить название чата")
		}
	}
	text, markup, err := h.chatListMenu(ctx, 0, msg.From.ID)
	if err != nil {
		h.sendSwapInfoMessage(ctx, b, msg.Chat.ID, msg.MessageThreadID, "Не удалось получить список чатов. Повторите /edit_schedule")
		return
	}
	if _, err := h.sendMenu(ctx, b, update, "schedule", text, markup); err != nil {
		log.Print("Не удалось открыть меню расписания")
	}
}

func (h *BotHandler) EditScheduleEntry(ctx context.Context, b *bot.Bot, update *models.Update) {
	query, data, chatID, userID, username := h.getAllReplyInfo(update)

	scheduleID, err := strconv.Atoi(strings.Split(data, ":")[1])

	if err != nil {
		msg := fmt.Sprintf("Ошибка при преобразовании айди записи в int (пользователь %s).\nОшибка: %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	h.editMu.RLock()
	editMesID, _ := h.editMessages[userID]
	h.editMu.RUnlock()

	schedule, err := h.selectedSchedule(ctx, userID, scheduleID)

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
	if err := h.closeMenu(ctx, b, update.CallbackQuery.From.ID); err != nil {
		log.Print("Не удалось удалить меню расписания")
	}
	h.answer(ctx, b, update.CallbackQuery, "")
}

func (h *BotHandler) AddNewSchedule(ctx context.Context, b *bot.Bot, update *models.Update) {
	query, _, chatID, userID, username := h.getAllReplyInfo(update)

	h.editMu.RLock()
	editMesID, _ := h.editMessages[userID]
	h.editMu.RUnlock()

	h.stateMu.Lock()
	session := h.userState[userID]
	session.state = "awaiting_schedule"
	h.userState[userID] = session
	h.stateMu.Unlock()

	_, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: editMesID,
		Text: `Пожалуйста, введите в чат данные новой записи.

Порядок: день недели, тип недели, время начала, время конца, айди темы чата, описание (необязательно), заголовок очереди (необязательно).

Пример: понедельник, нечётная, 11:00, 12:30, 1243, лабораторное занятие, БГГ323 — Высоконагруженные приложения

Заголовок появится первой строкой в сообщении очереди. Если описание не нужно, оставьте его поле пустым. Текст с запятыми заключайте в двойные кавычки.`,

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

	h.stateMu.RLock()
	curState := h.userState[userID]
	h.stateMu.RUnlock()
	if h.chats == nil || curState.targetChatID == 0 {
		h.EditMesWithError(ctx, b, chatID, editMesID, "Сначала выберите чат", models.InlineKeyboardButton{Text: "К чатам", CallbackData: "ChatList"})
		return
	}
	if _, err := h.chats.Get(ctx, curState.targetChatID); err != nil {
		h.EditMesWithError(ctx, b, chatID, editMesID, "Выбранный чат недоступен", models.InlineKeyboardButton{Text: "К чатам", CallbackData: "ChatList"})
		return
	}

	reader := csv.NewReader(strings.NewReader(strings.TrimSpace(data)))
	reader.TrimLeadingSpace = true
	newSchData, parseErr := reader.Read()

	for i := range newSchData {
		newSchData[i] = strings.TrimSpace(newSchData[i])
	}
	if len(newSchData) > 1 {
		newSchData[0] = strings.ToLower(newSchData[0])
		newSchData[1] = strings.ToLower(newSchData[1])
	}

	if parseErr != nil || !h.isValidData(newSchData) {
		text := "Неверный формат данных, пожалуйста повторите ввод.\n\nОжидается: <день недели>,<тип недели>,<время начала>,<время окончания>,<ID темы вашего чата>,<Описание (необязательно)>,<Заголовок (необязательно)>."
		h.EditMesWithError(ctx, b, chatID, editMesID, text, backBtnMainMenu)
		return
	}

	startTime, err := time.Parse("15:04:05", newSchData[2])
	if err != nil {
		startTime, err = time.Parse("15:04", newSchData[2])
		if err != nil {
			text := "Неверно задано время начала, повторите ввод.\n\nОжидается: <день недели>,<тип недели>,<время начала>,<время окончания>,<ID темы вашего чата>,<Описание (необязательно)>,<Заголовок (необязательно)>."
			h.EditMesWithError(ctx, b, chatID, editMesID, text, backBtnMainMenu)
			return
		}
	}

	endTime, err := time.Parse("15:04:05", newSchData[3])
	if err != nil {
		endTime, err = time.Parse("15:04", newSchData[3])
		if err != nil {
			text := "Неверно задано время конца, повторите ввод.\n\nОжидается: <день недели>,<тип недели>,<время начала>,<время окончания>,<ID темы вашего чата>,<Описание (необязательно)>,<Заголовок (необязательно)>."
			h.EditMesWithError(ctx, b, chatID, editMesID, text, backBtnMainMenu)
			return
		}
	}
	ThreadID, _ := strconv.Atoi(newSchData[4])

	var description *string
	if len(newSchData) >= 6 {
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
		ChatID:            curState.targetChatID,
		DayOfWeek:         day,
		WeekType:          weekType,
		StartTime:         startTime,
		EndTime:           endTime,
		ThreadID:          ThreadID,
		ThreadDescription: description,
	}
	if len(newSchData) == 7 {
		schedule.Title = newSchData[6]
	}
	if schedule.ChatID == 0 {
		h.EditMesWithError(ctx, b, chatID, editMesID, "Откройте меню в группе, где нужна очередь", backBtnMainMenu)
		return
	}

	id, err := h.db.AddNewScheduleEntry(ctx, schedule)

	if err != nil {
		log.Printf("Ошибка при добавлении записи в базу данных (пользователь %s). %v", username, err)
		text := "Ошибка во время добавления записи в базу данных. Повторите попытку или обратитесь к модерации"
		h.EditMesWithError(ctx, b, chatID, editMesID, text, backBtnMainMenu)
		return
	}

	schedule.ID = id
	h.stateMu.Lock()
	session := h.userState[userID]
	session.state = ""
	session.scheduleID = 0
	h.userState[userID] = session
	h.stateMu.Unlock()

	if h.scheduler != nil {
		h.scheduler.ScheduleNext(schedule)
	} else {
		log.Println("Ошибка перепланирования, scheduler не был задан и передан в структуру")
	}

	text, markup, err := h.selectedChatMenu(ctx, userID)
	if err != nil {
		log.Print("Не удалось получить расписание после сохранения")
		return
	}
	if err := h.editMenu(ctx, b, userID, text, markup); err != nil {
		log.Print("Не удалось обновить меню после сохранения записи")
	}
}

func (h *BotHandler) DeleteScheduleEntry(ctx context.Context, b *bot.Bot, update *models.Update) {
	query, data, chatID, userID, username := h.getAllReplyInfo(update)

	h.editMu.RLock()
	editMesID, _ := h.editMessages[userID]
	h.editMu.RUnlock()

	scheduleID, err := strconv.Atoi(strings.Split(data, ":")[1])

	if err != nil {
		msg := fmt.Sprintf("Ошибка при преобразовании айди записи в int (пользователь %s).\nОшибка: %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	err = h.queues.Delete(ctx, scheduleID)

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

	h.stateMu.Lock()
	session := h.userState[userID]
	session.state, session.scheduleID = "", 0
	h.userState[userID] = session
	h.stateMu.Unlock()
	text, markup, err := h.selectedChatMenu(ctx, userID)
	if err != nil {
		h.handleError(ctx, b, query.ID, fmt.Sprintf("read selected chat after deleting schedule: %v", err))
		return
	}

	_, err = b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:      chatID,
		MessageID:   editMesID,
		Text:        "Запись успешно удалена.\n\n" + text,
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
	previous, err := h.db.GetScheduleEntry(ctx, curState.scheduleID)
	if err != nil {
		h.EditMesWithError(ctx, b, chatID, editMesID, "Не удалось прочитать расписание", backBtnEditTime)
		return
	}
	other := previous.EndTime
	if curState.state == "edit_end_time" {
		other = previous.StartTime
	}
	if newTime.Format("15:04:05") == other.Format("15:04:05") {
		h.EditMesWithError(ctx, b, chatID, editMesID, "Начало и конец должны отличаться", backBtnEditTime)
		return
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

	h.stateMu.Lock()
	session := h.userState[userID]
	session.state = ""
	session.scheduleID = 0
	h.userState[userID] = session
	h.stateMu.Unlock()
	text, markup := h.GenerateEditScheduleMenu(sch)
	if err := h.editMenu(ctx, b, userID, text, markup); err != nil {
		log.Print("Не удалось обновить меню после сохранения записи")
	}
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
	if err != nil || newThread < 0 {
		log.Printf("Неверный айди темы чата (пользователь %s)", username)
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

	if err := h.rescheduleWithSch(ctx, curState.scheduleID, sch); err != nil {
		log.Printf("reschedule edited topic: %v", err)
	}

	h.stateMu.Lock()
	session := h.userState[userID]
	session.state = ""
	session.scheduleID = 0
	h.userState[userID] = session
	h.stateMu.Unlock()
	text, markup := h.GenerateEditScheduleMenu(sch)
	if err := h.editMenu(ctx, b, userID, text, markup); err != nil {
		log.Print("Не удалось обновить меню после сохранения записи")
	}
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

	if len([]rune(description)) > 500 {
		h.EditMesWithError(ctx, b, chatID, editMesID, "Описание должно быть не длиннее 500 символов", backBtnEditSch)
		return
	}
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

	h.stateMu.Lock()
	session := h.userState[userID]
	session.state = ""
	session.scheduleID = 0
	h.userState[userID] = session
	h.stateMu.Unlock()
	text, markup := h.GenerateEditScheduleMenu(sch)
	if err := h.editMenu(ctx, b, userID, text, markup); err != nil {
		log.Print("Не удалось обновить меню после сохранения записи")
	}
}
