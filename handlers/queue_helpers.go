package handlers

import (
	"context"
	"log"
	"time"

	"queuebot/db"
)

func (h *BotHandler) isQueueOpen(schedule db.Schedule) bool {
	now := time.Now()
	return schedule.ActiveOpensAt != nil && schedule.ActiveClosesAt != nil && !now.Before(*schedule.ActiveOpensAt) && now.Before(*schedule.ActiveClosesAt) && schedule.QueueMesID != nil
}

func (h *BotHandler) IsQueueOpen(scheduleID int) bool {
	schedule, err := h.db.GetScheduleEntry(context.Background(), scheduleID)
	return err == nil && h.isQueueOpen(schedule)
}

func (h *BotHandler) reschedule(ctx context.Context, scheduleID int) error {
	if h.scheduler == nil {
		log.Print("scheduler is not configured")
		return nil
	}
	sch, err := h.db.GetScheduleEntry(ctx, scheduleID)
	if err != nil {
		return err
	}
	return h.rescheduleWithSch(ctx, scheduleID, sch)
}

func (h *BotHandler) rescheduleWithSch(ctx context.Context, scheduleID int, sch db.Schedule) error {
	if h.scheduler == nil {
		log.Print("scheduler is not configured")
		return nil
	}
	if sch.ActiveOpensAt != nil {
		return nil
	}
	h.scheduler.RemoveSchedule(scheduleID)
	h.scheduler.ScheduleNext(sch)
	return nil
}
