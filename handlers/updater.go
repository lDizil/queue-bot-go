package handlers

import (
	"context"
	"log"
	u "queuebot/utils"
	"time"

	"github.com/go-telegram/bot"
)

type updateTask struct {
	ctx        context.Context
	b          *bot.Bot
	chatID     int64
	scheduleID int
}

func (h *BotHandler) updateWorker(delay time.Duration, timeForExpiredEditSes time.Duration) {
	ticker := time.NewTicker(delay)
	pending := map[int]*updateTask{}
	sessionTicker := time.NewTicker(timeForExpiredEditSes / 3)

	for {
		select {
		case task := <-h.updateQueue:
			pending[task.scheduleID] = &task
		case <-ticker.C:
			for scheduleID, task := range pending {
				ok, err := h.updateQueueMessage(task.ctx, task.b, task.chatID, scheduleID, u.QueueOpen)
				if err != nil {
					log.Println("Ошибка обновления сообщения очереди:", err)
				} else if !ok {
					log.Println("Сообщение очереди не найдено для scheduleID:", task.scheduleID)
				}
				delete(pending, scheduleID)
			}
		case <-sessionTicker.C:
			h.isExpiredUserEditSes(timeForExpiredEditSes)
		}

	}
}
