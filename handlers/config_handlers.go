package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"queuebot/access"
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
	{"moderation_block_duration", "Блокировка повторной записи", "Введите длительность от 1s до 24h, например 3m. Применится к новым сессиям управления участниками."},
	{"one_shot_duration", "Длительность разовой очереди", "Введите длительность от 1m до 168h, например 90m. Применится к новым очередям."},
}

func (h *BotHandler) EditConfigReplyText(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update == nil || update.Message == nil || update.Message.From == nil || update.Message.From.ID != h.developerID || h.settings == nil {
		return
	}
	h.configMu.Lock()
	defer h.configMu.Unlock()

	text, markup := configMenu(h.settings.Values(), "")
	if _, err := h.sendMenu(ctx, b, update, "config", text, markup); err != nil {
		log.Println("Не удалось отправить меню настройки бота")
	}
}

func (h *BotHandler) EditConfigField(ctx context.Context, b *bot.Bot, update *models.Update) {
	h.configMu.Lock()
	defer h.configMu.Unlock()
	query, _, session, ok := h.settingsCallback(ctx, update, "config", true)
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
	h.editConfigMenu(ctx, b, query.From.ID, text, configBackMarkup())
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
	_, hasMessage := h.editMessages[userID]
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
		h.editConfigMenu(ctx, b, userID, configPrompt(h.settings.Values(), field, text), configBackMarkup())
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
	h.editConfigMenu(ctx, b, userID, text, markup)
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
	case "delay_update_queue", "edit_session_timeout", "scheduler_tick_interval", "one_shot_delay", "one_shot_duration", "moderation_block_duration":
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
		case "moderation_block_duration":
			values.ModerationBlockDuration = duration
		}
	default:
		return errInvalidConfigInput
	}
	return nil
}

func (h *BotHandler) ConfigBack(ctx context.Context, b *bot.Bot, update *models.Update) {
	h.configMu.Lock()
	defer h.configMu.Unlock()
	query, _, session, ok := h.settingsCallback(ctx, update, "config", true)
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
	h.editConfigMenu(ctx, b, query.From.ID, text, markup)
	answerSettingsCallback(ctx, b, query.ID, "")
}

func (h *BotHandler) LeaveEditConfig(ctx context.Context, b *bot.Bot, update *models.Update) {
	h.configMu.Lock()
	defer h.configMu.Unlock()
	query, _, _, ok := h.settingsCallback(ctx, update, "config", true)
	if !ok {
		return
	}
	if err := h.closeMenu(ctx, b, query.From.ID); err != nil {
		log.Println("Не удалось удалить меню настройки; повторим удаление")
	}
	answerSettingsCallback(ctx, b, query.ID, "")
}

func (h *BotHandler) ToggleCurrentWeek(ctx context.Context, b *bot.Bot, update *models.Update) {
	h.configMu.Lock()
	defer h.configMu.Unlock()
	query, message, session, ok := h.settingsCallback(ctx, update, "schedule", false)
	if !ok || !h.allowed(ctx, query.From.ID, access.ChangeWeek) {
		return
	}
	parts := strings.Split(query.Data, ":")
	if len(parts) != 3 || parts[0] != "SetCurrentWeek" {
		answerSettingsCallback(ctx, b, query.ID, "Кнопка устарела. Откройте /edit_schedule заново")
		return
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
	text, markup, menuErr := h.selectedChatMenu(ctx, query.From.ID)
	if menuErr != nil {
		answerSettingsCallback(ctx, b, query.ID, status+". Откройте меню заново")
		return
	}
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

func (h *BotHandler) settingsCallback(ctx context.Context, update *models.Update, menu string, developerOnly bool) (*models.CallbackQuery, *models.Message, userState, bool) {
	if update == nil || update.CallbackQuery == nil || update.CallbackQuery.Message.Message == nil || h.settings == nil {
		return nil, nil, userState{}, false
	}
	query := update.CallbackQuery
	if developerOnly && query.From.ID != h.developerID {
		return nil, nil, userState{}, false
	}
	h.stateMu.RLock()
	session, exists := h.userState[query.From.ID]
	h.stateMu.RUnlock()
	if !exists || session.menu != menu || !h.menuCallbackMatches(ctx, update) {
		return nil, nil, userState{}, false
	}
	return query, query.Message.Message, session, true
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
	keyboard = append(keyboard, []models.InlineKeyboardButton{{Text: "Администраторы", CallbackData: "RolesMenu"}})
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
	case "moderation_block_duration":
		return configDuration(values.ModerationBlockDuration)
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

func (h *BotHandler) editConfigMenu(ctx context.Context, b *bot.Bot, userID int64, text string, markup *models.InlineKeyboardMarkup) {
	if err := h.editMenu(ctx, b, userID, text, markup); err != nil {
		log.Println("Не удалось обновить меню настройки бота")
	}
}
