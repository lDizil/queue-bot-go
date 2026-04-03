package handlers

import (
	"context"
	"fmt"
	"log"
	"queuebot/db"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (h *BotHandler) EditScheduleReplyText(ctx context.Context, b *bot.Bot, update *models.Update) {
    msg := update.Message
    username := msg.From.Username
    chatID := msg.Chat.ID

	schedules, err := h.db.GetAllSchedules(ctx)
	if err != nil {
		log.Printf("Ошибка получения очереди (пользователь %s). %v", username, err)
		return
	}

	text, markup := h.GenerateEditMessage(schedules)

	msg, err = b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ReplyMarkup: markup,
	})

	if err != nil {
		log.Printf("Ошибка при попытке отправить сообщения для редактирования расписания (пользователь %s). %v", username, err)
		return
	}

	log.Printf("Сообщение для редактирования очереди (id: %d) отправлено в чат", msg.ID)
}

func (h *BotHandler) GenerateEditMessage(schedules []db.Schedule) (string, *models.InlineKeyboardMarkup) {
	text := "Вы вошли в режим редактирования.\n Можете изменить существующие записи или добавить новые\n"

	var builder strings.Builder

	builder.Grow(120 + len(schedules)*30)
	builder.WriteString(text)

	keyboard := [][]models.InlineKeyboardButton{}
	if len(schedules) == 0 {
		builder.WriteString("\nНет записей для редактирования")
	} else {
		for _, sch := range schedules {
			var btn models.InlineKeyboardButton
			if sch.ThreadDescription == nil {
				btn = models.InlineKeyboardButton{
					Text:         fmt.Sprintf("%s | %s | %s - %s | %d", dayEnToRu[strings.ToLower(sch.DayOfWeek)], weekTypeEnToRu[strings.ToLower(sch.WeekType)], sch.StartTime.Format("15:04:05"), sch.EndTime.Format("15:04:05"), sch.ThreadId),
					CallbackData: fmt.Sprintf("EditSchedule:%d", sch.ID),
				}
			} else {
				btn = models.InlineKeyboardButton{
					Text:         fmt.Sprintf("%s | %s | %s - %s | %d | %s", dayEnToRu[strings.ToLower(sch.DayOfWeek)], weekTypeEnToRu[strings.ToLower(sch.WeekType)], sch.StartTime.Format("15:04:05"), sch.EndTime.Format("15:04:05"), sch.ThreadId, *sch.ThreadDescription),
					CallbackData: fmt.Sprintf("EditSchedule:%d", sch.ID),
				}
			}

			keyboard = append(keyboard, []models.InlineKeyboardButton{btn})
		}
	}

	btn := models.InlineKeyboardButton{
		Text:         "Добавить новую запись",
		CallbackData: "AddNewSchedule",
	}

	keyboard = append(keyboard, []models.InlineKeyboardButton{btn})

	return builder.String(), &models.InlineKeyboardMarkup{InlineKeyboard: keyboard}
}

func (h *BotHandler) AddNewSchedule(ctx *context.Context, b *bot.Bot, update *models.Update) {

}
