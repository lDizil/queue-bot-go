package handlers

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"queuebot/queue"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (h *BotHandler) queueReference(ctx context.Context, b *bot.Bot, update *models.Update) (queue.Reference, bool) {
	if update == nil || update.CallbackQuery == nil || update.CallbackQuery.Message.Message == nil {
		return queue.Reference{}, false
	}
	query := update.CallbackQuery
	parts := strings.Split(query.Data, ":")
	if len(parts) < 2 {
		h.answer(ctx, b, query, "Некорректная кнопка очереди")
		return queue.Reference{}, false
	}
	id, err := strconv.Atoi(parts[1])
	if err != nil || id <= 0 {
		h.answer(ctx, b, query, "Некорректная кнопка очереди")
		return queue.Reference{}, false
	}
	return queue.Reference{ScheduleID: id, ChatID: query.Message.Message.Chat.ID, MessageID: query.Message.Message.ID}, true
}

func (h *BotHandler) actor(user models.User) queue.Actor {
	name := strings.Join(strings.Fields(user.FirstName+" "+user.LastName), " ")
	return queue.Actor{ID: user.ID, Username: user.Username, DisplayName: name, Admin: h.adminIDs[user.ID]}
}

func (h *BotHandler) JoinToPosition(ctx context.Context, b *bot.Bot, update *models.Update) {
	ref, ok := h.queueReference(ctx, b, update)
	if !ok {
		return
	}
	query := update.CallbackQuery
	parts := strings.Split(query.Data, ":")
	if len(parts) != 3 {
		h.answer(ctx, b, query, "Некорректная кнопка места")
		return
	}
	position, err := strconv.Atoi(parts[2])
	if err != nil || position <= 0 {
		h.answer(ctx, b, query, "Некорректная кнопка места")
		return
	}
	position, err = h.queues.Join(ctx, ref, h.actor(query.From), position)
	if err != nil {
		h.queueError(ctx, b, query, err)
		return
	}
	h.answer(ctx, b, query, fmt.Sprintf("Вы заняли место %d. Удачной сдачи!", position))
}

func (h *BotHandler) JoinClosestFreeSlot(ctx context.Context, b *bot.Bot, update *models.Update) {
	ref, ok := h.queueReference(ctx, b, update)
	if !ok {
		return
	}
	position, err := h.queues.Join(ctx, ref, h.actor(update.CallbackQuery.From), 0)
	if err != nil {
		h.queueError(ctx, b, update.CallbackQuery, err)
		return
	}
	h.answer(ctx, b, update.CallbackQuery, fmt.Sprintf("Вы заняли место %d. Удачной сдачи!", position))
}

func (h *BotHandler) JoinBusySlot(ctx context.Context, b *bot.Bot, update *models.Update) {
	h.answer(ctx, b, update.CallbackQuery, "Место занято. Используйте актуальное сообщение очереди")
}

func (h *BotHandler) LeaveQueue(ctx context.Context, b *bot.Bot, update *models.Update) {
	ref, ok := h.queueReference(ctx, b, update)
	if !ok {
		return
	}
	if err := h.queues.Leave(ctx, ref, h.actor(update.CallbackQuery.From)); err != nil {
		h.queueError(ctx, b, update.CallbackQuery, err)
		return
	}
	h.answer(ctx, b, update.CallbackQuery, "Вы покинули очередь. Удачного дня!")
}

func (h *BotHandler) CompleteQueue(ctx context.Context, b *bot.Bot, update *models.Update) {
	ref, ok := h.queueReference(ctx, b, update)
	if !ok {
		return
	}
	query := update.CallbackQuery
	parts := strings.Split(query.Data, ":")
	var entryID, position int
	var target *bool
	if parts[0] == "CompleteSlot" {
		if len(parts) != 5 {
			h.answer(ctx, b, query, "Некорректная кнопка сдачи")
			return
		}
		id, errID := strconv.Atoi(parts[2])
		pos, errPos := strconv.Atoi(parts[3])
		value, errValue := strconv.ParseBool(parts[4])
		if errID != nil || errPos != nil || errValue != nil || id <= 0 || pos <= 0 {
			h.answer(ctx, b, query, "Некорректная кнопка сдачи")
			return
		}
		entryID, position, target = id, pos, &value
	} else if len(parts) != 2 {
		h.answer(ctx, b, query, "Некорректная кнопка сдачи")
		return
	}
	completed, err := h.queues.Complete(ctx, ref, h.actor(query.From), entryID, position, target, query.ID)
	if err != nil {
		h.queueError(ctx, b, query, err)
		return
	}
	text := "Отметка о сдаче снята"
	if completed {
		text = "Сдача отмечена ✅"
	}
	h.answer(ctx, b, query, text)
}

func queueUserTag(username string, userID int64) string {
	if username == "" {
		return fmt.Sprintf("id:%d", userID)
	}
	return fmt.Sprintf("@%s(%d)", username, userID)
}

func formatPositionRanges(positions []int) string {
	if len(positions) == 0 {
		return "-"
	}
	sort.Ints(positions)
	var result []string
	start, previous := positions[0], positions[0]
	for _, position := range positions[1:] {
		if position == previous+1 {
			previous = position
			continue
		}
		result = append(result, formatSingleRange(start, previous))
		start, previous = position, position
	}
	return strings.Join(append(result, formatSingleRange(start, previous)), ",")
}

func formatSingleRange(start, end int) string {
	if start == end {
		return strconv.Itoa(start)
	}
	return fmt.Sprintf("%d-%d", start, end)
}
