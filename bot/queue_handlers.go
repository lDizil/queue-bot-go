package bot

import (
	"context"
	"fmt"
	"log"
	"os"
	"queuebot/db"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

var totalSlotsInQueue, _ = strconv.Atoi(os.Getenv("TOTAL_SLOTS_IN_QUEUE"))
var amountOfSlotsInRow, _ = strconv.Atoi(os.Getenv("AMOUNT_OF_BUTTONS_IN_ROW"))

type BotHandler struct {
    db *db.DBRepository
}

func NewBotHandler(db *db.DBRepository) *BotHandler {
    return &BotHandler{db: db}
}
 
func(h *BotHandler) handleError(ctx context.Context,  b *bot.Bot, callbackID string, msg string) {
	log.Println(msg)
	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: callbackID,
		Text:            "Что-то пошло не так, обратитесь к модерации",
		ShowAlert:       true,
	})
}

func (h *BotHandler) SendQueueMessage(ctx context.Context, b *bot.Bot, update *models.Update) {
	if totalSlotsInQueue == 0 || amountOfSlotsInRow == 0 {
		log.Println("Не заданы переменные окружения для настройки очереди")
		return
	}

	scheduleID := 2

	queue, err := h.db.GetQueue(ctx, scheduleID)
	if err != nil {
		log.Println("Ошибка получения очереди")
		return
	}

	text, markup := h.RenderQueueMessage(queue, scheduleID)

	msg, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: update.Message.Chat.ID,
		Text: text, 
		ReplyMarkup: markup,
	})

	if err != nil {
		log.Println("Ошибка при отправке очереди в чат: ", err)
		return
	}

	log.Printf("Сообщение очереди (id: %d) отправлено в чат", msg.ID)
}


func (h *BotHandler) RenderQueueMessage(queue []db.QueueEntry, scheduleID int) (string, *models.InlineKeyboardMarkup) {
	taken := make(map[int]db.QueueEntry, totalSlotsInQueue)
	isTaken := map[int]bool{}
	
	for i := 1; i < totalSlotsInQueue; i++ {
		isTaken[i] = false
	}

	for _, entry := range queue {
		taken[entry.Position] = entry
	}

	var builder strings.Builder
 
	text := "Это сообщение - таблица для записи в очередь\nДля выбора места воспользуйтесь кнопками снизу\nУдачной сдачи работы \n\n"

	builder.Write([]byte(text))
	builder.Grow(2*len(text))

	if len(queue) == 0 {
		builder.Write([]byte("Очередь пуста\n\n"))
	} else {
		builder.Write([]byte("Текущая очередь:\n\n"))
		for _, entry := range queue {
			isTaken[entry.Position] = true
			builder.Write([]byte(fmt.Sprintf("%d. @%s\n", entry.Position, entry.Username)))
		}

		builder.Write([]byte("\nСвободные места: "))

		for i := 1; i <= totalSlotsInQueue; i++ {
			if !isTaken[i] {
				builder.Write([]byte(fmt.Sprintf("%d, ", i)))
			}
		}
	}

	keyboard := [][]models.InlineKeyboardButton{}
	row := []models.InlineKeyboardButton{}

	for i := 1; i < totalSlotsInQueue; i++ {
		var btn models.InlineKeyboardButton

		if _, ok := taken[i]; ok {
			btn = models.InlineKeyboardButton {
				Text:         "❌",
				CallbackData: fmt.Sprintf("Join:%d", i),
			}
		} else {
			btn = models.InlineKeyboardButton{
                Text:         fmt.Sprintf("%d", i),
                CallbackData: fmt.Sprintf("Join:%d:%d", scheduleID, i),
            }
		}

		row = append(row, btn)

		if len(row) >= amountOfSlotsInRow {
			keyboard = append(keyboard, row)
			row = []models.InlineKeyboardButton{}
		}
	}

	if len(row) != 0 {
		keyboard = append(keyboard, row)
	}

	btnJoinQueue := models.InlineKeyboardButton{
		Text:         "Занять ближайшее свободное место",
		CallbackData: fmt.Sprintf("JoinFistFreeslot:%d",scheduleID),
	}

	btnLeaveQueue := models.InlineKeyboardButton{
		Text:         "Выйти из очереди",
		CallbackData: fmt.Sprintf("LeaveFromQueue:%d",scheduleID),
	}

	btnSendQueueAgain := models.InlineKeyboardButton{
		Text:         "Отправить очередь повторно",
		CallbackData:  fmt.Sprintf("SendQueueAgain:%d",scheduleID),
	}

	keyboard = append(keyboard, []models.InlineKeyboardButton{btnJoinQueue}, []models.InlineKeyboardButton{btnLeaveQueue}, []models.InlineKeyboardButton{btnSendQueueAgain})

	return builder.String(), &models.InlineKeyboardMarkup{InlineKeyboard: keyboard}
}

func (h *BotHandler) UpdateQueueMessage(ctx context.Context, b *bot.Bot, chatID int64, messageID int, scheduleID int) {
	queue, err := h.db.GetQueue(ctx, scheduleID)

	if err != nil {
		log.Println("Ошибка получения очереди")
		return
	}

	text, markup := h.RenderQueueMessage(queue, scheduleID)

	b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID: chatID,
		MessageID: messageID,
		Text: text,
		ReplyMarkup: markup,
	})
}

func (h *BotHandler) JoinQueue(ctx context.Context, b *bot.Bot, update *models.Update) {
	query := update.CallbackQuery
	data := query.Data

	userID := query.From.ID
	username := query.From.Username

	scheduleID, err := strconv.Atoi(strings.Split(data, ":")[1])
	if err != nil {
		msg := fmt.Sprintf("Ошибка во время преобразования scheduleID в int (пользователь %s): %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return
	}

	slot, err := strconv.Atoi(strings.Split(data, ":")[2])
	if err != nil {
		msg := fmt.Sprintf("Ошибка во время преобразования slot в int (пользователь %s): %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return 
	}

	isUserInQueue, err := h.db.IsUserInQueue(ctx, userID, scheduleID)
	if err != nil {
		msg := fmt.Sprintf("Ошибка проверки стоит ли пользователь в очереди (пользователь %s): %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return 
	}

	if isUserInQueue {
		b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			Text: "Вы уже заняли место в очереди",
			CallbackQueryID: query.ID,
		})

		return 
	}

	enrty := db.QueueEntry{
		UserId: userID,
		Username: username,
		Position: slot,
		ScheduleID: scheduleID,
	}

	
	err = h.db.JoinQueue(ctx, enrty)
	if err != nil {
		msg := fmt.Sprintf("Ошибка при попытке встать в очередь (пользователь %s): %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return 
	}

	msg :=  query.Message.Message
	chatID    := msg.Chat.ID
	messageID := msg.ID

	h.UpdateQueueMessage(ctx, b, chatID, messageID, scheduleID)

	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			Text: fmt.Sprintf("Вы успешно заняли %d в очереди\nДа прибудет с вами сила", slot),
			CallbackQueryID: query.ID,
		})
	 
}

func (h *BotHandler) LeaveQueue(ctx context.Context, b *bot.Bot, update *models.Update)  {
	query := update.CallbackQuery
	data := query.Data

	userID := query.From.ID
	username := query.From.Username

	scheduleID, err := strconv.Atoi(strings.Split(data, ":")[1])

	isUserInQueue, err := h.db.IsUserInQueue(ctx, userID, scheduleID)
	if err != nil {
		msg := fmt.Sprintf("Ошибка проверки стоит ли пользователь в очереди (пользователь %s): %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return 
	}

	if !isUserInQueue {
		b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			Text: "Вы не находитель в очереди",
			CallbackQueryID: query.ID,
		})

		return 
	}

	err = h.db.LeaveFromQueue(ctx, userID, scheduleID)

	if err != nil {
		msg := fmt.Sprintf("Ошибка выхода из очереди (пользователь %s): %v", username, err)
		h.handleError(ctx, b, query.ID, msg)
		return 
	}

	msg :=  query.Message.Message
	chatID    := msg.Chat.ID
	messageID := msg.ID

	h.UpdateQueueMessage(ctx, b, chatID, messageID, scheduleID)
}