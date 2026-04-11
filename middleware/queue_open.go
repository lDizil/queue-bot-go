package middleware

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type QueueStatusProvider interface {
	IsQueueOpen(scheduleID int) bool
}

func QueueOpen(p QueueStatusProvider) func(bot.HandlerFunc) bot.HandlerFunc {
	return func(next bot.HandlerFunc) bot.HandlerFunc {
		return func(ctx context.Context, b *bot.Bot, update *models.Update) {
			query := update.CallbackQuery
			data := query.Data
			parts := strings.Split(data, ":")
			scheduleID, err := strconv.Atoi(parts[1])

			if err != nil {
				next(ctx, b, update)
				return
			}

			if !p.IsQueueOpen(scheduleID) {
				b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
					Text:            "Очередь закрыта. Пока вы не можете выполнять действия.",
					CallbackQueryID: query.ID,
				})
				return
			}
			next(ctx, b, update)
		}
	}
}
