package middleware

import (
	"context"
	"fmt"
	"log"
	"slices"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func getUser(update *models.Update) (int64, bool) {
    if update.CallbackQuery != nil {
        return update.CallbackQuery.From.ID, true
    }
    if update.Message != nil {
        return update.Message.From.ID, true
    }
    return 0, false
}

func AdminOnly(adminIDs []int64) func(bot.HandlerFunc) bot.HandlerFunc {
	return func(next bot.HandlerFunc) bot.HandlerFunc {
		return func(ctx context.Context, b *bot.Bot, update *models.Update) {
			userID, ok := getUser(update)
			if !ok {
				return
			}

			if slices.Contains(adminIDs, userID) {
					next(ctx, b, update)
					return
				}
			
			if update.CallbackQuery != nil {
				b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
					CallbackQueryID: update.CallbackQuery.ID,
					Text: "У Вас недостаточно прав для выполнения действия",
				})
			} else {
				username := update.Message.From.Username
				_, err := b.SendMessage(ctx, &bot.SendMessageParams{
					ChatID: update.Message.Chat.ID,
					MessageThreadID: update.Message.MessageThreadID,
					Text:            fmt.Sprintf("У Вас (пользователь %s) недостаточно прав для выполнения действия", username),
				})

				if err != nil {
					log.Println("Ошибка отправки сообщения о недостаточности прав")
					return 
				}
			}
		}
	}
}
