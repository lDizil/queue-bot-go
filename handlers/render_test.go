package handlers

import (
	"strings"
	"testing"

	"queuebot/db"
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
			if btn.Text == "❌" {
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
			if !strings.Contains(btn.Text, "Занять") &&
				!strings.Contains(btn.Text, "Выйти") &&
				!strings.Contains(btn.Text, "Отправить") {
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
			if btn.Text == "❌" {
				crossCount++
			}
		}
	}
	if crossCount != 3 {
		t.Errorf("ожидалось 3 кнопки ❌, получили %d", crossCount)
	}
}
