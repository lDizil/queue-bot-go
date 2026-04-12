package handlers

import (
	"context"
	"log"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (h *BotHandler) StartHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	descripton := "Привет! Я новая версия бота QueueBot, написанная на Go. Предоставляю удобный интерфейс очереди, когда вам нужно организовать какую-то последовательность людей, например, для сдачи лабораторных работ"
	_, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: descripton, MessageThreadID: update.Message.MessageThreadID})

	if err != nil {
		log.Println("Ошибка при отправке сообщение в ответ на /start в чат")
	}
}
