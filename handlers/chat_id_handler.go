package handlers

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (h *BotHandler) chatIDHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update == nil || update.Message == nil {
		return
	}
	message := update.Message
	text := fmt.Sprintf("ID чата: <code>%d</code>", message.Chat.ID)
	if message.MessageThreadID > 0 {
		text += fmt.Sprintf("\nID темы: <code>%d</code>", message.MessageThreadID)
	}
	reply, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: message.Chat.ID, MessageThreadID: message.MessageThreadID,
		Text: text, ParseMode: models.ParseModeHTML,
	})
	if err != nil {
		log.Print("Не удалось отправить ID чата")
		return
	}
	go h.deleteChatIDMessages(b, message.Chat.ID, reply.ID, message.ID, 15*time.Second)
}

func (h *BotHandler) deleteChatIDMessages(b *bot.Bot, chatID int64, replyID, commandID int, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-h.ctx.Done():
		return
	case <-timer.C:
	}
	for _, messageID := range []int{replyID, commandID} {
		if h.ctx.Err() != nil {
			return
		}
		ctx, cancel := context.WithTimeout(h.ctx, 3*time.Second)
		_, err := b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: chatID, MessageID: messageID})
		cancel()
		if err != nil {
			log.Print("Не удалось удалить сообщение с ID чата")
		}
	}
}
