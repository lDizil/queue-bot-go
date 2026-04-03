package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"queuebot/db"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5"
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

// хендлер — вызывается библиотекой по команде /queue
func (h *BotHandler) SendQueueMessage(ctx context.Context, b *bot.Bot, update *models.Update) {
	scheduleID := 2
	_ = h.sendQueueMessage(ctx, b, update.Message.Chat.ID, scheduleID)
}

// внутренний метод — принимает scheduleID, вызывается из любого места
func (h *BotHandler) sendQueueMessage(ctx context.Context, b *bot.Bot, chatID int64, scheduleID int) error {
	if h.totalSlotsInQueue == 0 || h.amountOfSlotsInRow == 0 {
		log.Println("Не заданы переменные окружения для настройки очереди")
		return errors.New("не заданы переменные окружения для настройки очереди")
	}

	queue, err := h.db.GetQueue(ctx, scheduleID)
	if err != nil {
		log.Println("Ошибка получения очереди")
		return err
	}

	text, markup := h.RenderQueueMessage(queue, scheduleID)

	msg, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ReplyMarkup: markup,
	})

	if err != nil {
		log.Println("Ошибка при отправке очереди в чат: ", err)
		return err
	}

	h.queueMu.Lock()
	h.queueMessages[scheduleID] = msg.ID
	h.queueMu.Unlock()

	log.Printf("Сообщение очереди (id: %d) отправлено в чат", msg.ID)

	return nil
}

func (h *BotHandler) QueueClosedHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: update.CallbackQuery.ID,
		Text:            "Очередь закрыта",
		ShowAlert:       false,
	})
}

func (h *BotHandler) ActualQueueInfo(ctx context.Context, b *bot.Bot, update *models.Update) {
	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: update.CallbackQuery.ID,
		Text:            "Актуальное сообщение с очередью ниже, посмотрите там ⬇️",
		ShowAlert:       false,
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
			Text:            "Очередь закрыта. Не имеет смысла отображать её снова",
			CallbackQueryID: query.ID,
		})
		return
	}

	err = h.sendQueueMessage(ctx, b, chatID, scheduleID)

	if err != nil {
		msg := fmt.Sprintf("Ошибка при повторной отправки очереди (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		Text:            "Новое сообщения для очереди повторно отправлено в чат",
		CallbackQueryID: query.ID,
	})
}

func (h *BotHandler) RenderQueueMessage(queue []db.QueueEntry, scheduleID int) (string, *models.InlineKeyboardMarkup) {
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

	return builder.String(), &models.InlineKeyboardMarkup{InlineKeyboard: keyboard}
}

func (h *BotHandler) updateQueueMessage(ctx context.Context, b *bot.Bot, chatID int64, scheduleID int) (bool, error) {
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

	text, markup := h.RenderQueueMessage(queue, scheduleID)

	b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:      chatID,
		MessageID:   messageID,
		Text:        text,
		ReplyMarkup: markup,
	})

	return true, nil
}

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
