package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"

	"queuebot/access"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type AccessControl interface {
	Allowed(context.Context, int64, access.Permission) (bool, error)
	ListAdmins(context.Context) ([]int64, error)
	Permissions(context.Context, int64) (map[access.Permission]bool, error)
	AddAdmin(context.Context, int64, int64) error
	RemoveAdmin(context.Context, int64, int64) error
	SetPermission(context.Context, int64, int64, access.Permission, bool) error
}

func (h *BotHandler) allowed(ctx context.Context, userID int64, permission access.Permission) bool {
	if h.access == nil {
		return false
	}
	allowed, err := h.access.Allowed(ctx, userID, permission)
	if err != nil {
		log.Print("Не удалось проверить разрешение пользователя")
		return false
	}
	return allowed
}

func (h *BotHandler) canEdit(ctx context.Context, userID int64) bool {
	for _, permission := range []access.Permission{access.EditSchedule, access.ManageChats, access.ChangeWeek} {
		if h.allowed(ctx, userID, permission) {
			return true
		}
	}
	return false
}

func (h *BotHandler) allowedState(ctx context.Context, userID int64, state userState) bool {
	switch state.state {
	case "role_add", "edit_config":
		return userID == h.developerID
	case "add_chat", "rename_chat":
		return h.allowed(ctx, userID, access.ManageChats)
	case "awaiting_schedule", "edit_start_time", "edit_end_time", "edit_thread_id", "edit_description", "edit_title":
		return h.allowed(ctx, userID, access.EditSchedule)
	}
	return state.menu == "config" && userID == h.developerID || h.canEdit(ctx, userID)
}

func updateUserID(update *models.Update) int64 {
	if update == nil {
		return 0
	}
	if update.CallbackQuery != nil {
		return update.CallbackQuery.From.ID
	}
	if update.Message != nil && update.Message.From != nil {
		return update.Message.From.ID
	}
	return 0
}

func (h *BotHandler) deny(ctx context.Context, b *bot.Bot, update *models.Update, silent bool) {
	text := "У Вас недостаточно прав для выполнения действия"
	if silent {
		text = ""
	}
	if update.CallbackQuery != nil {
		h.answer(ctx, b, update.CallbackQuery, text)
		return
	}
	if !silent && update.Message != nil {
		h.sendSwapInfoMessage(ctx, b, update.Message.Chat.ID, update.Message.MessageThreadID, text)
	}
}

func (h *BotHandler) requirePermission(permission access.Permission, silent bool, next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		if !h.allowed(ctx, updateUserID(update), permission) {
			h.deny(ctx, b, update, silent)
			return
		}
		next(ctx, b, update)
	}
}

func (h *BotHandler) requireDeveloper(silent bool, next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		if updateUserID(update) != h.developerID {
			h.deny(ctx, b, update, silent)
			return
		}
		next(ctx, b, update)
	}
}

func (h *BotHandler) requireEditor(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		userID := updateUserID(update)
		allowed := h.canEdit(ctx, userID)
		if query := update.CallbackQuery; query != nil {
			parts := strings.Split(menuCallbackData(query.Data), ":")
			switch parts[0] {
			case "AddChat", "RenameChat":
				allowed = h.allowed(ctx, userID, access.ManageChats)
			case "SetCurrentWeek":
				allowed = h.allowed(ctx, userID, access.ChangeWeek)
			case "SetDefaultChat":
				allowed = userID == h.developerID
			case "ChatList", "ChatPage", "SelectChat", "LeaveEditSchedule":
			case "Back":
				if len(parts) < 2 || parts[1] != "mainmenu" {
					allowed = h.allowed(ctx, userID, access.EditSchedule)
				}
			default:
				allowed = h.allowed(ctx, userID, access.EditSchedule)
			}
		}
		if !allowed {
			h.deny(ctx, b, update, true)
			return
		}
		next(ctx, b, update)
	}
}

var permissionLabels = map[access.Permission]string{
	access.ViewChatID:          "Просмотр ID чата",
	access.CreateOneShot:       "Разовые очереди",
	access.ManageQueueMessages: "Восстановление и повторная отправка",
	access.EditSchedule:        "Редактирование расписания",
	access.ManageChats:         "Добавление и названия чатов",
	access.ChangeWeek:          "Смена текущего типа недели",
	access.MarkOthers:          "Отметки сдачи других участников",
	access.RemoveParticipants:  "Исключение участников",
}

func (h *BotHandler) rolesMenu(ctx context.Context) (string, *models.InlineKeyboardMarkup, error) {
	if h.access == nil {
		return "", nil, errors.New("access service is not configured")
	}
	ids, err := h.access.ListAdmins(ctx)
	if err != nil {
		return "", nil, err
	}
	var rows [][]models.InlineKeyboardButton
	for _, id := range ids {
		if id == h.developerID {
			continue
		}
		rows = append(rows, []models.InlineKeyboardButton{{Text: strconv.FormatInt(id, 10), CallbackData: fmt.Sprintf("RoleSelect:%d", id)}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: "Добавить администратора", CallbackData: "RoleAdd"}}, []models.InlineKeyboardButton{{Text: "Назад", CallbackData: "ConfigBack"}})
	return "Администраторы бота\n\nВыберите администратора, чтобы настроить доступы. Разрешения действуют во всех чатах. У новых администраторов все разрешения выключены.", &models.InlineKeyboardMarkup{InlineKeyboard: rows}, nil
}

func (h *BotHandler) roleCard(ctx context.Context, target int64) (string, *models.InlineKeyboardMarkup, error) {
	permissions, err := h.access.Permissions(ctx, target)
	if err != nil {
		return "", nil, err
	}
	var rows [][]models.InlineKeyboardButton
	for index, permission := range access.AllPermissions() {
		state := "выкл."
		if permissions[permission] {
			state = "вкл."
		}
		rows = append(rows, []models.InlineKeyboardButton{{Text: permissionLabels[permission] + ": " + state, CallbackData: fmt.Sprintf("RolePerm:%d:%d:%t", target, index, !permissions[permission])}})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: "Снять права администратора", CallbackData: fmt.Sprintf("RoleRemove:%d", target)}}, []models.InlineKeyboardButton{{Text: "К администраторам", CallbackData: "RolesMenu"}})
	return fmt.Sprintf("Администратор: %d\n\nНажмите на разрешение, чтобы изменить его. Изменения применяются сразу.", target), &models.InlineKeyboardMarkup{InlineKeyboard: rows}, nil
}

func (h *BotHandler) rolesHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if updateUserID(update) != h.developerID || h.access == nil {
		h.deny(ctx, b, update, true)
		return
	}
	query := update.CallbackQuery
	parts := strings.Split(query.Data, ":")
	h.stateMu.RLock()
	session := h.userState[query.From.ID]
	h.stateMu.RUnlock()
	var text string
	var markup *models.InlineKeyboardMarkup
	var err error
	switch parts[0] {
	case "RolesMenu":
		session.state = ""
		session.roleTargetID = 0
		text, markup, err = h.rolesMenu(ctx)
	case "RoleAdd":
		session.state = "role_add"
		session.roleTargetID = 0
		text = "Введите Telegram ID нового администратора. Все его разрешения будут выключены."
		markup = &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "Назад", CallbackData: "RolesMenu"}}}}
	case "RoleSelect":
		if len(parts) != 2 {
			return
		}
		session.roleTargetID, err = strconv.ParseInt(parts[1], 10, 64)
		if err == nil && session.roleTargetID != h.developerID {
			text, markup, err = h.roleCard(ctx, session.roleTargetID)
		} else {
			return
		}
		session.state = ""
	case "RolePerm":
		if len(parts) != 4 || session.roleTargetID <= 0 || parts[1] != strconv.FormatInt(session.roleTargetID, 10) {
			return
		}
		index, parseErr := strconv.Atoi(parts[2])
		permissions := access.AllPermissions()
		if parseErr != nil || index < 0 || index >= len(permissions) {
			return
		}
		var enabled bool
		enabled, err = strconv.ParseBool(parts[3])
		if err == nil {
			err = h.access.SetPermission(ctx, query.From.ID, session.roleTargetID, permissions[index], enabled)
		}
		if err == nil && !enabled {
			_ = h.closeMenu(ctx, b, session.roleTargetID)
		}
		if err == nil {
			text, markup, err = h.roleCard(ctx, session.roleTargetID)
		}
	case "RoleRemove":
		if len(parts) != 2 || session.roleTargetID <= 0 || parts[1] != strconv.FormatInt(session.roleTargetID, 10) {
			return
		}
		err = h.access.RemoveAdmin(ctx, query.From.ID, session.roleTargetID)
		if err == nil {
			_ = h.closeMenu(ctx, b, session.roleTargetID)
			session.state = ""
			session.roleTargetID = 0
			text, markup, err = h.rolesMenu(ctx)
		}
	default:
		return
	}
	if err != nil {
		h.answer(ctx, b, query, "Не удалось изменить доступы. Откройте список администраторов заново")
		return
	}
	h.stateMu.Lock()
	h.userState[query.From.ID] = session
	h.stateMu.Unlock()
	if err := h.editMenu(ctx, b, query.From.ID, text, markup); err != nil {
		log.Print("Не удалось обновить меню администраторов")
	}
	h.answer(ctx, b, query, "")
}

func (h *BotHandler) handleRoleInput(ctx context.Context, b *bot.Bot, update *models.Update) {
	message := update.Message
	if message.From.ID != h.developerID || h.access == nil {
		return
	}
	target, err := strconv.ParseInt(strings.TrimSpace(message.Text), 10, 64)
	if err == nil {
		err = h.access.AddAdmin(ctx, message.From.ID, target)
	}
	if err != nil {
		_ = h.editMenu(ctx, b, message.From.ID, "Не удалось добавить администратора. Введите положительный Telegram ID пользователя.", &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "Назад", CallbackData: "RolesMenu"}}}})
		return
	}
	h.stateMu.Lock()
	session := h.userState[message.From.ID]
	session.state = ""
	session.roleTargetID = target
	h.userState[message.From.ID] = session
	h.stateMu.Unlock()
	text, markup, err := h.roleCard(ctx, target)
	if err != nil {
		log.Print("Не удалось прочитать разрешения администратора")
		return
	}
	if err := h.editMenu(ctx, b, message.From.ID, text, markup); err != nil {
		log.Print("Не удалось обновить карточку администратора")
	}
}
