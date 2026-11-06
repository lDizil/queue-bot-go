package handlers

import (
	"strings"
	"testing"

	"queuebot/queue"
)

func TestModerationSDKDeniedSharedButtonShowsAlertWithoutChatMessages(t *testing.T) {
	h, telegram, api, _ := newTelegramAdapter(t)
	const userID = int64(404)
	for _, data := range []string{"ManageQueue", "ManageQueue:7"} {
		update := sharedModerationCallback(userID, queue.Reference{ScheduleID: 7, ChatID: -100, MessageID: 500})
		update.CallbackQuery.Data = data
		processTelegramUpdates(t, h, telegram, update)
	}
	processTelegramUpdates(t, h, telegram, chatEditorMessage(userID, -100, 7, "/manage_queue"))
	requests := api.history()
	if len(requests) != 2 {
		t.Fatalf("denial added chat messages or failed to answer both buttons: %+v", requests)
	}
	for _, request := range requests {
		if request.method != "answerCallbackQuery" || request.values["show_alert"] != "true" || !strings.Contains(request.values["text"], "нет доступа к управлению участниками") {
			t.Fatalf("shared button did not show a private denial alert: %+v", request)
		}
	}
	h.stateMu.RLock()
	defer h.stateMu.RUnlock()
	if len(h.userState) != 0 {
		t.Fatal("denied button opened a management session")
	}
}
