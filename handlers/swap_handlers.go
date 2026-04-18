package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"queuebot/db"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/jackc/pgx/v5"
)

const (
	swapRequestTTL      = 2 * time.Minute
	swapResultDeleteTTL = 5 * time.Second
)

func (h *BotHandler) SwapRequest(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update == nil || update.Message == nil {
		return
	}

	message := update.Message
	chatID := message.Chat.ID
	threadID := message.MessageThreadID
	requesterUserID := message.From.ID
	requesterUsername := message.From.Username

	scheduleID, targetPosition, err := h.resolveSwapCommand(ctx, message.Text, threadID)
	if err != nil {
		h.sendSwapInfoMessage(ctx, b, chatID, threadID, err.Error())
		return
	}

	if !h.IsQueueOpen(scheduleID) {
		h.sendSwapInfoMessage(ctx, b, chatID, threadID, "Очередь сейчас закрыта, обмен местами недоступен")
		return
	}

	requesterEntry, err := h.db.GetQueueEntryByUser(ctx, requesterUserID, scheduleID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.sendSwapInfoMessage(ctx, b, chatID, threadID, "Вы не стоите в этой очереди, обмен невозможен")
			return
		}

		log.Printf("[swap] ошибка поиска места инициатора (очередь=%d, пользователь=%s). %v", scheduleID, queueUserTag(requesterUsername, requesterUserID), err)
		h.sendSwapInfoMessage(ctx, b, chatID, threadID, "Не удалось проверить место инициатора, попробуйте позже")
		return
	}

	targetEntry, err := h.db.GetQueueEntryByPosition(ctx, targetPosition, scheduleID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.sendSwapInfoMessage(ctx, b, chatID, threadID, "Указанное место сейчас свободно, обмен возможен только с занятым местом")
			return
		}

		log.Printf("[swap] ошибка поиска участника по позиции (очередь=%d, позиция=%d, пользователь=%s). %v", scheduleID, targetPosition, queueUserTag(requesterUsername, requesterUserID), err)
		h.sendSwapInfoMessage(ctx, b, chatID, threadID, "Не удалось проверить целевое место, попробуйте позже")
		return
	}

	if targetEntry.UserID == requesterUserID {
		h.sendSwapInfoMessage(ctx, b, chatID, threadID, "Это ваше текущее место, обмен не требуется")
		return
	}

	schedule, err := h.db.GetScheduleEntry(ctx, scheduleID)
	if err != nil {
		log.Printf("[swap] ошибка получения записи расписания (очередь=%d, пользователь=%s). %v", scheduleID, queueUserTag(requesterUsername, requesterUserID), err)
		h.sendSwapInfoMessage(ctx, b, chatID, threadID, "Не удалось найти тему очереди, попробуйте позже")
		return
	}

	requestID := h.nextSwapRequestID()
	text := fmt.Sprintf(
		"Запрос на обмен местами\n\nПользователь %s (место %d) хочет обменяться с %s (место %d).\n\n%s, подтвердите обмен.",
		swapUserLabel(requesterEntry.Username),
		requesterEntry.Position,
		swapUserLabel(targetEntry.Username),
		targetEntry.Position,
		swapUserLabel(targetEntry.Username),
	)

	markup := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{
			{Text: "Да", CallbackData: fmt.Sprintf("SwapConfirm:%s", requestID)},
			{Text: "Нет", CallbackData: fmt.Sprintf("SwapReject:%s", requestID)},
		},
	}}

	sent, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          chatID,
		MessageThreadID: schedule.ThreadID,
		Text:            text,
		ReplyMarkup:     markup,
	})
	if err != nil {
		log.Printf("[swap] ошибка отправки запроса на подтверждение (очередь=%d, пользователь=%s). %v", scheduleID, queueUserTag(requesterUsername, requesterUserID), err)
		h.sendSwapInfoMessage(ctx, b, chatID, threadID, "Не удалось отправить запрос на подтверждение")
		return
	}

	h.swapMu.Lock()
	h.pendingSwaps[requestID] = swapRequest{
		ID:                requestID,
		ScheduleID:        scheduleID,
		ChatID:            chatID,
		ThreadID:          schedule.ThreadID,
		MessageID:         sent.ID,
		RequesterUserID:   requesterEntry.UserID,
		RequesterUsername: requesterEntry.Username,
		RequesterPosition: requesterEntry.Position,
		TargetUserID:      targetEntry.UserID,
		TargetUsername:    targetEntry.Username,
		TargetPosition:    targetEntry.Position,
		ExpiresAt:         time.Now().Add(swapRequestTTL),
	}
	h.swapMu.Unlock()

	go h.expireSwapRequest(requestID)
}

func (h *BotHandler) SwapConfirm(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update == nil || update.CallbackQuery == nil {
		return
	}

	query := update.CallbackQuery
	requestID, err := h.parseSwapRequestID(query.Data)
	if err != nil {
		b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: query.ID,
			Text:            "Некорректный запрос на обмен",
			ShowAlert:       true,
		})
		return
	}

	req, ok, unauthorized := h.claimSwapRequest(requestID, query.From.ID)
	if unauthorized != "" {
		b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: query.ID,
			Text:            unauthorized,
			ShowAlert:       true,
		})
		return
	}

	if !ok {
		b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: query.ID,
			Text:            "Запрос уже обработан или истек",
			ShowAlert:       true,
		})
		return
	}

	resultText := "Обмен не выполнен"
	callbackText := "Обмен отклонен"

	if time.Now().After(req.ExpiresAt) {
		resultText = "Время подтверждения истекло. Обмен не выполнен"
		callbackText = "Время подтверждения истекло"
	} else if !h.IsQueueOpen(req.ScheduleID) {
		resultText = "Очередь уже закрыта, обмен не выполнен"
		callbackText = "Очередь закрыта"
	} else {
		err = h.db.SwapQueuePositions(ctx, req.ScheduleID, req.RequesterUserID, req.TargetUserID, req.RequesterPosition, req.TargetPosition)
		switch {
		case err == nil:
			resultText = fmt.Sprintf(
				"Обмен выполнен успешно: %s теперь на месте %d, %s теперь на месте %d",
				swapUserLabel(req.RequesterUsername), req.TargetPosition,
				swapUserLabel(req.TargetUsername), req.RequesterPosition,
			)
			callbackText = "Обмен подтвержден"

			task := &updateTask{
				ctx:        ctx,
				b:          b,
				chatID:     req.ChatID,
				scheduleID: req.ScheduleID,
			}
			h.updateQueue <- *task
		case errors.Is(err, db.ErrSwapStateChanged):
			resultText = "Кто-то изменил очередь раньше подтверждения. Обмен не выполнен, отправьте новый запрос"
			callbackText = "Очередь уже изменилась"
		default:
			log.Printf("[swap] ошибка подтверждения обмена (request=%s, очередь=%d). %v", req.ID, req.ScheduleID, err)
			resultText = "Ошибка во время обмена. Попробуйте еще раз"
			callbackText = "Ошибка обмена"
		}
	}

	h.finishSwapRequest(ctx, b, req, resultText)

	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: query.ID,
		Text:            callbackText,
		ShowAlert:       false,
	})
}

func (h *BotHandler) SwapReject(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update == nil || update.CallbackQuery == nil {
		return
	}

	query := update.CallbackQuery
	requestID, err := h.parseSwapRequestID(query.Data)
	if err != nil {
		b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: query.ID,
			Text:            "Некорректный запрос на обмен",
			ShowAlert:       true,
		})
		return
	}

	req, ok, unauthorized := h.claimSwapRequest(requestID, query.From.ID)
	if unauthorized != "" {
		b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: query.ID,
			Text:            unauthorized,
			ShowAlert:       true,
		})
		return
	}

	if !ok {
		b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: query.ID,
			Text:            "Запрос уже обработан или истек",
			ShowAlert:       true,
		})
		return
	}

	resultText := fmt.Sprintf("%s отказался от обмена местами", swapUserLabel(req.TargetUsername))
	h.finishSwapRequest(ctx, b, req, resultText)

	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: query.ID,
		Text:            "Обмен отменен",
		ShowAlert:       false,
	})
}

func (h *BotHandler) resolveSwapCommand(ctx context.Context, rawText string, threadID int) (int, int, error) {
	parts := strings.Fields(strings.TrimSpace(rawText))
	if len(parts) != 2 {
		return 0, 0, errors.New("Формат: /swap <место>")
	}

	parsePositiveInt := func(raw string) (int, error) {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return 0, errors.New("ожидалось положительное число")
		}
		return value, nil
	}

	targetPosition, err := parsePositiveInt(parts[1])
	if err != nil {
		return 0, 0, errors.New("Неверный формат места. Пример: /swap 5")
	}

	if targetPosition > h.totalSlotsInQueue {
		return 0, 0, fmt.Errorf("Место %d вне диапазона очереди", targetPosition)
	}

	if threadID == 0 {
		return 0, 0, errors.New("Команду /swap нужно писать в теме с очередью")
	}

	scheduleID, err := h.db.GetActiveScheduleIDByThread(ctx, threadID)
	if err != nil {
		switch {
		case errors.Is(err, db.ErrActiveQueueNotFound):
			return 0, 0, errors.New("В этой теме сейчас нет активной очереди")
		default:
			log.Printf("[swap] ошибка поиска активной очереди по теме %d. %v", threadID, err)
			return 0, 0, errors.New("Не удалось определить очередь по теме, попробуйте позже")
		}
	}

	return scheduleID, targetPosition, nil
}

func (h *BotHandler) parseSwapRequestID(data string) (string, error) {
	parts := strings.Split(data, ":")
	if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
		return "", errors.New("invalid swap request id")
	}

	return parts[1], nil
}

func (h *BotHandler) claimSwapRequest(requestID string, actorUserID int64) (swapRequest, bool, string) {
	h.swapMu.Lock()
	defer h.swapMu.Unlock()

	req, ok := h.pendingSwaps[requestID]
	if !ok {
		return swapRequest{}, false, ""
	}

	if req.TargetUserID != actorUserID {
		return swapRequest{}, false, "Подтвердить или отклонить обмен может только пользователь, к которому обращен запрос"
	}

	delete(h.pendingSwaps, requestID)
	return req, true, ""
}

func (h *BotHandler) nextSwapRequestID() string {
	seq := atomic.AddUint64(&h.swapSeq, 1)
	return strconv.FormatUint(seq, 10)
}

func (h *BotHandler) expireSwapRequest(requestID string) {
	timer := time.NewTimer(swapRequestTTL)
	defer timer.Stop()
	<-timer.C

	h.swapMu.Lock()
	req, ok := h.pendingSwaps[requestID]
	if ok {
		delete(h.pendingSwaps, requestID)
	}
	h.swapMu.Unlock()

	if !ok {
		return
	}

	h.finishSwapRequest(context.Background(), h.b, req, "Время подтверждения истекло. Обмен не выполнен")
}

func (h *BotHandler) finishSwapRequest(ctx context.Context, b *bot.Bot, req swapRequest, resultText string) {
	if b == nil {
		return
	}

	_, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:      req.ChatID,
		MessageID:   req.MessageID,
		Text:        resultText,
		ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}},
	})
	if err != nil {
		log.Printf("[swap] ошибка изменения результата запроса (request=%s). %v", req.ID, err)
	}

	go func(chatID int64, messageID int) {
		timer := time.NewTimer(swapResultDeleteTTL)
		defer timer.Stop()
		<-timer.C

		_, err := b.DeleteMessage(context.Background(), &bot.DeleteMessageParams{
			ChatID:    chatID,
			MessageID: messageID,
		})
		if err != nil {
			log.Printf("[swap] ошибка удаления результата запроса (message=%d). %v", messageID, err)
		}
	}(req.ChatID, req.MessageID)
}

func (h *BotHandler) sendSwapInfoMessage(ctx context.Context, b *bot.Bot, chatID int64, threadID int, text string) {
	if b == nil {
		return
	}

	_, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          chatID,
		MessageThreadID: threadID,
		Text:            text,
	})
	if err != nil {
		log.Printf("[swap] ошибка отправки информационного сообщения: %v", err)
	}
}

func swapUserLabel(username string) string {
	username = strings.TrimSpace(username)
	if username == "" {
		return "пользователь"
	}

	return "@" + username
}
