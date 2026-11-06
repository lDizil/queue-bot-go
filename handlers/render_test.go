package handlers

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf16"

	"queuebot/db"
	"queuebot/queue"
	u "queuebot/utils"
)

func newTestHandler(totalSlots, slotsInRow int) *BotHandler {
	return &BotHandler{
		totalSlotsInQueue:  totalSlots,
		amountOfSlotsInRow: slotsInRow,
	}
}

// RenderQueueMessage статусы
func TestRenderQueueMessage_Pending(t *testing.T) {
	h := newTestHandler(5, 5)
	text, markup := h.RenderQueueMessage(nil, 1, u.QueuePending)

	if !strings.Contains(text, "🔴") {
		t.Error("QueuePending: ожидался 🔴 в тексте")
	}
	if markup == nil || len(markup.InlineKeyboard) == 0 {
		t.Error("QueuePending: ожидалась клавиатура с кнопками")
	}
}

func TestRenderQueueMessage_Open(t *testing.T) {
	h := newTestHandler(5, 5)
	text, markup := h.RenderQueueMessage(nil, 1, u.QueueOpen)

	if !strings.Contains(text, "🟢") {
		t.Error("QueueOpen: ожидался 🟢 в тексте")
	}
	if markup == nil || len(markup.InlineKeyboard) == 0 {
		t.Error("QueueOpen: ожидалась клавиатура")
	}
}

func TestRenderQueueMessage_Closed(t *testing.T) {
	h := newTestHandler(5, 5)
	text, markup := h.RenderQueueMessage(nil, 1, u.QueueClosed)

	if !strings.Contains(text, "🔴") {
		t.Error("QueueClosed: ожидался 🔴 в тексте")
	}
	totalBtns := 0
	for _, row := range markup.InlineKeyboard {
		totalBtns += len(row)
	}
	if totalBtns != 1 {
		t.Errorf("QueueClosed: ожидалась 1 кнопка, получили %d", totalBtns)
	}
}

// RenderQueueMessage полный цикл
func TestRenderQueueMessage_FullCycle(t *testing.T) {
	h := newTestHandler(5, 5)
	scheduleID := 42

	text, _ := h.RenderQueueMessage(nil, scheduleID, u.QueuePending)
	if !strings.Contains(text, "Очередь пуста") {
		t.Error("шаг 1: ожидался текст 'Очередь пуста'")
	}
	if !strings.Contains(text, "🔴") {
		t.Error("шаг 1: ожидался статус 🔴")
	}

	queue := []db.QueueEntry{
		{UserID: 101, Username: "alice", ScheduleID: scheduleID, Position: 1},
	}
	text, markup := h.RenderQueueMessage(queue, scheduleID, u.QueueOpen)
	if !strings.Contains(text, "@alice") {
		t.Error("шаг 2: ожидался @alice в очереди")
	}
	if !strings.Contains(text, "🟢") {
		t.Error("шаг 2: ожидался статус 🟢")
	}
	foundCross := false
	for _, row := range markup.InlineKeyboard {
		for _, btn := range row {
			if btn.Text == "1 ⏳" {
				foundCross = true
			}
		}
	}
	if !foundCross {
		t.Error("шаг 2: ожидалась кнопка ❌ для занятого места")
	}

	queue = append(queue, db.QueueEntry{
		UserID: 102, Username: "bob", ScheduleID: scheduleID, Position: 3,
	})
	text, _ = h.RenderQueueMessage(queue, scheduleID, u.QueueOpen)
	if !strings.Contains(text, "@alice") || !strings.Contains(text, "@bob") {
		t.Error("шаг 3: ожидались @alice и @bob в очереди")
	}

	queue = queue[1:]
	text, _ = h.RenderQueueMessage(queue, scheduleID, u.QueueOpen)
	if strings.Contains(text, "@alice") {
		t.Error("шаг 4: @alice должна была выйти из очереди")
	}
	if !strings.Contains(text, "@bob") {
		t.Error("шаг 4: @bob должен остаться в очереди")
	}

	text, markup = h.RenderQueueMessage(nil, scheduleID, u.QueueClosed)
	if !strings.Contains(text, "🔴") {
		t.Error("шаг 5: ожидался статус 🔴")
	}
	totalBtns := 0
	for _, row := range markup.InlineKeyboard {
		totalBtns += len(row)
	}
	if totalBtns != 1 {
		t.Errorf("шаг 5: ожидалась 1 кнопка, получили %d", totalBtns)
	}
}

// RenderQueueMessage слоты

func TestRenderQueueMessage_SlotButtons(t *testing.T) {
	h := newTestHandler(10, 5)
	_, markup := h.RenderQueueMessage(nil, 1, u.QueueOpen)

	slotBtns := 0
	for _, row := range markup.InlineKeyboard {
		for _, btn := range row {
			if strings.HasPrefix(btn.CallbackData, "Join:") || strings.HasPrefix(btn.CallbackData, "CompleteSlot:") {
				slotBtns++
			}
		}
	}
	if slotBtns != 10 {
		t.Errorf("ожидалось 10 кнопок слотов, получили %d", slotBtns)
	}
}

func TestRenderQueueMessage_AllSlotsTaken(t *testing.T) {
	h := newTestHandler(3, 3)
	queue := []db.QueueEntry{
		{UserID: 1, Username: "a", ScheduleID: 1, Position: 1},
		{UserID: 2, Username: "b", ScheduleID: 1, Position: 2},
		{UserID: 3, Username: "c", ScheduleID: 1, Position: 3},
	}
	_, markup := h.RenderQueueMessage(queue, 1, u.QueueOpen)

	crossCount := 0
	for _, row := range markup.InlineKeyboard {
		for _, btn := range row {
			if strings.HasPrefix(btn.CallbackData, "CompleteSlot:") {
				crossCount++
			}
		}
	}
	if crossCount != 3 {
		t.Errorf("ожидалось 3 кнопки ❌, получили %d", crossCount)
	}
}

func TestRenderQueue_TitleAndCompletion(t *testing.T) {
	h := newTestHandler(5, 5)
	text, markup := h.renderQueue(queue.Snapshot{Schedule: db.Schedule{ID: 7, Title: "БГГ323 — Высоконагруженные приложения", ActiveCapacity: 5, ActiveColumns: 5}, Entries: []db.QueueEntry{{ID: 101, Position: 2, Username: "bob", Completed: true}}, Status: u.QueueOpen})
	if !strings.HasPrefix(text, "БГГ323 — Высоконагруженные приложения\n\nЭто сообщение") {
		t.Fatalf("unexpected heading: %q", text)
	}
	if !strings.Contains(text, "2. @bob ✅\n") {
		t.Fatalf("completion must follow name: %q", text)
	}
	button := markup.InlineKeyboard[0][1]
	if button.Text != "2 ✅" || button.CallbackData != "CompleteSlot:7:101:2:false" {
		t.Fatalf("completion button must carry participant identity and target state: %+v", button)
	}
}

func TestRenderQueue_TelegramLengthLimit(t *testing.T) {
	h := newTestHandler(90, 8)
	for _, username := range []bool{false, true} {
		var entries []db.QueueEntry
		for i := 1; i <= 90; i++ {
			entry := db.QueueEntry{ID: i, Position: i, DisplayName: strings.Repeat("😀", 128), Completed: true}
			if username {
				entry.Username = strings.Repeat("a", 32)
			}
			entries = append(entries, entry)
		}
		text, markup := h.renderQueue(queue.Snapshot{Schedule: db.Schedule{ID: 7, Title: strings.Repeat("😀", 200), ActiveCapacity: 90, ActiveColumns: 8}, Entries: entries, Status: u.QueueClosed})
		if length := len(utf16.Encode([]rune(text))); length > 4096 {
			t.Fatalf("message exceeds Telegram limit: %d", length)
		}
		for _, entry := range entries {
			if !strings.Contains(text, fmt.Sprintf("%d. ", entry.Position)) {
				t.Fatalf("missing participant %d", entry.Position)
			}
		}
		for _, row := range markup.InlineKeyboard {
			for _, button := range row {
				if len(button.CallbackData) > 64 {
					t.Fatalf("callback exceeds Telegram limit: %q", button.CallbackData)
				}
			}
		}
	}
}
