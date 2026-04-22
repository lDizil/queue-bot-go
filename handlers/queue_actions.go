package handlers

import (
	"context"
	"fmt"
	"log"
	"queuebot/db"
	"sort"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5"
)

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

	takenBefore, freeBefore, snapshotErr := h.getQueueSlotSnapshot(ctx, scheduleID)
	if snapshotErr != nil {
		log.Printf("[queue] не удалось получить состояние очереди: очередь=%d, кнопка=место_%d, пользователь=%s. %v", scheduleID, slot, queueUserTag(username, userID), snapshotErr)
		takenBefore, freeBefore = "?", "?"
	}

	log.Printf("[queue] клик: кнопка=место_%d, очередь=%d, пользователь=%s, msg=%d, callback=%s, заняты=[%s], свободны=[%s]", slot, scheduleID, queueUserTag(username, userID), query.Message.Message.ID, data, takenBefore, freeBefore)

	isInQueue, err := h.checkIsUserInQueue(ctx, b, userID, scheduleID, query, username)

	if err != nil {
		return
	}

	if isInQueue {
		log.Printf("[queue] отказ: кнопка=место_%d, очередь=%d, пользователь=%s, причина=уже_в_очереди, заняты=[%s], свободны=[%s]", slot, scheduleID, queueUserTag(username, userID), takenBefore, freeBefore)
		return
	}

	taken, err := h.db.IsPositionTaken(ctx, slot, scheduleID)
	if err != nil {
		msg := fmt.Sprintf("Ошибка проверки занята ли позиция (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	if taken {
		log.Printf("[queue] отказ: кнопка=место_%d, очередь=%d, пользователь=%s, причина=место_занято, заняты=[%s], свободны=[%s]", slot, scheduleID, queueUserTag(username, userID), takenBefore, freeBefore)
		b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			Text:            "Место уже занято, выберите другое",
			CallbackQueryID: query.ID,
			ShowAlert:       true,
		})
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

	log.Printf("[queue] успех: кнопка=место_%d, очередь=%d, пользователь=%s, занял=%d, заняты=[%s], свободны=[%s]", slot, scheduleID, queueUserTag(username, userID), slot, takenBefore, freeBefore)

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
		ShowAlert:       true,
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

	takenBefore, freeBefore, snapshotErr := h.getQueueSlotSnapshot(ctx, scheduleID)
	if snapshotErr != nil {
		log.Printf("[queue] не удалось получить состояние очереди: очередь=%d, кнопка=ближайшее, пользователь=%s. %v", scheduleID, queueUserTag(username, userID), snapshotErr)
		takenBefore, freeBefore = "?", "?"
	}

	log.Printf("[queue] клик: кнопка=ближайшее, очередь=%d, пользователь=%s, msg=%d, callback=%s, заняты=[%s], свободны=[%s]", scheduleID, queueUserTag(username, userID), query.Message.Message.ID, data, takenBefore, freeBefore)

	slot, err := h.db.JoinFirstFreeSlot(ctx, userID, username, scheduleID, h.totalSlotsInQueue)
	if err != nil {
		if err == pgx.ErrNoRows {
			isInQueue, checkErr := h.db.IsUserInQueue(ctx, userID, scheduleID)
			if checkErr == nil && isInQueue {
				log.Printf("[queue] отказ: кнопка=ближайшее, очередь=%d, пользователь=%s, причина=уже_в_очереди, заняты=[%s], свободны=[%s]", scheduleID, queueUserTag(username, userID), takenBefore, freeBefore)
				b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
					Text:            "Вы уже заняли место в очереди",
					CallbackQueryID: query.ID,
					ShowAlert:       true,
				})
			} else {
				log.Printf("[queue] отказ: кнопка=ближайшее, очередь=%d, пользователь=%s, причина=свободных_мест_нет, заняты=[%s], свободны=[%s]", scheduleID, queueUserTag(username, userID), takenBefore, freeBefore)
				b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
					Text:            "Все места в очереди заняты",
					CallbackQueryID: query.ID,
					ShowAlert:       true,
				})
			}
			return
		} else {
			log.Printf("[queue] ошибка: кнопка=ближайшее, очередь=%d, пользователь=%s, заняты=[%s], свободны=[%s]. %v", scheduleID, queueUserTag(username, userID), takenBefore, freeBefore, err)
			msg := fmt.Sprintf("Ошибка при попытке занять ближайшее место в очереди (пользователь %s). %v", username, err)
			h.handleError(ctx, b, query.ID, msg)
			return
		}
	}

	log.Printf("[queue] успех: кнопка=ближайшее, очередь=%d, пользователь=%s, занял=%d, заняты=[%s], свободны=[%s]", scheduleID, queueUserTag(username, userID), slot, takenBefore, freeBefore)

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
		ShowAlert:       true,
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

	takenBefore, freeBefore, snapshotErr := h.getQueueSlotSnapshot(ctx, scheduleID)
	if snapshotErr != nil {
		log.Printf("[queue] не удалось получить состояние очереди: очередь=%d, кнопка=занятое_место_%d, пользователь=%s. %v", scheduleID, slot, queueUserTag(username, userID), snapshotErr)
		takenBefore, freeBefore = "?", "?"
	}

	log.Printf("[queue] клик: кнопка=занятое_место_%d, очередь=%d, пользователь=%s, msg=%d, callback=%s, заняты=[%s], свободны=[%s]", slot, scheduleID, queueUserTag(username, userID), query.Message.Message.ID, data, takenBefore, freeBefore)

	isUserInQueue, err := h.db.IsUserInQueue(ctx, userID, scheduleID)
	if err != nil {
		msg := fmt.Sprintf("Ошибка проверки стоит ли пользователь в очереди (пользователь %s). %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	if isUserInQueue {
		log.Printf("[queue] отказ: кнопка=занятое_место_%d, очередь=%d, пользователь=%s, причина=уже_в_очереди, заняты=[%s], свободны=[%s]", slot, scheduleID, queueUserTag(username, userID), takenBefore, freeBefore)
		b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			Text:            "Вы уже заняли место в очереди",
			CallbackQueryID: query.ID,
			ShowAlert:       true,
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
		log.Printf("[queue] отказ: кнопка=занятое_место_%d, очередь=%d, пользователь=%s, причина=позиция_уже_свободна, заняты=[%s], свободны=[%s]", slot, scheduleID, queueUserTag(username, userID), takenBefore, freeBefore)
		b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			Text:            "Позиция не занята, но считается закрытой (крестик на кнопке), повторите попытку",
			CallbackQueryID: query.ID,
			ShowAlert:       true,
		})
		return
	}

	log.Printf("[queue] отказ: кнопка=занятое_место_%d, очередь=%d, пользователь=%s, причина=место_занято, заняты=[%s], свободны=[%s]", slot, scheduleID, queueUserTag(username, userID), takenBefore, freeBefore)

	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		Text:            "Данное место уже занято, выберите другое",
		CallbackQueryID: query.ID,
		ShowAlert:       true,
	})

}

func queueUserTag(username string, userID int64) string {
	if username == "" {
		return fmt.Sprintf("id:%d", userID)
	}

	return fmt.Sprintf("@%s(%d)", username, userID)
}

func (h *BotHandler) getQueueSlotSnapshot(ctx context.Context, scheduleID int) (string, string, error) {
	queue, err := h.db.GetQueue(ctx, scheduleID)
	if err != nil {
		return "", "", err
	}

	occupied := make(map[int]struct{}, len(queue))
	for _, entry := range queue {
		occupied[entry.Position] = struct{}{}
	}

	takenSlots := make([]int, 0, len(queue))
	freeSlots := make([]int, 0, h.totalSlotsInQueue)

	for position := 1; position <= h.totalSlotsInQueue; position++ {
		if _, exists := occupied[position]; exists {
			takenSlots = append(takenSlots, position)
		} else {
			freeSlots = append(freeSlots, position)
		}
	}

	return formatPositionRanges(takenSlots), formatPositionRanges(freeSlots), nil
}

func formatPositionRanges(positions []int) string {
	if len(positions) == 0 {
		return "-"
	}

	sort.Ints(positions)

	ranges := make([]string, 0, len(positions))
	start := positions[0]
	prev := positions[0]

	for i := 1; i < len(positions); i++ {
		cur := positions[i]

		if cur == prev+1 {
			prev = cur
			continue
		}

		ranges = append(ranges, formatSingleRange(start, prev))
		start = cur
		prev = cur
	}

	ranges = append(ranges, formatSingleRange(start, prev))

	return strings.Join(ranges, ",")
}

func formatSingleRange(start int, end int) string {
	if start == end {
		return strconv.Itoa(start)
	}

	return fmt.Sprintf("%d-%d", start, end)
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
			ShowAlert:       true,
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
		ShowAlert:       true,
	})
}
