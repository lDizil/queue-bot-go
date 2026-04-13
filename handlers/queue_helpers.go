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

func (h *BotHandler) isQueueOpen(schedule db.Schedule) bool {
	moscow, _ := time.LoadLocation("Europe/Moscow")
	now := time.Now().In(moscow)

	start := time.Date(now.Year(), now.Month(), now.Day(),
		schedule.StartTime.Hour(), schedule.StartTime.Minute(), 0, 0, moscow)
	end := time.Date(now.Year(), now.Month(), now.Day(),
		schedule.EndTime.Hour(), schedule.EndTime.Minute(), 0, 0, moscow)

	if end.Before(start) {
		end = end.AddDate(0, 0, 1)
	}

	return now.After(start) && now.Before(end)
}

func (h *BotHandler) IsQueueOpen(scheduleID int) bool {
	schedule, err := h.db.GetScheduleEntry(context.Background(), scheduleID)
	if err != nil {
		return false
	}
	return h.isQueueOpen(schedule)
}

func (h *BotHandler) reschedule(ctx context.Context, scheduleID int) error {
	if h.scheduler == nil {
		log.Println("Ошибка перепланирования, scheduler не был задан и передан в структуру")
		return nil
	}

	err := h.db.ResetNotifications(ctx, scheduleID)
	if err != nil {
		return err
	}

	sch, err := h.db.GetScheduleEntry(ctx, scheduleID)
	if err != nil {
		return err
	}

	h.scheduler.RemoveSchedule(scheduleID)
	h.scheduler.ScheduleNext(sch)

	return nil
}

func (h *BotHandler) rescheduleWithSch(ctx context.Context, scheduleID int, sch db.Schedule) error {
	if h.scheduler == nil {
		log.Println("Ошибка перепланирования, scheduler не был задан и передан в структуру")
		return nil
	}

	err := h.db.ResetNotifications(ctx, scheduleID)
	if err != nil {
		return err
	}

	sch.Notified5min = false
	sch.Notified1min = false
	sch.NotifiedOpen = false

	h.scheduler.RemoveSchedule(scheduleID)
	h.scheduler.ScheduleNext(sch)

	return nil
}
