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
	"queuebot/chats"
	"queuebot/db"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (h *BotHandler) SetChatService(service *chats.Service) {
	h.chats = service
}

func (h *BotHandler) chatListMenu(ctx context.Context, page int, userID int64) (string, *models.InlineKeyboardMarkup, error) {
	if h.chats == nil {
		return "", nil, errors.New("chat service is not configured")
	}
	registered, err := h.chats.List(ctx)
	if err != nil {
		return "", nil, err
	}
	const pageSize = 15
	lastPage := max(0, (len(registered)-1)/pageSize)
	page = min(max(page, 0), lastPage)
	var keyboard [][]models.InlineKeyboardButton
	for _, chat := range registered[page*pageSize : min((page+1)*pageSize, len(registered))] {
		label := chatName(chat)
		if chat.ID != 0 {
			label += fmt.Sprintf(" · %d", chat.ID)
		}
		if chat.IsDefault {
			label += " · основной"
		}
		keyboard = append(keyboard, []models.InlineKeyboardButton{{Text: label, CallbackData: fmt.Sprintf("SelectChat:%d", chat.ID)}})
	}
	var navigation []models.InlineKeyboardButton
	if page > 0 {
		navigation = append(navigation, models.InlineKeyboardButton{Text: "Предыдущие", CallbackData: fmt.Sprintf("ChatPage:%d", page-1)})
	}
	if page < lastPage {
		navigation = append(navigation, models.InlineKeyboardButton{Text: "Следующие", CallbackData: fmt.Sprintf("ChatPage:%d", page+1)})
	}
	if len(navigation) > 0 {
		keyboard = append(keyboard, navigation)
	}
	keyboard = append(keyboard,
		[]models.InlineKeyboardButton{{Text: "Добавить чат", CallbackData: "AddChat"}},
		[]models.InlineKeyboardButton{{Text: "Закончить редактирование", CallbackData: "LeaveEditSchedule"}})
	text := "Выберите чат для редактирования расписания.\n\nОсновной чат используется для команд в личном чате."
	if len(registered) == 0 {
		text += "\n\nПока нет добавленных чатов. Добавьте группу, в которой уже находится бот."
	}
	return text, h.filterEditorMarkup(ctx, userID, &models.InlineKeyboardMarkup{InlineKeyboard: keyboard}), nil
}

func chatName(chat chats.Chat) string {
	if chat.ID == 0 {
		return "Чат не указан"
	}
	if chat.Name != "" {
		return chat.Name
	}
	return fmt.Sprintf("Чат %d", chat.ID)
}

func (h *BotHandler) selectedChatMenu(ctx context.Context, userID int64) (string, *models.InlineKeyboardMarkup, error) {
	h.stateMu.RLock()
	session := h.userState[userID]
	target := session.targetChatID
	h.stateMu.RUnlock()
	if h.chats == nil || !session.chatSelected {
		return "", nil, errors.New("schedule chat is not selected")
	}
	chat, err := h.chats.Get(ctx, target)
	if errors.Is(err, chats.ErrNotFound) {
		h.stateMu.Lock()
		session := h.userState[userID]
		session.state, session.scheduleID, session.targetChatID, session.chatSelected = "", 0, 0, false
		h.userState[userID] = session
		h.stateMu.Unlock()
		return h.chatListMenu(ctx, 0, userID)
	}
	if err != nil {
		return "", nil, err
	}
	return h.chatScheduleMenu(ctx, userID, chat)
}

func (h *BotHandler) chatScheduleMenu(ctx context.Context, userID int64, chat chats.Chat) (string, *models.InlineKeyboardMarkup, error) {
	schedules, err := h.schedulesInChat(ctx, chat.ID)
	if err != nil {
		return "", nil, err
	}
	text, markup := h.GenerateEditMessage(chat, schedules, userID == h.developerID)
	return text, h.filterEditorMarkup(ctx, userID, markup), nil
}

func (h *BotHandler) selectedSchedule(ctx context.Context, userID int64, scheduleID int) (db.Schedule, error) {
	h.stateMu.RLock()
	session := h.userState[userID]
	target := session.targetChatID
	h.stateMu.RUnlock()
	if !session.chatSelected {
		return db.Schedule{}, chats.ErrNotFound
	}
	schedule, err := h.db.GetScheduleEntry(ctx, scheduleID)
	if err != nil {
		return db.Schedule{}, err
	}
	if schedule.ChatID != target {
		return db.Schedule{}, chats.ErrNotFound
	}
	return schedule, nil
}

func (h *BotHandler) ShowChatList(ctx context.Context, b *bot.Bot, update *models.Update) {
	query, _, chatID, userID, _ := h.getAllReplyInfo(update)
	page := 0
	if strings.HasPrefix(query.Data, "ChatPage:") {
		page, _ = strconv.Atoi(strings.TrimPrefix(query.Data, "ChatPage:"))
	}
	h.stateMu.Lock()
	session := h.userState[userID]
	session.state, session.scheduleID, session.targetChatID, session.chatSelected = "", 0, 0, false
	h.userState[userID] = session
	h.stateMu.Unlock()
	text, markup, err := h.chatListMenu(ctx, page, userID)
	if err != nil {
		h.answer(ctx, b, query, "Не удалось получить список чатов. Повторите попытку")
		return
	}
	h.editConfigText(ctx, b, chatID, query.Message.Message.ID, text, markup)
	h.answer(ctx, b, query, "")
}

func (h *BotHandler) SelectChat(ctx context.Context, b *bot.Bot, update *models.Update) {
	query, _, chatID, userID, _ := h.getAllReplyInfo(update)
	target, err := strconv.ParseInt(strings.TrimPrefix(query.Data, "SelectChat:"), 10, 64)
	if err != nil || h.chats == nil {
		h.answer(ctx, b, query, "Некорректный чат")
		return
	}
	chat, err := h.chats.Get(ctx, target)
	if err != nil {
		h.answer(ctx, b, query, "Чат недоступен. Откройте список чатов заново")
		return
	}
	h.stateMu.Lock()
	session := h.userState[userID]
	session.state, session.scheduleID, session.targetChatID, session.chatSelected = "", 0, target, true
	h.userState[userID] = session
	h.stateMu.Unlock()
	text, markup, err := h.chatScheduleMenu(ctx, userID, chat)
	if err != nil {
		h.answer(ctx, b, query, "Не удалось получить расписание чата")
		return
	}
	h.editConfigText(ctx, b, chatID, query.Message.Message.ID, text, markup)
	h.answer(ctx, b, query, "")
}

func (h *BotHandler) AddChat(ctx context.Context, b *bot.Bot, update *models.Update) {
	query, _, chatID, userID, _ := h.getAllReplyInfo(update)
	h.stateMu.Lock()
	session := h.userState[userID]
	session.state, session.scheduleID, session.targetChatID, session.chatSelected = "add_chat", 0, 0, false
	h.userState[userID] = session
	h.stateMu.Unlock()
	text := "Введите ID группы, в которую уже добавлен бот.\nНапример: -1001234567890. Название получим из Telegram."
	h.editConfigText(ctx, b, chatID, query.Message.Message.ID, text, chatListBackMarkup())
	h.answer(ctx, b, query, "")
}

func chatListBackMarkup() *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "К чатам", CallbackData: "ChatList"}}}}
}

func (h *BotHandler) RenameChat(ctx context.Context, b *bot.Bot, update *models.Update) {
	query, _, chatID, userID, _ := h.getAllReplyInfo(update)
	h.stateMu.Lock()
	session := h.userState[userID]
	session.state, session.scheduleID = "rename_chat", 0
	h.userState[userID] = session
	h.stateMu.Unlock()
	h.editConfigText(ctx, b, chatID, query.Message.Message.ID, "Введите название чата для меню бота. Название самой группы в Telegram не изменится.", &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{backBtnMainMenu}}})
	h.answer(ctx, b, query, "")
}

func (h *BotHandler) HandleChatInput(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	userID := message.From.ID
	h.stateMu.RLock()
	session := h.userState[userID]
	h.stateMu.RUnlock()
	h.editMu.RLock()
	messageID := h.editMessages[userID]
	h.editMu.RUnlock()
	if h.chats == nil {
		return
	}
	var chat chats.Chat
	var err error
	switch session.state {
	case "add_chat":
		var id int64
		id, err = strconv.ParseInt(strings.TrimSpace(message.Text), 10, 64)
		if err == nil {
			chat, err = h.chats.Add(ctx, id)
		}
		if err != nil {
			h.editConfigText(ctx, b, message.Chat.ID, messageID, "Не удалось добавить чат. Проверьте ID группы, что бот добавлен в неё и может отправлять сообщения.", chatListBackMarkup())
			return
		}
	case "rename_chat":
		chat, err = h.chats.Rename(ctx, session.targetChatID, message.Text)
		if err != nil {
			h.editConfigText(ctx, b, message.Chat.ID, messageID, "Не удалось сохранить название. Введите непустое название длиной до 200 символов.", &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{backBtnMainMenu}}})
			return
		}
	default:
		return
	}
	session.state, session.scheduleID, session.targetChatID, session.chatSelected = "", 0, chat.ID, true
	session.enteredAt = time.Now()
	h.stateMu.Lock()
	h.userState[userID] = session
	h.stateMu.Unlock()
	text, markup, err := h.chatScheduleMenu(ctx, userID, chat)
	if err != nil {
		log.Printf("read selected chat after saving: %v", err)
		h.editConfigText(ctx, b, message.Chat.ID, messageID, "Чат сохранён. Не удалось обновить расписание; откройте список чатов заново.", chatListBackMarkup())
		return
	}
	h.editConfigText(ctx, b, message.Chat.ID, messageID, text, markup)
}

func (h *BotHandler) SetDefaultChat(ctx context.Context, b *bot.Bot, update *models.Update) {
	query, _, chatID, userID, _ := h.getAllReplyInfo(update)
	if userID != h.developerID || h.chats == nil {
		h.answer(ctx, b, query, "Менять основной чат может только разработчик бота")
		return
	}
	h.stateMu.RLock()
	target := h.userState[userID].targetChatID
	h.stateMu.RUnlock()
	chat, err := h.chats.SetDefault(ctx, target)
	if err != nil {
		h.answer(ctx, b, query, "Не удалось изменить основной чат")
		return
	}
	text, markup, err := h.chatScheduleMenu(ctx, userID, chat)
	if err != nil {
		h.answer(ctx, b, query, "Основной чат сохранён, но не удалось обновить меню")
		return
	}
	h.editConfigText(ctx, b, chatID, query.Message.Message.ID, text, markup)
	h.answer(ctx, b, query, "Основной чат сохранён")
}

func (h *BotHandler) commandTarget(ctx context.Context, sourceChatID int64) (int64, error) {
	if sourceChatID < 0 {
		return sourceChatID, nil
	}
	if h.chats == nil {
		return 0, chats.ErrDefaultNotSet
	}
	return h.chats.ResolveTarget(ctx, sourceChatID)
}

func (h *BotHandler) filterEditorMarkup(ctx context.Context, userID int64, markup *models.InlineKeyboardMarkup) *models.InlineKeyboardMarkup {
	edit := h.allowed(ctx, userID, access.EditSchedule)
	chats := h.allowed(ctx, userID, access.ManageChats)
	week := h.allowed(ctx, userID, access.ChangeWeek)
	var rows [][]models.InlineKeyboardButton
	for _, row := range markup.InlineKeyboard {
		var buttons []models.InlineKeyboardButton
		for _, button := range row {
			prefix, _, _ := strings.Cut(button.CallbackData, ":")
			allowed := true
			switch prefix {
			case "AddChat", "RenameChat":
				allowed = chats
			case "SetCurrentWeek":
				allowed = week
			case "EditSchedule", "AddNewSchedule":
				allowed = edit
			case "SetDefaultChat":
				allowed = userID == h.developerID
			}
			if allowed {
				buttons = append(buttons, button)
			}
		}
		if len(buttons) > 0 {
			rows = append(rows, buttons)
		}
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}
