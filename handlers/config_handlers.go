package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"queuebot/db"
	"queuebot/settings"
	"queuebot/utils"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type configField struct {
	key   string
	label string
	hint  string
}

var configFields = []configField{
	{"total_slots_in_queue", "Мест в очереди", "Введите целое число от 1 до 90. Применится к новым очередям."},
	{"amount_of_buttons_in_row", "Кнопок в строке", "Введите целое число от 1 до 8. Применится к новым очередям."},
	{"delay_update_queue", "Задержка обновления сообщения", "Введите длительность от 50ms до 30s, например 400ms."},
	{"edit_session_timeout", "Таймаут редактирования", "Введите длительность от 30s до 24h, например 4m."},
	{"week1_date", "Опорная дата календаря", "Введите дату в формате ГГГГ-ММ-ДД, например 2026-09-01. Тип недели задаёт чётность всей недели этой даты."},
	{"week1_type", "Тип опорной недели", "Введите чётная или нечётная. Можно также even или odd."},
	{"scheduler_tick_interval", "Интервал проверки расписания", "Введите длительность от 100ms до 1m, например 1s."},
	{"one_shot_delay", "До открытия разовой очереди", "Введите длительность от 1m до 168h, например 6m. Применится к новым очередям."},
	{"one_shot_duration", "Длительность разовой очереди", "Введите длительность от 1m до 168h, например 90m. Применится к новым очередям."},
}

func (h *BotHandler) EditConfigReplyText(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update == nil || update.Message == nil || update.Message.From == nil || update.Message.From.ID != h.developerID || h.settings == nil {
		return
	}
	h.configMu.Lock()
	defer h.configMu.Unlock()

	var (
		source = update.Message
		userID = source.From.ID
		chatID = source.Chat.ID
	)
	h.editMu.Lock()
	oldMessageID := h.editMessages[userID]
	oldChatID := h.editMessageChats[userID]
	delete(h.editMessages, userID)
	delete(h.editMessageChats, userID)
	h.editMu.Unlock()
	h.stateMu.Lock()
	delete(h.userState, userID)
	h.stateMu.Unlock()
	if oldMessageID != 0 {
		if _, err := b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: oldChatID, MessageID: oldMessageID}); err != nil {
			log.Println("Не удалось удалить предыдущее меню редактирования")
		}
	}

	text, markup := configMenu(h.settings.Values(), "")
	message, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:          chatID,
		MessageThreadID: source.MessageThreadID,
		Text:            text,
		ReplyMarkup:     markup,
	})
	if err != nil {
		log.Println("Не удалось отправить меню настройки бота")
		return
	}
	h.editMu.Lock()
	h.editMessages[userID] = message.ID
	h.editMessageChats[userID] = chatID
	h.editMu.Unlock()
	h.stateMu.Lock()
	h.userState[userID] = userState{
		menu:      "config",
		enteredAt: time.Now(),
		chatID:    chatID,
		threadID:  source.MessageThreadID,
	}
	h.stateMu.Unlock()
}

func (h *BotHandler) EditConfigField(ctx context.Context, b *bot.Bot, update *models.Update) {
	h.configMu.Lock()
	defer h.configMu.Unlock()
	query, message, session, ok := h.settingsCallback(update, "config", true)
	if !ok {
		return
	}
	key, found := strings.CutPrefix(query.Data, "EditConfig:")
	field, exists := findConfigField(key)
	if !found || !exists {
		answerSettingsCallback(ctx, b, query.ID, "Такой настройки нет")
		return
	}
	session.state = "edit_config"
	session.configKey = field.key
	session.enteredAt = time.Now()
	h.stateMu.Lock()
	h.userState[query.From.ID] = session
	h.stateMu.Unlock()

	text := configPrompt(h.settings.Values(), field, "")
	h.editConfigText(ctx, b, message.Chat.ID, message.ID, text, configBackMarkup())
	answerSettingsCallback(ctx, b, query.ID, "")
}

func (h *BotHandler) HandleConfigInput(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update == nil || update.Message == nil || update.Message.From == nil || update.Message.From.ID != h.developerID || h.settings == nil {
		return
	}
	h.configMu.Lock()
	defer h.configMu.Unlock()

	message := update.Message
	userID := message.From.ID
	h.stateMu.RLock()
	session, exists := h.userState[userID]
	h.stateMu.RUnlock()
	if !exists || session.menu != "config" || session.state != "edit_config" || session.chatID != message.Chat.ID || session.threadID != message.MessageThreadID || time.Since(session.enteredAt) > h.settings.Values().TimeForExpiredEditSes {
		return
	}
	h.editMu.RLock()
	messageID, hasMessage := h.editMessages[userID]
	menuChatID := h.editMessageChats[userID]
	h.editMu.RUnlock()
	if !hasMessage || menuChatID != message.Chat.ID {
		return
	}
	field, exists := findConfigField(session.configKey)
	if !exists {
		return
	}

	err := h.settings.Update(ctx, func(values *settings.Values) error {
		if err := changeConfigValue(values, field.key, message.Text); err != nil {
			return errInvalidConfigInput
		}
		if err := values.Validate(); err != nil {
			return errInvalidConfigInput
		}
		return nil
	})
	if err != nil {
		text := "Не удалось сохранить настройку. Повторите попытку."
		if errors.Is(err, errInvalidConfigInput) {
			text = "Значение не подходит. " + field.hint
		} else {
			log.Println("Не удалось сохранить настройки бота")
		}
		h.editConfigText(ctx, b, message.Chat.ID, messageID, configPrompt(h.settings.Values(), field, text), configBackMarkup())
		return
	}
	session.state = ""
	session.configKey = ""
	session.enteredAt = time.Now()
	h.stateMu.Lock()
	h.userState[userID] = session
	h.stateMu.Unlock()

	status := "Настройка сохранена."
	if h.scheduler != nil {
		if err := h.scheduler.Reconfigure(); err != nil {
			log.Println("Настройки сохранены, но не удалось перепланировать будущие очереди")
			status += " Не удалось обновить расписание; повторите сохранение настройки."
		}
	}
	text, markup := configMenu(h.settings.Values(), status)
	h.editConfigText(ctx, b, message.Chat.ID, messageID, text, markup)
}

var errInvalidConfigInput = errors.New("invalid configuration input")

func changeConfigValue(values *settings.Values, key, input string) error {
	input = strings.TrimSpace(input)
	switch key {
	case "total_slots_in_queue", "amount_of_buttons_in_row":
		number, err := strconv.Atoi(input)
		if err != nil {
			return err
		}
		if key == "total_slots_in_queue" {
			values.TotalSlotsInQueue = number
			return nil
		}
		values.AmountOfButtonsInRow = number
	case "week1_date":
		values.Week1Date = input
	case "week1_type":
		input = strings.ToLower(input)
		if translated, exists := weekTypeRuToEn[input]; exists {
			input = translated
		}
		values.Week1Type = input
	case "delay_update_queue", "edit_session_timeout", "scheduler_tick_interval", "one_shot_delay", "one_shot_duration":
		duration, err := time.ParseDuration(input)
		if err != nil {
			return err
		}
		switch key {
		case "delay_update_queue":
			values.DelayUpdateQueue = duration
		case "edit_session_timeout":
			values.TimeForExpiredEditSes = duration
		case "scheduler_tick_interval":
			values.SchedulerTickInterval = duration
		case "one_shot_delay":
			values.OneShotDelay = duration
		case "one_shot_duration":
			values.OneShotDuration = duration
		}
	default:
		return errInvalidConfigInput
	}
	return nil
}

func (h *BotHandler) ConfigBack(ctx context.Context, b *bot.Bot, update *models.Update) {
	h.configMu.Lock()
	defer h.configMu.Unlock()
	query, message, session, ok := h.settingsCallback(update, "config", true)
	if !ok {
		return
	}
	session.state = ""
	session.configKey = ""
	session.enteredAt = time.Now()
	h.stateMu.Lock()
	h.userState[query.From.ID] = session
	h.stateMu.Unlock()
	text, markup := configMenu(h.settings.Values(), "")
	h.editConfigText(ctx, b, message.Chat.ID, message.ID, text, markup)
	answerSettingsCallback(ctx, b, query.ID, "")
}

func (h *BotHandler) LeaveEditConfig(ctx context.Context, b *bot.Bot, update *models.Update) {
	h.configMu.Lock()
	defer h.configMu.Unlock()
	query, message, _, ok := h.settingsCallback(update, "config", true)
	if !ok {
		return
	}
	h.stateMu.Lock()
	delete(h.userState, query.From.ID)
	h.stateMu.Unlock()
	h.editMu.Lock()
	delete(h.editMessages, query.From.ID)
	delete(h.editMessageChats, query.From.ID)
	h.editMu.Unlock()
	h.editConfigText(ctx, b, message.Chat.ID, message.ID, "Настройка бота завершена", nil)
	answerSettingsCallback(ctx, b, query.ID, "")
}

func (h *BotHandler) ToggleCurrentWeek(ctx context.Context, b *bot.Bot, update *models.Update) {
	h.configMu.Lock()
	defer h.configMu.Unlock()
	query, message, session, ok := h.settingsCallback(update, "schedule", false)
	if !ok || !h.adminIDs[query.From.ID] {
		return
	}
	parts := strings.Split(query.Data, ":")
	if len(parts) != 3 || parts[0] != "SetCurrentWeek" {
		answerSettingsCallback(ctx, b, query.ID, "Кнопка устарела. Откройте /edit_schedule заново")
		return
	}
	var schedules []db.Schedule
	if h.db != nil {
		var err error
		schedules, err = h.schedulesInChat(ctx, message.Chat.ID)
		if err != nil {
			log.Println("Не удалось прочитать расписание перед сменой недели")
			answerSettingsCallback(ctx, b, query.ID, "Не удалось обновить неделю. Повторите попытку")
			return
		}
	}
	now := time.Now()
	err := h.settings.Update(ctx, func(values *settings.Values) error {
		return setCurrentWeek(values, parts[1], parts[2], now)
	})
	if err != nil {
		if errors.Is(err, errInvalidConfigInput) {
			answerSettingsCallback(ctx, b, query.ID, "Кнопка устарела. Откройте /edit_schedule заново")
			return
		}
		log.Println("Не удалось сохранить чётность недели")
		answerSettingsCallback(ctx, b, query.ID, "Не удалось сохранить неделю. Повторите попытку")
		return
	}
	status := "Теперь текущая неделя — " + weekTypeEnToRu[parts[2]]
	if h.scheduler != nil {
		if err := h.scheduler.Reconfigure(); err != nil {
			log.Println("Чётность сохранена, но не удалось перепланировать будущие очереди")
			status = "Неделя сохранена, но будущие очереди не перепланированы"
		}
	}
	session.state = ""
	session.configKey = ""
	session.enteredAt = time.Now()
	h.stateMu.Lock()
	h.userState[query.From.ID] = session
	h.stateMu.Unlock()
	text, markup := h.GenerateEditMessage(schedules)
	h.editConfigText(ctx, b, message.Chat.ID, message.ID, text, markup)
	answerSettingsCallback(ctx, b, query.ID, status)
}

func setCurrentWeek(values *settings.Values, monday, weekType string, now time.Time) error {
	if monday != configWeekMonday(now).Format("2006-01-02") || (weekType != "odd" && weekType != "even") {
		return errInvalidConfigInput
	}
	values.Week1Date = monday
	values.Week1Type = weekType
	return nil
}

func configWeekMonday(now time.Time) time.Time {
	now = now.In(utils.Moscow())
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, utils.Moscow())
	offset := (int(midnight.Weekday()) + 6) % 7
	return midnight.AddDate(0, 0, -offset)
}

func (h *BotHandler) settingsCallback(update *models.Update, menu string, developerOnly bool) (*models.CallbackQuery, *models.Message, userState, bool) {
	if update == nil || update.CallbackQuery == nil || update.CallbackQuery.Message.Message == nil || h.settings == nil {
		return nil, nil, userState{}, false
	}
	var (
		query   = update.CallbackQuery
		message = query.Message.Message
		userID  = query.From.ID
	)
	if developerOnly && userID != h.developerID {
		return nil, nil, userState{}, false
	}
	h.stateMu.RLock()
	session, exists := h.userState[userID]
	h.stateMu.RUnlock()
	if !exists || session.menu != menu || session.chatID != message.Chat.ID || session.threadID != message.MessageThreadID || time.Since(session.enteredAt) > h.settings.Values().TimeForExpiredEditSes {
		return nil, nil, userState{}, false
	}
	h.editMu.RLock()
	messageID := h.editMessages[userID]
	chatID := h.editMessageChats[userID]
	h.editMu.RUnlock()
	if messageID != message.ID || chatID != message.Chat.ID {
		return nil, nil, userState{}, false
	}
	return query, message, session, true
}

func configMenu(values settings.Values, status string) (string, *models.InlineKeyboardMarkup) {
	text := "Настройки бота\n\nРазмер и сетка применяются к новым очередям. Настройки сохраняются между перезапусками."
	if status != "" {
		text = status + "\n\n" + text
	}
	keyboard := make([][]models.InlineKeyboardButton, 0, len(configFields)+1)
	for _, field := range configFields {
		keyboard = append(keyboard, []models.InlineKeyboardButton{{
			Text:         field.label + ": " + configValue(values, field.key),
			CallbackData: "EditConfig:" + field.key,
		}})
	}
	keyboard = append(keyboard, []models.InlineKeyboardButton{{Text: "Завершить настройку", CallbackData: "ConfigLeave"}})
	return text, &models.InlineKeyboardMarkup{InlineKeyboard: keyboard}
}

func findConfigField(key string) (configField, bool) {
	for _, field := range configFields {
		if field.key == key {
			return field, true
		}
	}
	return configField{}, false
}

func configPrompt(values settings.Values, field configField, status string) string {
	text := fmt.Sprintf("%s\nСейчас: %s\n\n%s", field.label, configValue(values, field.key), field.hint)
	if status != "" {
		text = status + "\n\n" + text
	}
	return text
}

func configValue(values settings.Values, key string) string {
	switch key {
	case "total_slots_in_queue":
		return strconv.Itoa(values.TotalSlotsInQueue)
	case "amount_of_buttons_in_row":
		return strconv.Itoa(values.AmountOfButtonsInRow)
	case "delay_update_queue":
		return configDuration(values.DelayUpdateQueue)
	case "edit_session_timeout":
		return configDuration(values.TimeForExpiredEditSes)
	case "week1_date":
		return values.Week1Date
	case "week1_type":
		return weekTypeEnToRu[values.Week1Type]
	case "scheduler_tick_interval":
		return configDuration(values.SchedulerTickInterval)
	case "one_shot_delay":
		return configDuration(values.OneShotDelay)
	case "one_shot_duration":
		return configDuration(values.OneShotDuration)
	}
	return ""
}

func configDuration(duration time.Duration) string {
	text := duration.String()
	if duration%time.Minute == 0 {
		text = strings.TrimSuffix(text, "0s")
	}
	if duration%time.Hour == 0 {
		text = strings.TrimSuffix(text, "0m")
	}
	return text
}

func configBackMarkup() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "Назад", CallbackData: "ConfigBack"}}}}
}

func (h *BotHandler) editConfigText(ctx context.Context, b *bot.Bot, chatID int64, messageID int, text string, markup *models.InlineKeyboardMarkup) {
	if _, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID: chatID, MessageID: messageID, Text: text, ReplyMarkup: markup}); err != nil {
		if !strings.Contains(err.Error(), "message is not modified") {
			log.Println("Не удалось обновить меню настройки бота")
		}
	}
}

func answerSettingsCallback(ctx context.Context, b *bot.Bot, callbackID, text string) {
	if _, err := b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: callbackID, Text: text}); err != nil {
		log.Println("Не удалось подтвердить нажатие в меню настройки бота")
	}
}
