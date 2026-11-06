package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"queuebot/db"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type menuSession struct {
	ref  db.MenuCleanup
	kind string
}

type menuCallbackContextKey struct{}
type menuCallbackIdentity struct {
	ownerID    int64
	generation string
}

var errMenuClosed = errors.New("menu session is closed")

func (h *BotHandler) sendMenu(ctx context.Context, b *bot.Bot, update *models.Update, kind, text string, markup *models.InlineKeyboardMarkup) (*models.Message, error) {
	if update == nil {
		return nil, errors.New("menu requires a source message")
	}
	var callbackID string
	source := update.Message
	if query := update.CallbackQuery; query != nil && query.Message.Message != nil {
		copy := *query.Message.Message
		copy.From = &query.From
		source = &copy
		callbackID = query.ID
	}
	if source == nil || source.From == nil {
		return nil, errors.New("menu requires a source message")
	}
	ownerID := source.From.ID
	h.menusMu.Lock()
	defer h.menusMu.Unlock()
	if err := h.closeMenuLocked(ctx, b, ownerID); err != nil {
		log.Println("Не удалось удалить предыдущее служебное меню; повторим удаление")
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, fmt.Errorf("menu generation: %w", err)
	}
	generation := hex.EncodeToString(random[:])
	private := source.Chat.ID < 0 && (kind == "config" || kind == "moderation")
	var privateMarkup *models.InlineKeyboardMarkup
	params := &bot.SendMessageParams{ChatID: source.Chat.ID, MessageThreadID: source.MessageThreadID, Text: text, ReplyMarkup: markup}
	if private {
		wrapped, err := wrapMenuMarkup(markup, generation)
		if err != nil {
			return nil, err
		}
		privateMarkup = wrapped
		params.Text = "Открываем личное меню…"
		params.ReplyMarkup = nil
		params.EphemeralMessageParameters = &models.EphemeralMessageParameters{ReceiverUserID: ownerID, CallbackQueryID: callbackID}
	}
	message, err := b.SendMessage(ctx, params)
	if err != nil {
		if private {
			h.privateMenuNoticeLocked(ctx, b, source, kind)
		}
		return nil, fmt.Errorf("send service menu: %w", err)
	}
	ref := db.MenuCleanup{Generation: generation, OwnerID: ownerID, ChatID: source.Chat.ID, ThreadID: source.MessageThreadID, MessageID: message.ID, EphemeralID: message.EphemeralMessageID, Deadline: time.Now().Add(h.menuTimeout())}
	if private && (ref.EphemeralID <= 0 || ref.MessageID != 0) || !private && (ref.MessageID <= 0 || ref.EphemeralID != 0) {
		if ref.MessageID > 0 || ref.EphemeralID > 0 {
			if ref.EphemeralID > 0 {
				ref.MessageID = 0
			}
			ref.Deadline = time.Now()
			h.queueMenuCleanupLocked(ref)
			_ = h.deleteMenuLocked(ctx, b, ref)
		}
		if private {
			h.privateMenuNoticeLocked(ctx, b, source, kind)
		}
		return nil, errors.New("Telegram returned an invalid service menu reference")
	}
	// A new generation supersedes cleanup for the same Telegram identity.
	for token, pending := range h.menuCleanup {
		if sameMenuTransport(pending, ref) {
			delete(h.menuCleanup, token)
		}
	}
	if h.db != nil {
		if err := h.db.SaveMenuCleanup(ctx, ref); err != nil {
			ref.Deadline = time.Now()
			h.queueMenuCleanupLocked(ref)
			_ = h.deleteMenuLocked(ctx, b, ref)
			return nil, err
		}
	}
	if private {
		if _, err := b.EditEphemeralMessageText(ctx, &bot.EditEphemeralMessageTextParams{ChatID: ref.ChatID, ReceiverUserID: ownerID, EphemeralMessageID: ref.EphemeralID, Text: text, ReplyMarkup: privateMarkup}); err != nil {
			ref.Deadline = time.Now()
			h.queueMenuCleanupLocked(ref)
			_ = h.deleteMenuLocked(ctx, b, ref)
			h.privateMenuNoticeLocked(ctx, b, source, kind)
			return nil, fmt.Errorf("display private service menu: %w", err)
		}
	}
	if h.menus == nil {
		h.menus = make(map[int64]menuSession)
	}
	h.menus[ownerID] = menuSession{ref: ref, kind: kind}
	h.stateMu.Lock()
	if h.userState == nil {
		h.userState = make(map[int64]userState)
	}
	h.userState[ownerID] = userState{menu: kind, enteredAt: time.Now(), chatID: ref.ChatID, threadID: ref.ThreadID}
	h.stateMu.Unlock()
	h.editMu.Lock()
	if h.editMessages == nil {
		h.editMessages = make(map[int64]int)
	}
	if h.editMessageChats == nil {
		h.editMessageChats = make(map[int64]int64)
	}
	h.editMessages[ownerID] = ref.MessageID
	h.editMessageChats[ownerID] = ref.ChatID
	h.editMu.Unlock()
	return message, nil
}

func (h *BotHandler) menuTimeout() time.Duration {
	if h.settings != nil {
		return h.settings.Values().TimeForExpiredEditSes
	}
	return 4 * time.Minute
}

func wrapMenuMarkup(markup *models.InlineKeyboardMarkup, generation string) (*models.InlineKeyboardMarkup, error) {
	if markup == nil {
		return nil, nil
	}
	wrapped := &models.InlineKeyboardMarkup{InlineKeyboard: make([][]models.InlineKeyboardButton, len(markup.InlineKeyboard))}
	for index, row := range markup.InlineKeyboard {
		wrapped.InlineKeyboard[index] = append([]models.InlineKeyboardButton(nil), row...)
		for column := range wrapped.InlineKeyboard[index] {
			button := &wrapped.InlineKeyboard[index][column]
			if button.CallbackData == "" {
				continue
			}
			button.CallbackData = "m:" + generation + ":" + button.CallbackData
			if len(button.CallbackData) > 64 {
				return nil, errors.New("service menu callback exceeds Telegram limit")
			}
		}
	}
	return wrapped, nil
}

func sameMenuTransport(a, b db.MenuCleanup) bool {
	return a.OwnerID == b.OwnerID && a.ChatID == b.ChatID && a.MessageID == b.MessageID && a.EphemeralID == b.EphemeralID
}

func (h *BotHandler) privateMenuNoticeLocked(ctx context.Context, b *bot.Bot, source *models.Message, kind string) {
	command := "/edit_config"
	if kind == "moderation" {
		command = "/manage_queue"
	}
	message, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: source.Chat.ID, MessageThreadID: source.MessageThreadID, Text: "Личное меню недоступно. Откройте " + command + " в личном чате с ботом."})
	if err != nil || message.ID <= 0 {
		return
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return
	}
	ref := db.MenuCleanup{Generation: hex.EncodeToString(random[:]), OwnerID: source.From.ID, ChatID: source.Chat.ID, ThreadID: source.MessageThreadID, MessageID: message.ID, Deadline: time.Now().Add(h.menuTimeout())}
	h.queueMenuCleanupLocked(ref)
	if h.db != nil {
		if err := h.db.SaveMenuCleanup(ctx, ref); err != nil {
			_ = h.deleteMenuLocked(ctx, b, ref)
		}
	}
}

func (h *BotHandler) editMenu(ctx context.Context, b *bot.Bot, userID int64, text string, markup *models.InlineKeyboardMarkup) error {
	return h.editMenuGeneration(ctx, b, userID, "", text, markup)
}

func (h *BotHandler) MenuGeneration(userID int64) string {
	h.menusMu.Lock()
	defer h.menusMu.Unlock()
	return h.menus[userID].ref.Generation
}

func (h *BotHandler) editMenuGeneration(ctx context.Context, b *bot.Bot, userID int64, expectedGeneration, text string, markup *models.InlineKeyboardMarkup) error {
	h.menusMu.Lock()
	defer h.menusMu.Unlock()
	menu, found := h.menus[userID]
	h.stateMu.RLock()
	session, live := h.userState[userID]
	h.stateMu.RUnlock()
	if !live || time.Since(session.enteredAt) > h.menuTimeout() || expectedGeneration != "" && (!found || expectedGeneration != menu.ref.Generation) {
		return errMenuClosed
	}
	if !found {
		h.editMu.RLock()
		menu.ref = db.MenuCleanup{OwnerID: userID, ChatID: h.editMessageChats[userID], MessageID: h.editMessages[userID]}
		h.editMu.RUnlock()
		if menu.ref.MessageID <= 0 {
			return errMenuClosed
		}
	}
	var err error
	if menu.ref.EphemeralID > 0 {
		wrapped, wrapErr := wrapMenuMarkup(markup, menu.ref.Generation)
		if wrapErr != nil {
			return wrapErr
		}
		_, err = b.EditEphemeralMessageText(ctx, &bot.EditEphemeralMessageTextParams{ChatID: menu.ref.ChatID, ReceiverUserID: userID, EphemeralMessageID: menu.ref.EphemeralID, Text: text, ReplyMarkup: wrapped})
	} else {
		_, err = b.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID: menu.ref.ChatID, MessageID: menu.ref.MessageID, Text: text, ReplyMarkup: markup})
	}
	if err != nil && strings.Contains(err.Error(), "message is not modified") {
		return nil
	}
	return err
}

func (h *BotHandler) closeMenu(ctx context.Context, b *bot.Bot, userID int64) error {
	h.menusMu.Lock()
	defer h.menusMu.Unlock()
	return h.closeMenuLocked(ctx, b, userID)
}

func (h *BotHandler) closeMenuLocked(ctx context.Context, b *bot.Bot, userID int64) error {
	menu, found := h.menus[userID]
	delete(h.menus, userID)
	h.stateMu.Lock()
	delete(h.userState, userID)
	h.stateMu.Unlock()
	h.editMu.Lock()
	if !found && h.editMessages[userID] > 0 {
		menu.ref = db.MenuCleanup{Generation: fmt.Sprintf("legacy-%d-%d-%d", userID, h.editMessageChats[userID], h.editMessages[userID]), OwnerID: userID, ChatID: h.editMessageChats[userID], MessageID: h.editMessages[userID]}
		found = true
	}
	delete(h.editMessages, userID)
	delete(h.editMessageChats, userID)
	h.editMu.Unlock()
	if !found {
		return nil
	}
	menu.ref.Deadline = time.Now()
	h.queueMenuCleanupLocked(menu.ref)
	return h.deleteMenuLocked(ctx, b, menu.ref)
}

func (h *BotHandler) queueMenuCleanupLocked(ref db.MenuCleanup) {
	if h.menuCleanup == nil {
		h.menuCleanup = make(map[string]db.MenuCleanup)
	}
	h.menuCleanup[ref.Generation] = ref
}

func (h *BotHandler) deleteMenuLocked(ctx context.Context, b *bot.Bot, ref db.MenuCleanup) error {
	if h.db != nil {
		if err := h.db.SaveMenuCleanup(ctx, ref); err != nil {
			log.Println("Не удалось сохранить повторное удаление служебного меню")
		}
	}
	if b == nil {
		return errors.New("menu transport unavailable")
	}
	var err error
	if ref.EphemeralID > 0 {
		_, err = b.DeleteEphemeralMessage(ctx, &bot.DeleteEphemeralMessageParams{ChatID: ref.ChatID, ReceiverUserID: ref.OwnerID, EphemeralMessageID: ref.EphemeralID})
	} else {
		_, err = b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: ref.ChatID, MessageID: ref.MessageID})
	}
	if err != nil && !menuAlreadyDeleted(err) {
		h.scheduleMenuRetryLocked(ctx, ref, err)
		return err
	}
	if h.db != nil {
		if err := h.db.DeleteMenuCleanup(ctx, ref.Generation); err != nil {
			h.scheduleMenuRetryLocked(ctx, ref, err)
			return err
		}
	}
	delete(h.menuCleanup, ref.Generation)
	return nil
}

func (h *BotHandler) scheduleMenuRetryLocked(ctx context.Context, ref db.MenuCleanup, err error) {
	delay := 5 * time.Second
	var limited *bot.TooManyRequestsError
	if errors.As(err, &limited) {
		delay = max(delay, time.Duration(limited.RetryAfter)*time.Second)
	}
	ref.Deadline = time.Now().Add(delay)
	h.queueMenuCleanupLocked(ref)
	if h.db != nil {
		if err := h.db.SaveMenuCleanup(ctx, ref); err != nil {
			log.Println("Не удалось сохранить срок повторного удаления служебного меню")
		}
	}
}

func menuAlreadyDeleted(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "message to delete not found") || strings.Contains(text, "ephemeral message not found") || strings.Contains(text, "ephemeral message to delete not found")
}

// This decoder is only for routing; it does not authorize a callback.
func menuCallbackData(data string) string {
	if strings.HasPrefix(data, "m:") {
		_, payload, found := strings.Cut(data[2:], ":")
		if found {
			return payload
		}
	}
	return data
}

func (h *BotHandler) NormalizeMenuCallback(ctx context.Context, update *models.Update) (context.Context, bool) {
	if update == nil || update.CallbackQuery == nil || update.CallbackQuery.Message.Message == nil {
		return ctx, false
	}
	query := update.CallbackQuery
	message := query.Message.Message
	if !strings.HasPrefix(query.Data, "m:") {
		if message.EphemeralMessageID == 0 && message.ID > 0 {
			return ctx, true
		}
		return ctx, h.menuCallbackMatches(ctx, update)
	}
	generation, payload, found := strings.Cut(query.Data[2:], ":")
	if !found || len(generation) != 16 || payload == "" || len(query.Data) > 64 {
		return ctx, false
	}
	h.menusMu.Lock()
	defer h.menusMu.Unlock()
	menu, live := h.menus[query.From.ID]
	if !live || menu.ref.Generation != generation || menu.ref.EphemeralID <= 0 || message.ID != 0 || menu.ref.ChatID != message.Chat.ID || menu.ref.ThreadID != message.MessageThreadID || menu.ref.EphemeralID != message.EphemeralMessageID {
		return ctx, false
	}
	h.stateMu.RLock()
	session, live := h.userState[query.From.ID]
	h.stateMu.RUnlock()
	if !live || session.menu != menu.kind || time.Since(session.enteredAt) > h.menuTimeout() {
		return ctx, false
	}
	query.Data = payload
	return context.WithValue(ctx, menuCallbackContextKey{}, menuCallbackIdentity{ownerID: query.From.ID, generation: generation}), true
}

func (h *BotHandler) menuCallbackMatches(ctx context.Context, update *models.Update) bool {
	if update == nil || update.CallbackQuery == nil || update.CallbackQuery.Message.Message == nil {
		return false
	}
	query := update.CallbackQuery
	message := query.Message.Message
	h.menusMu.Lock()
	defer h.menusMu.Unlock()
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	session, live := h.userState[query.From.ID]
	if !live || session.chatID != message.Chat.ID || session.threadID != message.MessageThreadID || time.Since(session.enteredAt) > h.menuTimeout() {
		return false
	}
	menu, tracked := h.menus[query.From.ID]
	if tracked {
		if menu.ref.ChatID != message.Chat.ID || menu.ref.ThreadID != message.MessageThreadID || menu.ref.MessageID != message.ID || menu.ref.EphemeralID != message.EphemeralMessageID || menu.kind != session.menu {
			return false
		}
		if menu.ref.EphemeralID > 0 {
			identity, authenticated := ctx.Value(menuCallbackContextKey{}).(menuCallbackIdentity)
			if !authenticated || identity.ownerID != query.From.ID || identity.generation != menu.ref.Generation {
				return false
			}
		}
	} else {
		h.editMu.RLock()
		matches := message.ID > 0 && message.EphemeralMessageID == 0 && h.editMessages[query.From.ID] == message.ID && h.editMessageChats[query.From.ID] == message.Chat.ID
		h.editMu.RUnlock()
		if !matches {
			return false
		}
	}
	session.enteredAt = time.Now()
	h.userState[query.From.ID] = session
	return true
}

func (h *BotHandler) BootstrapMenuCleanup(ctx context.Context, b *bot.Bot) error {
	h.menusMu.Lock()
	defer h.menusMu.Unlock()
	if h.menuWorkerCancel != nil {
		return errors.New("menu cleanup already started")
	}
	if h.db != nil {
		menus, err := h.db.ListMenuCleanup(ctx)
		if err != nil {
			return err
		}
		for _, ref := range menus {
			h.queueMenuCleanupLocked(ref)
		}
	}
	h.retryMenuCleanupLocked(ctx, b)
	workerCtx, cancel := context.WithCancel(ctx)
	h.menuWorkerCancel = cancel
	h.menuWorkerDone = make(chan struct{})
	go func() {
		defer close(h.menuWorkerDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
				h.menusMu.Lock()
				h.refreshMenuDeadlinesLocked(workerCtx)
				h.retryMenuCleanupLocked(workerCtx, b)
				h.menusMu.Unlock()
			}
		}
	}()
	return nil
}

func (h *BotHandler) refreshMenuDeadlinesLocked(ctx context.Context) {
	for ownerID, menu := range h.menus {
		if ctx.Err() != nil {
			return
		}
		h.stateMu.RLock()
		session, live := h.userState[ownerID]
		h.stateMu.RUnlock()
		if !live || session.menu != menu.kind {
			continue
		}
		deadline := session.enteredAt.Add(h.menuTimeout())
		if deadline.Equal(menu.ref.Deadline) {
			continue
		}
		if h.db != nil {
			requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := h.db.TouchMenuCleanup(requestCtx, menu.ref.Generation, deadline)
			cancel()
			if err != nil {
				log.Println("Не удалось обновить срок удаления служебного меню")
				continue
			}
		}
		menu.ref.Deadline = deadline
		h.menus[ownerID] = menu
	}
}

func (h *BotHandler) retryMenuCleanupLocked(ctx context.Context, b *bot.Bot) {
	for _, ref := range h.menuCleanup {
		if ctx.Err() != nil {
			return
		}
		if time.Now().Before(ref.Deadline) {
			continue
		}
		requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_ = h.deleteMenuLocked(requestCtx, b, ref)
		cancel()
	}
}

func (h *BotHandler) ShutdownMenuCleanup(ctx context.Context) error {
	h.menusMu.Lock()
	cancel, done := h.menuWorkerCancel, h.menuWorkerDone
	h.menusMu.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	h.editActionMu.Lock()
	defer h.editActionMu.Unlock()
	h.menusMu.Lock()
	defer h.menusMu.Unlock()
	for ownerID := range h.menus {
		if err := h.closeMenuLocked(ctx, h.b, ownerID); err != nil {
			log.Println("Удаление служебного меню будет повторено при запуске")
		}
	}
	h.retryMenuCleanupLocked(ctx, h.b)
	return ctx.Err()
}
