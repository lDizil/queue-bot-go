package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"queuebot/db"
	"queuebot/queue"
	u "queuebot/utils"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

var _ queue.Messenger = (*BotHandler)(nil)
var _ queue.LegacyMessenger = (*BotHandler)(nil)

func (h *BotHandler) RunOneShotQueue(ctx context.Context, b *bot.Bot, update *models.Update) {
	msg := update.Message
	title := strings.TrimSpace(strings.TrimPrefix(msg.Text, strings.Fields(msg.Text)[0]))
	target, err := h.commandTarget(ctx, msg.Chat.ID)
	if err != nil {
		h.sendSwapInfoMessage(ctx, b, msg.Chat.ID, msg.MessageThreadID, "Не удалось определить чат. Выберите основной через /edit_schedule или выполните команду в нужной группе")
		return
	}
	if len([]rune(title)) > 200 {
		h.sendSwapInfoMessage(ctx, b, msg.Chat.ID, msg.MessageThreadID, "Заголовок должен быть не длиннее 200 символов")
		return
	}
	if err := h.scheduler.RunInstant(ctx, target, msg.MessageThreadID, title); err != nil {
		h.sendSwapInfoMessage(ctx, b, msg.Chat.ID, msg.MessageThreadID, queue.ErrorText(err))
		return
	}
	h.sendSwapInfoMessage(ctx, b, msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("Одноразовая очередь откроется через %s.", configDuration(h.settings.Values().OneShotDelay)))
}

func (h *BotHandler) ActualQueueInfo(ctx context.Context, b *bot.Bot, update *models.Update) {
	h.answer(ctx, b, update.CallbackQuery, "Актуальное сообщение с очередью ниже ⬇️")
}

func (h *BotHandler) QueueClosed(ctx context.Context, b *bot.Bot, update *models.Update) {
	h.answer(ctx, b, update.CallbackQuery, "Очередь закрыта")
}

func (h *BotHandler) SendQueueAgain(ctx context.Context, b *bot.Bot, update *models.Update) {
	ref, ok := h.queueReference(ctx, b, update)
	if !ok {
		return
	}
	if err := h.queues.Repost(ctx, ref); err != nil {
		h.queueError(ctx, b, update.CallbackQuery, err)
		return
	}
	h.answer(ctx, b, update.CallbackQuery, "Очередь отправлена ниже")
}

func (h *BotHandler) RenderQueueMessage(entries []db.QueueEntry, scheduleID int, status u.QueueStatus) (string, *models.InlineKeyboardMarkup) {
	return h.renderQueue(queue.Snapshot{Schedule: db.Schedule{ID: scheduleID, ActiveCapacity: h.totalSlotsInQueue, ActiveColumns: h.amountOfSlotsInRow}, Entries: entries, Status: status})
}

func (h *BotHandler) renderQueue(snapshot queue.Snapshot) (string, *models.InlineKeyboardMarkup) {
	var (
		schedule = snapshot.Schedule
		capacity = schedule.ActiveCapacity
		columns  = schedule.ActiveColumns
		builder  strings.Builder
	)
	if capacity < 1 {
		capacity = h.totalSlotsInQueue
	}
	if columns < 1 {
		columns = h.amountOfSlotsInRow
	}
	if columns < 1 {
		columns = 5
	}
	if schedule.Title != "" {
		fmt.Fprintf(&builder, "%s\n\n", queueLabel(schedule.Title, 200))
	}
	builder.WriteString("Это сообщение — таблица для записи в очередь\nДля выбора места воспользуйтесь кнопками снизу\nУдачной сдачи работы!\n\n")
	taken := make(map[int]db.QueueEntry, len(snapshot.Entries))
	if len(snapshot.Entries) == 0 {
		builder.WriteString("Очередь пуста\n")
	} else {
		builder.WriteString("Текущая очередь:\n\n")
		for _, entry := range snapshot.Entries {
			taken[entry.Position] = entry
			name := queueLabel(strings.Join(strings.Fields(entry.DisplayName), " "), 32)
			if entry.Username != "" {
				name = "@" + queueLabel(entry.Username, 32)
			}
			if name == "" {
				name = "Участник"
			}
			mark := ""
			if entry.Completed {
				mark = " ✅"
			}
			fmt.Fprintf(&builder, "%d. %s%s\n", entry.Position, name, mark)
		}
	}
	var free []string
	var keyboard [][]models.InlineKeyboardButton
	var row []models.InlineKeyboardButton
	for position := 1; position <= capacity; position++ {
		entry, occupied := taken[position]
		button := models.InlineKeyboardButton{Text: strconv.Itoa(position), CallbackData: fmt.Sprintf("Join:%d:%d", schedule.ID, position)}
		if occupied {
			mark := "⏳"
			if entry.Completed {
				mark = "✅"
			}
			button.Text = fmt.Sprintf("%d %s", position, mark)
			button.CallbackData = fmt.Sprintf("CompleteSlot:%d:%d:%d:%t", schedule.ID, entry.ID, position, !entry.Completed)
		} else {
			free = append(free, strconv.Itoa(position))
		}
		row = append(row, button)
		if len(row) == columns {
			keyboard = append(keyboard, row)
			row = nil
		}
	}
	if len(row) > 0 {
		keyboard = append(keyboard, row)
	}
	if len(free) > 0 {
		fmt.Fprintf(&builder, "\nСвободные места: %s", strings.Join(free, ", "))
	} else {
		builder.WriteString("\nСвободных мест нет")
	}
	keyboard = append(keyboard,
		[]models.InlineKeyboardButton{{Text: "Занять ближайшее свободное место", CallbackData: fmt.Sprintf("JoinFirstFreeslot:%d", schedule.ID)}},
		[]models.InlineKeyboardButton{{Text: "Сдал", CallbackData: fmt.Sprintf("CompleteSelf:%d", schedule.ID)}, {Text: "Выйти из очереди", CallbackData: fmt.Sprintf("LeaveFromQueue:%d", schedule.ID)}},
		[]models.InlineKeyboardButton{{Text: "Отправить очередь повторно", CallbackData: fmt.Sprintf("SendQueueAgain:%d", schedule.ID)}})
	switch snapshot.Status {
	case u.QueueOpen:
		builder.WriteString("\n\n🟢 Очередь открыта 🟢")
	case u.QueuePending:
		builder.WriteString("\n\n🔴 Очередь ещё не открыта 🔴")
	case u.QueueClosed:
		builder.WriteString("\n\n🔴 Очередь закрыта 🔴")
		keyboard = [][]models.InlineKeyboardButton{{{Text: "❌ Очередь закрыта ❌", CallbackData: "CloseQueue"}}}
	}
	return builder.String(), &models.InlineKeyboardMarkup{InlineKeyboard: keyboard}
}

func queueLabel(value string, limit int) string {
	units := 0
	for _, r := range value {
		units++
		if r > 0xffff {
			units++
		}
	}
	if units <= limit {
		return value
	}
	var result strings.Builder
	units = 0
	for _, r := range value {
		size := 1
		if r > 0xffff {
			size = 2
		}
		if units+size > limit-1 {
			break
		}
		result.WriteRune(r)
		units += size
	}
	return result.String() + "…"
}

func (h *BotHandler) SendQueue(ctx context.Context, snapshot queue.Snapshot) (int, error) {
	text, markup := h.renderQueue(snapshot)
	threadID := snapshot.Schedule.ThreadID
	if snapshot.Schedule.ActiveThreadID != nil {
		threadID = *snapshot.Schedule.ActiveThreadID
	}
	message, err := h.b.SendMessage(ctx, &bot.SendMessageParams{ChatID: snapshot.Schedule.ChatID, MessageThreadID: threadID, Text: text, ReplyMarkup: markup})
	if err != nil {
		return 0, queueDeliveryError(err)
	}
	return message.ID, nil
}

func (h *BotHandler) EditQueue(ctx context.Context, snapshot queue.Snapshot, messageID int) error {
	text, markup := h.renderQueue(snapshot)
	_, err := h.b.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID: snapshot.Schedule.ChatID, MessageID: messageID, Text: text, ReplyMarkup: markup})
	if errors.Is(err, bot.ErrorBadRequest) && strings.Contains(err.Error(), "message is not modified") {
		return nil
	}
	if errors.Is(err, bot.ErrorBadRequest) && strings.Contains(err.Error(), "message to edit not found") {
		return queue.ErrMessageMissing
	}
	return queueDeliveryError(err)
}

func (h *BotHandler) RecoverQueue(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	target, err := h.commandTarget(ctx, message.Chat.ID)
	if err != nil {
		h.sendSwapInfoMessage(ctx, b, message.Chat.ID, message.MessageThreadID, "Основной чат не выбран. Выберите его через /edit_schedule")
		return
	}
	if err := h.queues.RecoverTopic(ctx, target, message.MessageThreadID); err != nil {
		h.sendSwapInfoMessage(ctx, b, message.Chat.ID, message.MessageThreadID, queue.ErrorText(err))
		return
	}
	h.sendSwapInfoMessage(ctx, b, message.Chat.ID, message.MessageThreadID, "Очередь обновлена")
}

func (h *BotHandler) RetireQueue(ctx context.Context, chatID int64, messageID int) error {
	_, err := h.b.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{ChatID: chatID, MessageID: messageID, ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "⬇️ Актуальное сообщение ниже ⬇️", CallbackData: "ActualQueue"}}}}})
	if errors.Is(err, bot.ErrorBadRequest) && strings.Contains(err.Error(), "message is not modified") {
		return nil
	}
	if errors.Is(err, bot.ErrorBadRequest) && strings.Contains(err.Error(), "message to edit not found") {
		return queue.ErrMessageMissing
	}
	return queueDeliveryError(err)
}

func (h *BotHandler) QueueMessageDate(ctx context.Context, schedule db.Schedule, messageID int) (time.Time, error) {
	markup := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}}
	message, err := h.b.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{ChatID: schedule.ChatID, MessageID: messageID, ReplyMarkup: markup})
	if errors.Is(err, bot.ErrorBadRequest) && strings.Contains(err.Error(), "message is not modified") {
		markup.InlineKeyboard = [][]models.InlineKeyboardButton{{{Text: "❌ Очередь закрыта ❌", CallbackData: "CloseQueue"}}}
		message, err = h.b.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{ChatID: schedule.ChatID, MessageID: messageID, ReplyMarkup: markup})
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("read original queue message date: %w", queueDeliveryError(err))
	}
	if message == nil || message.Date <= 0 || message.ID != messageID || message.Chat.ID != schedule.ChatID {
		return time.Time{}, queue.ErrLegacyDateUnavailable
	}
	return time.Unix(int64(message.Date), 0), nil
}

func (h *BotHandler) NotifyQueue(ctx context.Context, schedule db.Schedule, text string) error {
	threadID := schedule.ThreadID
	if schedule.ActiveThreadID != nil {
		threadID = *schedule.ActiveThreadID
	}
	_, err := h.b.SendMessage(ctx, &bot.SendMessageParams{ChatID: schedule.ChatID, MessageThreadID: threadID, Text: text})
	return queueDeliveryError(err)
}

func (h *BotHandler) answer(ctx context.Context, b *bot.Bot, query *models.CallbackQuery, text string) {
	if query == nil {
		return
	}
	if _, err := b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: query.ID, Text: text}); err != nil {
		log.Printf("answer callback: %v", err)
	}
}

func (h *BotHandler) queueError(ctx context.Context, b *bot.Bot, query *models.CallbackQuery, err error) {
	if !errors.Is(err, queue.ErrClosed) && !errors.Is(err, queue.ErrStale) {
		log.Printf("queue action: %v", err)
	}
	h.answer(ctx, b, query, queue.ErrorText(err))
}
