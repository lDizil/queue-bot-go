package middleware

import (
	"context"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type EditSessionProvider interface {
	ValidateEditSession(userID, chatID int64, threadID, messageID int) bool
}

func EditSession(p EditSessionProvider) func(bot.HandlerFunc) bot.HandlerFunc {
	return func(next bot.HandlerFunc) bot.HandlerFunc {
		return func(ctx context.Context, b *bot.Bot, update *models.Update) {
			if update == nil || update.CallbackQuery == nil || update.CallbackQuery.Message.Message == nil {
				return
			}
			userID := update.CallbackQuery.From.ID
			query := update.CallbackQuery
			replyEditMesID := query.Message.Message.ID

			if !p.ValidateEditSession(userID, query.Message.Message.Chat.ID, query.Message.Message.MessageThreadID, replyEditMesID) {
				b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
					Text:            "Вы не находитесь в режиме редактирования",
					CallbackQueryID: query.ID,
				})

				return
			}

			next(ctx, b, update)
		}
	}
}
