package middleware

import (
	"context"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type EditSessionProvider interface {
	GetEditMessage(userID int64) (int, bool)
}

func EditSession(p EditSessionProvider) func(bot.HandlerFunc) bot.HandlerFunc {
	return func(next bot.HandlerFunc) bot.HandlerFunc {
		return func(ctx context.Context, b *bot.Bot, update *models.Update) {
			userID := update.CallbackQuery.From.ID
			query := update.CallbackQuery
			replyEditMesID := query.Message.Message.ID

			editMesID, exists := p.GetEditMessage(userID)
			if !exists {
				b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
					Text:            "Вы не находитесь в режиме редактирования",
					CallbackQueryID: query.ID,
				})

				return 
			}

			if editMesID != replyEditMesID {
				b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
					Text:            "Это сообщение редактирования очереди для другого пользователя. Если хотите также изменить расписание, вызовите /edit_schedule",
					CallbackQueryID: query.ID,
				})

				return 
			}

			next(ctx, b, update)
		}
	}
}
