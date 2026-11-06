package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"queuebot/access"
	"queuebot/dispatch"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func (h *BotHandler) NewTelegramBot(token string, options ...bot.Option) (*bot.Bot, error) {
	if h.dispatcher != nil {
		return nil, errors.New("Telegram bot is already initialized")
	}
	h.dispatcher = dispatch.New(4, 128)
	options = append(options, bot.WithWorkers(1), bot.WithNotAsyncHandlers(),
		bot.WithDefaultHandler(h.StateHandler), bot.WithMiddlewares(h.dispatchUpdates))
	telegram, err := bot.New(token, options...)
	if err != nil {
		_ = h.dispatcher.Shutdown(context.Background())
		return nil, err
	}
	return telegram, nil
}

func (h *BotHandler) Shutdown(ctx context.Context) error {
	var err error
	if h.dispatcher != nil {
		err = h.dispatcher.Shutdown(ctx)
	}
	return errors.Join(err, h.ShutdownMenuCleanup(ctx))
}

func (h *BotHandler) dispatchUpdates(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, b *bot.Bot, update *models.Update) {
		if update == nil {
			return
		}
		snapshot := *update
		if update.CallbackQuery != nil {
			query := *update.CallbackQuery
			snapshot.CallbackQuery = &query
		}
		if err := h.dispatcher.Submit(ctx, updateKey(&snapshot), func(executionCtx context.Context) {
			next(executionCtx, b, &snapshot)
		}); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, dispatch.ErrClosed) {
			log.Printf("enqueue Telegram update: %v", err)
		}
	}
}

func updateKey(update *models.Update) string {
	if query := update.CallbackQuery; query != nil {
		prefix, _, _ := strings.Cut(query.Data, ":")
		switch prefix {
		case "Join", "JoinBusySlot", "JoinFirstFreeslot", "LeaveFromQueue", "CompleteSelf", "CompleteSlot", "SendQueueAgain", "SwapConfirm", "SwapReject", "ActualQueue", "CloseQueue":
			if message := query.Message.Message; message != nil {
				return fmt.Sprintf("topic:%d:%d", message.Chat.ID, message.MessageThreadID)
			}
		}
		return fmt.Sprintf("user:%d", query.From.ID)
	}
	if message := update.Message; message != nil {
		parts := strings.Fields(message.Text)
		if len(parts) > 0 {
			command, _, _ := strings.Cut(parts[0], "@")
			switch command {
			case "/swap", "/queue", "/one_shot_queue":
				return fmt.Sprintf("topic:%d:%d", message.Chat.ID, message.MessageThreadID)
			}
		}
		if message.From != nil {
			return fmt.Sprintf("user:%d", message.From.ID)
		}
		return fmt.Sprintf("topic:%d:%d", message.Chat.ID, message.MessageThreadID)
	}
	return "other"
}

func (h *BotHandler) RegisterHandlers(b *bot.Bot, username string) {
	editHandler := func(next bot.HandlerFunc) bot.HandlerFunc { return h.SerializeEdit(h.requireEditor(next)) }
	configHandler := func(next bot.HandlerFunc) bot.HandlerFunc { return h.requireDeveloper(true, h.SerializeEdit(next)) }
	moderationHandler := func(next bot.HandlerFunc) bot.HandlerFunc {
		return h.SerializeEdit(h.requirePermission(access.RemoveParticipants, true, next))
	}
	registerCommand := func(name string, handler bot.HandlerFunc) {
		b.RegisterHandlerMatchFunc(func(update *models.Update) bool {
			return update != nil && update.Message != nil && commandMatches(update.Message.Text, name, username)
		}, handler)
	}
	registerCallback := func(name string, handler bot.HandlerFunc) {
		b.RegisterHandlerMatchFunc(func(update *models.Update) bool {
			return update != nil && update.CallbackQuery != nil && strings.HasPrefix(menuCallbackData(update.CallbackQuery.Data), name)
		}, handler)
	}
	registerCommand("start", h.StartHandler)
	registerCommand("chat_id", h.requirePermission(access.ViewChatID, true, h.chatIDHandler))
	registerCommand("swap", h.SwapRequest)
	registerCommand("manage_queue", moderationHandler(h.ManageQueue))
	registerCommand("one_shot_queue", h.requirePermission(access.CreateOneShot, false, h.RunOneShotQueue))
	registerCommand("queue", h.requirePermission(access.ManageQueueMessages, false, h.RecoverQueue))
	registerCommand("edit_schedule", h.SerializeEdit(h.requireEditor(h.EditScheduleReplyText)))
	registerCommand("edit_config", h.requireDeveloper(false, h.SerializeEdit(h.EditConfigReplyText)))
	registerCallback("Join:", h.JoinToPosition)
	registerCallback("JoinBusySlot:", h.JoinBusySlot)
	registerCallback("JoinFirstFreeslot:", h.JoinClosestFreeSlot)
	registerCallback("LeaveFromQueue:", h.LeaveQueue)
	registerCallback("CompleteSelf:", h.CompleteQueue)
	registerCallback("CompleteSlot:", h.CompleteQueue)
	registerCallback("SendQueueAgain:", h.requirePermission(access.ManageQueueMessages, false, h.SendQueueAgain))
	registerCallback("SwapConfirm:", h.SwapConfirm)
	registerCallback("SwapReject:", h.SwapReject)
	registerCallback("ActualQueue", h.ActualQueueInfo)
	registerCallback("CloseQueue", h.QueueClosed)
	registerCallback("ChatList", editHandler(h.ShowChatList))
	registerCallback("ChatPage:", editHandler(h.ShowChatList))
	registerCallback("SelectChat:", editHandler(h.SelectChat))
	registerCallback("AddChat", editHandler(h.AddChat))
	registerCallback("RenameChat:", editHandler(h.RenameChat))
	registerCallback("SetDefaultChat:", h.requireDeveloper(true, h.SerializeEdit(h.SetDefaultChat)))
	registerCallback("AddNewSchedule:", editHandler(h.AddNewSchedule))
	registerCallback("LeaveEditSchedule", editHandler(h.LeaveEditSchedule))
	registerCallback("Back:", editHandler(h.Back))
	registerCallback("EditSchedule:", editHandler(h.EditScheduleEntry))
	registerCallback("DeleteEntry:", editHandler(h.DeleteScheduleEntry))
	registerCallback("EditTypeOfWeek:", editHandler(h.ChangeWeekType))
	registerCallback("EditDayOfWeek:", editHandler(h.GenerateChangeWeekDayMenu))
	registerCallback("ChWeekDay:", editHandler(h.ChangeWeekDay))
	registerCallback("EditTime:", editHandler(h.EditTimeMenu))
	registerCallback("HandleTime", editHandler(h.EditTime))
	registerCallback("EditThreadID:", editHandler(h.EditThreadID))
	registerCallback("EditDescription:", editHandler(h.EditDescription))
	registerCallback("EditTitle:", editHandler(h.EditTitle))
	registerCallback("SetCurrentWeek:", editHandler(h.ToggleCurrentWeek))
	registerCallback("EditConfig:", configHandler(h.EditConfigField))
	registerCallback("ConfigBack", configHandler(h.ConfigBack))
	registerCallback("ConfigLeave", configHandler(h.LeaveEditConfig))
	registerCallback("RolesMenu", configHandler(h.rolesHandler))
	registerCallback("Role", configHandler(h.rolesHandler))
	registerCallback("ManageQueue", moderationHandler(h.ManageQueue))
	registerCallback("ModerationSelect:", moderationHandler(h.SelectModerationQueue))
	registerCallback("ModerationRemove:", moderationHandler(h.RemoveParticipant))
	registerCallback("ModerationMode:", moderationHandler(h.SetModerationMode))
	registerCallback("ModerationBlockPage:", moderationHandler(h.ModerationBlocksPage))
	registerCallback("ModerationBlocks", moderationHandler(h.ModerationBlocks))
	registerCallback("ModerationUnblock:", moderationHandler(h.UnblockParticipant))
	registerCallback("ModerationParticipants", moderationHandler(h.ModerationParticipants))
	registerCallback("ModerationBack", moderationHandler(h.ModerationBack))
	registerCallback("ModerationPage:", moderationHandler(h.ModerationPage))
	registerCallback("ModerationLeave", moderationHandler(h.LeaveModeration))
}

func commandMatches(text, name, username string) bool {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return false
	}
	command, target, qualified := strings.Cut(parts[0], "@")
	return command == "/"+name && (!qualified || strings.EqualFold(target, username))
}
