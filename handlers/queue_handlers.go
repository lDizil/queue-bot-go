package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"queuebot/db"
	u "queuebot/utils"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (h *BotHandler) RunOneShotQueue(ctx context.Context, b *bot.Bot, update *models.Update) {
	threadID := update.Message.MessageThreadID

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          update.Message.Chat.ID,
		MessageThreadID: threadID,
		Text:            "Одноразовая очередь будет запущена через 6 минут.",
	})

	h.scheduler.RunInstant(ctx, threadID)
}

func (h *BotHandler) sendQueueMessage(ctx context.Context, b *bot.Bot, chatID int64, scheduleID int, threadID int, statusQueue u.QueueStatus) (int, error) {
	if h.totalSlotsInQueue == 0 || h.amountOfSlotsInRow == 0 {
		log.Println("Не заданы переменные окружения для настройки очереди")
		return 0, errors.New("не заданы переменные окружения для настройки очереди")
	}

	queue, err := h.db.GetQueue(ctx, scheduleID)
	if err != nil {
		log.Println("Ошибка получения очереди")
		return 0, err
	}

	text, markup := h.RenderQueueMessage(queue, scheduleID, statusQueue)

	msg, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          chatID,
		MessageThreadID: threadID,
		Text:            text,
		ReplyMarkup:     markup,
	})

	if err != nil {
		log.Println("Ошибка при отправке очереди в чат: ", err)
		return 0, err
	}

	h.queueMu.Lock()
	h.queueMessages[scheduleID] = msg.ID
	h.queueMu.Unlock()

	log.Printf("Сообщение очереди (id: %d) отправлено в чат", msg.ID)

	return msg.ID, nil
}

func (h *BotHandler) ActualQueueInfo(ctx context.Context, b *bot.Bot, update *models.Update) {
	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: update.CallbackQuery.ID,
		Text:            "Актуальное сообщение с очередью ниже, посмотрите там ⬇️",
		ShowAlert:       true,
	})
}

func (h *BotHandler) QueueClosed(ctx context.Context, b *bot.Bot, update *models.Update) {
	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: update.CallbackQuery.ID,
		Text:            "Очередь закрыта",
		ShowAlert:       true,
	})
}

func (h *BotHandler) SendQueueAgain(ctx context.Context, b *bot.Bot, update *models.Update) {
	query := update.CallbackQuery
	data := query.Data

	msg := query.Message.Message
	chatID := msg.Chat.ID

	username := query.From.Username

	scheduleID, err := h.parseScheduleID(ctx, b, query, data, username)
	if err != nil {
		return
	}

	h.queueMu.RLock()
	oldMessageID, exists := h.queueMessages[scheduleID]
	h.queueMu.RUnlock()

	if exists {
		btn := models.InlineKeyboardButton{
			Text:         "⬇️ Актуальное сообщение ниже ⬇️",
			CallbackData: "ActualQueue",
		}
		b.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
			ChatID:    chatID,
			MessageID: oldMessageID,
			ReplyMarkup: &models.InlineKeyboardMarkup{
				InlineKeyboard: [][]models.InlineKeyboardButton{{btn}},
			},
		})
	} else {
		b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			Text:            "Очередь закрыта. Нет смысла отображать её снова",
			CallbackQueryID: query.ID,
			ShowAlert:       true,
		})
		return
	}

	schedule, err := h.db.GetScheduleEntry(ctx, scheduleID)
	if err != nil {
		msg := fmt.Sprintf("Ошибка получения расписания (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	isOpen := h.isQueueOpen(schedule)

	var status u.QueueStatus
	if isOpen {
		status = u.QueueOpen
	} else {
		status = u.QueueClosed
	}

	_, err = h.sendQueueMessage(ctx, b, chatID, scheduleID, schedule.ThreadID, status)

	if err != nil {
		msg := fmt.Sprintf("Ошибка при повторной отправки очереди (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		Text:            "Новое сообщения для очереди повторно отправлено в чат",
		CallbackQueryID: query.ID,
		ShowAlert:       true,
	})
}

func (h *BotHandler) RenderQueueMessage(queue []db.QueueEntry, scheduleID int, statusQueue u.QueueStatus) (string, *models.InlineKeyboardMarkup) {
	taken := make(map[int]db.QueueEntry, h.totalSlotsInQueue)
	isTaken := map[int]bool{}

	for i := 1; i <= h.totalSlotsInQueue; i++ {
		isTaken[i] = false
	}

	for _, entry := range queue {
		taken[entry.Position] = entry
	}

	var builder strings.Builder

	text := "Это сообщение - таблица для записи в очередь\nДля выбора места воспользуйтесь кнопками снизу\nУдачной сдачи работы \n\n"
	builder.Grow(120 + len(queue)*30 + h.totalSlotsInQueue*4)

	builder.WriteString(text)

	if len(queue) == 0 {
		builder.WriteString("Очередь пуста\n\n")
		builder.WriteString("Свободные места: ")

		free := []string{}

		for i := 1; i <= h.totalSlotsInQueue; i++ {
			free = append(free, fmt.Sprintf("%d", i))
		}

		builder.WriteString(strings.Join(free, ", "))
	} else {
		builder.WriteString("Текущая очередь:\n\n")
		for _, entry := range queue {
			isTaken[entry.Position] = true
			fmt.Fprintf(&builder, "%d. @%s\n", entry.Position, entry.Username)
		}
		builder.WriteString("\nСвободные места: ")

		free := []string{}
		for i := 1; i <= h.totalSlotsInQueue; i++ {
			if !isTaken[i] {
				free = append(free, fmt.Sprintf("%d", i))
			}
		}
		builder.WriteString(strings.Join(free, ", "))
	}

	keyboard := [][]models.InlineKeyboardButton{}
	row := []models.InlineKeyboardButton{}

	for i := 1; i <= h.totalSlotsInQueue; i++ {
		var btn models.InlineKeyboardButton

		if _, ok := taken[i]; ok {
			btn = models.InlineKeyboardButton{
				Text:         "❌",
				CallbackData: fmt.Sprintf("JoinBusySlot:%d:%d", scheduleID, i),
			}
		} else {
			btn = models.InlineKeyboardButton{
				Text:         fmt.Sprintf("%d", i),
				CallbackData: fmt.Sprintf("Join:%d:%d", scheduleID, i),
			}
		}

		row = append(row, btn)

		if len(row) >= h.amountOfSlotsInRow {
			keyboard = append(keyboard, row)
			row = []models.InlineKeyboardButton{}
		}
	}

	if len(row) != 0 {
		keyboard = append(keyboard, row)
	}

	btnJoinQueue := models.InlineKeyboardButton{
		Text:         "Занять ближайшее свободное место",
		CallbackData: fmt.Sprintf("JoinFirstFreeslot:%d", scheduleID),
	}

	btnLeaveQueue := models.InlineKeyboardButton{
		Text:         "Выйти из очереди",
		CallbackData: fmt.Sprintf("LeaveFromQueue:%d", scheduleID),
	}

	btnSendQueueAgain := models.InlineKeyboardButton{
		Text:         "Отправить очередь повторно",
		CallbackData: fmt.Sprintf("SendQueueAgain:%d", scheduleID),
	}

	keyboard = append(keyboard, []models.InlineKeyboardButton{btnJoinQueue}, []models.InlineKeyboardButton{btnLeaveQueue}, []models.InlineKeyboardButton{btnSendQueueAgain})

	var status string
	switch statusQueue {
	case u.QueuePending:
		status = "\n\n🔴 Очередь закрыта 🔴"
	case u.QueueOpen:
		status = "\n\n🟢 Очередь открыта 🟢"
	case u.QueueClosed:
		status = "\n\n🔴 Очередь закрыта 🔴"
		keyboard = [][]models.InlineKeyboardButton{}

		btn := models.InlineKeyboardButton{
			Text:         "❌ Очередь закрыта ❌",
			CallbackData: "CloseQueue",
		}

		keyboard = append(keyboard, []models.InlineKeyboardButton{btn})
	}

	builder.WriteString(status)

	return builder.String(), &models.InlineKeyboardMarkup{InlineKeyboard: keyboard}
}

func (h *BotHandler) updateQueueMessage(ctx context.Context, b *bot.Bot, chatID int64, scheduleID int, statusQueue u.QueueStatus) (bool, error) {
	queue, err := h.db.GetQueue(ctx, scheduleID)

	if err != nil {
		log.Println("Ошибка получения очереди")
		return false, err
	}

	h.queueMu.RLock()
	messageID, exists := h.queueMessages[scheduleID]
	h.queueMu.RUnlock()

	if !exists {
		log.Println("Очередь закрыта и недоступна для записи")
		return false, nil
	}

	text, markup := h.RenderQueueMessage(queue, scheduleID, statusQueue)

	b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:      chatID,
		MessageID:   messageID,
		Text:        text,
		ReplyMarkup: markup,
	})

	return true, nil
}

func (h *BotHandler) SendScheduledQueue(ctx context.Context, b *bot.Bot, chatID int64, scheduleID int, threadID int, statusQueue u.QueueStatus) (int, error) {
	msgID, err := h.sendQueueMessage(ctx, b, chatID, scheduleID, threadID, statusQueue)
	if err != nil {
		log.Printf("Ошибка при отправке очереди %d в чат %d. %v", scheduleID, chatID, err)
		return 0, err
	}

	err = h.db.SetQueueMessageID(ctx, scheduleID, msgID)
	if err != nil {
		log.Printf("Ошибка записи айди сообщения очереди %d в базу данных. %v", scheduleID, err)
		return 0, err
	}

	h.SetQueueMessage(scheduleID, msgID)

	return msgID, nil
}

func (h *BotHandler) EditScheduledQueue(ctx context.Context, b *bot.Bot, chatID int64, scheduleID int, statusQueue u.QueueStatus) error {
	isUpdated, err := h.updateQueueMessage(ctx, b, chatID, scheduleID, statusQueue)
	if !isUpdated && err != nil {
		log.Printf("Ошибка обновления сообщения с очередью %v", err)
	}
	return err
}

func (h *BotHandler) SendNotification5min(ctx context.Context, b *bot.Bot, chatID int64, threadID int, scheduleID int) error {
	_, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          chatID,
		MessageThreadID: threadID,
		Text:            "5 минут до начала пары. Скоро можно будет записаться в очередь.",
	})

	if err != nil {
		log.Printf("Ошибка отправки оповещения об открытии за 5 минут %v", err)
		return err
	}

	return nil
}

func (h *BotHandler) SendNotification1min(ctx context.Context, b *bot.Bot, chatID int64, threadID int, scheduleID int) error {
	_, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          chatID,
		MessageThreadID: threadID,
		Text:            "1 минута до открытия очереди. Очередь отправлена в следующем сообщении.",
	})

	if err != nil {
		log.Printf("Ошибка отправки оповещения об открытии за 1 минуту: %v", err)
		return err
	}

	return nil
}

func (h *BotHandler) ClearScheduledQueue(ctx context.Context, b *bot.Bot, chatID int64, scheduleID int, statusQueue u.QueueStatus) error {
	var firstErr error

	_, err := h.updateQueueMessage(ctx, b, chatID, scheduleID, statusQueue)
	if err != nil {
		log.Printf("Ошибка обновления сообщения с очередью: %v", err)
		firstErr = err
	}

	err = h.db.ClearQueue(ctx, scheduleID)
	if err != nil {
		log.Printf("Ошибка очистки очереди в базе данных: %v", err)
		if firstErr == nil {
			firstErr = err
		}
	}

	err = h.db.ClearQueueMessageID(ctx, scheduleID)
	if err != nil {
		log.Printf("Ошибка удаления айди сообщения с очередью из базы данных: %v", err)
		if firstErr == nil {
			firstErr = err
		}
	}

	return firstErr
}
