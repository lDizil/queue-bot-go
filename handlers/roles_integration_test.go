package handlers

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"queuebot/access"
)

func TestRolesEditorStartsDeniedAndTogglesOnlySelectedAdministrator(t *testing.T) {
	h, b, _, roles := newRolesEditor(t)
	processTelegramUpdates(t, h, b, chatEditorMessage(h.developerID, 99, 0, "/edit_config"))
	processTelegramUpdates(t, h, b, chatEditorCallback(h, h.developerID, "RolesMenu"))
	processTelegramUpdates(t, h, b, chatEditorCallback(h, h.developerID, "RoleAdd"), chatEditorMessage(h.developerID, 99, 0, "456"))
	permissions, err := roles.Permissions(t.Context(), 456)
	if err != nil {
		t.Fatal(err)
	}
	for permission, enabled := range permissions {
		if enabled {
			t.Fatalf("new administrator inherited %s", permission)
		}
	}
	index := permissionIndex(t, access.CreateOneShot)
	toggle := chatEditorCallback(h, h.developerID, fmt.Sprintf("RolePerm:456:%d:true", index))
	processTelegramUpdates(t, h, b, toggle)
	if allowed, err := roles.Allowed(t.Context(), 456, access.CreateOneShot); err != nil || !allowed {
		t.Fatalf("one-shot permission was not enabled: %v %v", allowed, err)
	}
	if err := roles.AddAdmin(t.Context(), h.developerID, 778); err != nil {
		t.Fatal(err)
	}
	processTelegramUpdates(t, h, b, chatEditorCallback(h, h.developerID, "RoleSelect:778"), toggle)
	if allowed, err := roles.Allowed(t.Context(), 778, access.CreateOneShot); err != nil || allowed {
		t.Fatalf("old button changed another administrator: %v %v", allowed, err)
	}
	processTelegramUpdates(t, h, b, chatEditorCallback(h, h.developerID, "RoleRemove:456"))
	if admin, err := roles.IsAdmin(t.Context(), 456); err != nil || !admin {
		t.Fatalf("old remove button deleted another selected card: %v %v", admin, err)
	}
	processTelegramUpdates(t, h, b, chatEditorCallback(h, h.developerID, "RoleSelect:456"), chatEditorCallback(h, h.developerID, "RoleRemove:456"))
	if admin, err := roles.IsAdmin(t.Context(), 456); err != nil || admin {
		t.Fatalf("administrator was not removed: %v %v", admin, err)
	}
}

func TestGranularCommandPermissionsAndRevocationApplyWithoutRestart(t *testing.T) {
	h, b, api, roles := newRolesEditor(t)
	scheduler := h.scheduler.(*chatTestScheduler)
	if err := roles.AddAdmin(t.Context(), h.developerID, 456); err != nil {
		t.Fatal(err)
	}
	before := len(api.history())
	processTelegramUpdates(t, h, b, chatEditorMessage(456, 88, 0, "/chat_id"))
	if len(api.history()) != before {
		t.Fatal("unauthorized chat_id produced a response")
	}
	processTelegramUpdates(t, h, b, chatEditorMessage(456, 88, 0, "/one_shot_queue denied"))
	if len(scheduler.instantCalls()) != 0 {
		t.Fatal("admin role bypassed disabled one-shot permission")
	}
	if err := roles.SetPermission(t.Context(), h.developerID, 456, access.CreateOneShot, true); err != nil {
		t.Fatal(err)
	}
	processTelegramUpdates(t, h, b, chatEditorMessage(456, -900, 7, "/one_shot_queue allowed"))
	if len(scheduler.instantCalls()) != 1 {
		t.Fatal("enabled one-shot permission was not applied")
	}
	processTelegramUpdates(t, h, b, chatEditorMessage(456, 88, 0, "/edit_schedule"), chatEditorMessage(456, 88, 0, "/edit_config"))
	h.stateMu.RLock()
	_, sessionExists := h.userState[456]
	h.stateMu.RUnlock()
	if sessionExists {
		t.Fatal("narrowly authorized administrator opened another editor")
	}
	if err := roles.SetPermission(t.Context(), h.developerID, 456, access.CreateOneShot, false); err != nil {
		t.Fatal(err)
	}
	processTelegramUpdates(t, h, b, chatEditorMessage(456, -900, 7, "/one_shot_queue denied again"))
	if len(scheduler.instantCalls()) != 1 {
		t.Fatal("revoked permission remained usable")
	}
	if err := roles.SetPermission(t.Context(), h.developerID, 456, access.ViewChatID, true); err != nil {
		t.Fatal(err)
	}
	processTelegramUpdates(t, h, b, chatEditorMessage(456, -900, 7, "/chat_id"))
	if !strings.Contains(lastMenuText(t, api), "<code>-900</code>") {
		t.Fatal("authorized chat_id did not show the source chat")
	}
}

func TestRolesTabInGroupUsesPrivateCallbacksAndCannotBeUsedByAnotherUser(t *testing.T) {
	h, b, api, roles := newRolesEditor(t)
	if err := roles.AddAdmin(t.Context(), h.developerID, 456); err != nil {
		t.Fatal(err)
	}
	processTelegramUpdates(t, h, b, chatEditorMessage(h.developerID, -100, 7, "/edit_config"))
	callback := trackedRoleCallback(h, h.developerID, "RolesMenu")
	processTelegramUpdates(t, h, b, callback)
	selectCard := trackedRoleCallback(h, h.developerID, "RoleSelect:456")
	processTelegramUpdates(t, h, b, selectCard)
	index := permissionIndex(t, access.RemoveParticipants)
	forged := trackedRoleCallback(h, h.developerID, fmt.Sprintf("RolePerm:456:%d:true", index))
	forged.CallbackQuery.From.ID = 456
	processTelegramUpdates(t, h, b, forged)
	if allowed, err := roles.Allowed(t.Context(), 456, access.RemoveParticipants); err != nil || allowed {
		t.Fatalf("another administrator changed roles: %v %v", allowed, err)
	}
	processTelegramUpdates(t, h, b, trackedRoleCallback(h, h.developerID, fmt.Sprintf("RolePerm:456:%d:true", index)))
	if allowed, err := roles.Allowed(t.Context(), 456, access.RemoveParticipants); err != nil || !allowed {
		t.Fatalf("developer private callback failed: %v %v", allowed, err)
	}
	for _, request := range api.history() {
		if request.method == "editMessageText" {
			t.Fatal("group role settings were edited publicly")
		}
		if request.method == "editEphemeralMessageText" && request.values["receiver_user_id"] != strconv.FormatInt(h.developerID, 10) {
			t.Fatal("private role menu changed receiver")
		}
	}
}

func newRolesEditor(t *testing.T) (*BotHandler, *bot.Bot, *telegramAdapterAPI, *access.Service) {
	t.Helper()
	h, b, api, _ := newChatEditor(t)
	roles := access.NewService(h.db, h.developerID)
	if err := roles.Initialize(t.Context(), []int64{123}); err != nil {
		t.Fatal(err)
	}
	h.access = roles
	return h, b, api, roles
}

func permissionIndex(t *testing.T, permission access.Permission) int {
	t.Helper()
	for index, value := range access.AllPermissions() {
		if value == permission {
			return index
		}
	}
	t.Fatal("unknown test permission")
	return -1
}

func trackedRoleCallback(h *BotHandler, userID int64, data string) *models.Update {
	h.menusMu.Lock()
	menu := h.menus[userID]
	h.menusMu.Unlock()
	ref := menu.ref
	message := &models.Message{ID: ref.MessageID, EphemeralMessageID: ref.EphemeralID, Chat: models.Chat{ID: ref.ChatID}, MessageThreadID: ref.ThreadID}
	if ref.EphemeralID > 0 {
		data = "m:" + ref.Generation + ":" + data
	}
	return &models.Update{CallbackQuery: &models.CallbackQuery{ID: data, From: models.User{ID: userID}, Data: data, Message: models.MaybeInaccessibleMessage{Message: message}}}
}
