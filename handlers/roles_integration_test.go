package handlers

import (
	"context"
	"errors"
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
	processTelegramUpdates(t, h, b, chatEditorCallback(h, h.developerID, "RoleLabelSkip:456"))
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

func TestAdministratorLabelsCanBeAddedRenamedAndClearedWithoutChangingAccess(t *testing.T) {
	h, b, api, roles := newRolesEditor(t)
	processTelegramUpdates(t, h, b, chatEditorMessage(h.developerID, 99, 0, "/edit_config"))
	processTelegramUpdates(t, h, b, chatEditorCallback(h, h.developerID, "RolesMenu"))
	processTelegramUpdates(t, h, b, chatEditorCallback(h, h.developerID, "RoleAdd"), chatEditorMessage(h.developerID, 99, 0, "456"))
	if !strings.Contains(lastMenuText(t, api), "подпись") {
		t.Fatal("adding an administrator did not offer a label")
	}
	processTelegramUpdates(t, h, b, chatEditorMessage(h.developerID, 99, 0, "  Маша   — староста БГГ323  "))
	admin, err := roles.Admin(t.Context(), 456)
	if err != nil || admin.Label != "Маша — староста БГГ323" {
		t.Fatalf("label not saved: %+v %v", admin, err)
	}
	if !strings.Contains(lastMenuText(t, api), admin.Label) || !strings.Contains(lastMenuText(t, api), "456") {
		t.Fatal("administrator card lost its label or stable ID")
	}
	for permission, enabled := range mustRolePermissions(t, roles, 456) {
		if enabled {
			t.Fatalf("label granted %s", permission)
		}
	}
	if err := roles.SetPermission(t.Context(), h.developerID, 456, access.CreateOneShot, true); err != nil {
		t.Fatal(err)
	}
	processTelegramUpdates(t, h, b, chatEditorCallback(h, h.developerID, "RoleLabel:456"), chatEditorMessage(h.developerID, 99, 0, "Маша — новая подпись"))
	processTelegramUpdates(t, h, b, chatEditorCallback(h, h.developerID, "RolesMenu"))
	if !strings.Contains(lastMenuMarkup(t, api), "Маша — новая подпись") {
		t.Fatal("administrator list still shows only its ID")
	}
	processTelegramUpdates(t, h, b, chatEditorCallback(h, h.developerID, "RoleSelect:456"), chatEditorCallback(h, h.developerID, "RoleLabel:456"))
	processTelegramUpdates(t, h, b, chatEditorCallback(h, h.developerID, "RoleLabelClear:456"))
	admin, err = roles.Admin(t.Context(), 456)
	if err != nil || admin.Label != "" {
		t.Fatalf("label not cleared: %+v %v", admin, err)
	}
	if allowed, err := roles.Allowed(t.Context(), 456, access.CreateOneShot); err != nil || !allowed {
		t.Fatalf("renaming changed permissions: %v %v", allowed, err)
	}
}

func TestAdministratorLabelInputCancelsOnNavigationAndOldButtonsCannotRetarget(t *testing.T) {
	h, b, _, roles := newRolesEditor(t)
	for _, id := range []int64{456, 778} {
		if err := roles.AddAdmin(t.Context(), h.developerID, id); err != nil {
			t.Fatal(err)
		}
	}
	processTelegramUpdates(t, h, b, chatEditorMessage(h.developerID, 99, 0, "/edit_config"))
	processTelegramUpdates(t, h, b, chatEditorCallback(h, h.developerID, "RolesMenu"), chatEditorCallback(h, h.developerID, "RoleSelect:456"))
	stale := chatEditorCallback(h, h.developerID, "RoleLabel:456")
	processTelegramUpdates(t, h, b, stale)
	processTelegramUpdates(t, h, b, chatEditorCallback(h, h.developerID, "RolesMenu"), chatEditorMessage(h.developerID, 99, 0, "Ordinary text"))
	processTelegramUpdates(t, h, b, chatEditorCallback(h, h.developerID, "RoleSelect:778"), stale, chatEditorMessage(h.developerID, 99, 0, "Wrong target"))
	for _, id := range []int64{456, 778} {
		admin, err := roles.Admin(t.Context(), id)
		if err != nil || admin.Label != "" {
			t.Fatalf("old input changed administrator %d: %+v %v", id, admin, err)
		}
	}
}

func mustRolePermissions(t *testing.T, roles *access.Service, id int64) map[access.Permission]bool {
	t.Helper()
	permissions, err := roles.Permissions(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return permissions
}

func TestLabelNavigationReadFailureStillCancelsPendingInput(t *testing.T) {
	for _, test := range []struct{ action, want string }{{"RoleLabelClear:456", ""}, {"RoleLabelSkip:456", "Original"}, {"RolesMenu", "Original"}} {
		t.Run(test.action, func(t *testing.T) {
			h, b, _, roles := newRolesEditor(t)
			if err := roles.AddAdmin(t.Context(), h.developerID, 456); err != nil {
				t.Fatal(err)
			}
			if err := roles.SetAdminLabel(t.Context(), h.developerID, 456, "Original"); err != nil {
				t.Fatal(err)
			}
			processTelegramUpdates(t, h, b, chatEditorMessage(h.developerID, 99, 0, "/edit_config"))
			processTelegramUpdates(t, h, b, chatEditorCallback(h, h.developerID, "RolesMenu"), chatEditorCallback(h, h.developerID, "RoleSelect:456"), chatEditorCallback(h, h.developerID, "RoleLabel:456"))
			h.access = &labelReadFailure{AccessControl: roles}
			processTelegramUpdates(t, h, b, chatEditorCallback(h, h.developerID, test.action), chatEditorMessage(h.developerID, 99, 0, "Ordinary text after navigation"))
			admin, err := roles.Admin(t.Context(), 456)
			if err != nil || admin.Label != test.want {
				t.Fatalf("read failure kept pending label input: admin=%+v err=%v", admin, err)
			}
			h.stateMu.RLock()
			state := h.userState[h.developerID].state
			h.stateMu.RUnlock()
			if state != "" {
				t.Fatalf("failed navigation retained state %q", state)
			}
		})
	}
}

type labelReadFailure struct{ AccessControl }

var _ AccessControl = (*labelReadFailure)(nil)

func (*labelReadFailure) Admin(context.Context, int64) (access.Admin, error) {
	return access.Admin{}, errors.New("injected administrator read failure")
}
func (*labelReadFailure) ListAdmins(context.Context) ([]access.Admin, error) {
	return nil, errors.New("injected administrator list failure")
}

func TestLabelDeliveryFailureDoesNotConsumeFollowingOrdinaryText(t *testing.T) {
	for _, savedCard := range []bool{false, true} {
		name := "label prompt"
		if savedCard {
			name = "saved card"
		}
		t.Run(name, func(t *testing.T) {
			h, b, api, roles := newRolesEditor(t)
			processTelegramUpdates(t, h, b, chatEditorMessage(h.developerID, 99, 0, "/edit_config"))
			processTelegramUpdates(t, h, b, chatEditorCallback(h, h.developerID, "RolesMenu"), chatEditorCallback(h, h.developerID, "RoleAdd"))
			input, want := "456", ""
			if savedCard {
				processTelegramUpdates(t, h, b, chatEditorMessage(h.developerID, 99, 0, "456"))
				input, want = "Saved label", "Saved label"
			}
			api.fail("editMessageText", 503, "Service unavailable")
			processTelegramUpdates(t, h, b, chatEditorMessage(h.developerID, 99, 0, input), chatEditorMessage(h.developerID, 99, 0, "Ordinary text"))
			admin, err := roles.Admin(t.Context(), 456)
			if err != nil || admin.Label != want {
				t.Fatalf("delivery failure consumed ordinary text: %+v %v", admin, err)
			}
			h.stateMu.RLock()
			state := h.userState[h.developerID].state
			h.stateMu.RUnlock()
			if state != "" {
				t.Fatalf("failed delivery retained pending input: %q", state)
			}
		})
	}
}
