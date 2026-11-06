package db

import (
	"testing"
	"time"
)

func TestMenuCleanupReferencesSurviveRepositoryRestart(t *testing.T) {
	repo, _ := newChatTestRepository(t, 11)
	ctx := t.Context()
	deadline := time.Now().UTC().Truncate(time.Microsecond).Add(time.Minute)
	menus := []MenuCleanup{
		{Generation: "normal", OwnerID: 10, ChatID: -100, ThreadID: 7, MessageID: 501, Deadline: deadline},
		{Generation: "ephemeral", OwnerID: 20, ChatID: -100, ThreadID: 8, EphemeralID: 17, Deadline: deadline.Add(time.Minute)},
	}
	for _, menu := range menus {
		if err := repo.SaveMenuCleanup(ctx, menu); err != nil {
			t.Fatal(err)
		}
	}
	restarted := NewDBRepo(repo.pool)
	stored, err := restarted.ListMenuCleanup(ctx)
	if err != nil || len(stored) != len(menus) {
		t.Fatalf("cleanup references did not survive restart: got=%+v want=%+v err=%v", stored, menus, err)
	}
	for index, got := range stored {
		want := menus[index]
		if !got.Deadline.Equal(want.Deadline) {
			t.Fatalf("cleanup deadline changed: got=%s want=%s", got.Deadline, want.Deadline)
		}
		got.Deadline, want.Deadline = time.Time{}, time.Time{}
		if got != want {
			t.Fatalf("cleanup transport changed: got=%+v want=%+v", got, want)
		}
	}
	if err := restarted.DeleteMenuCleanup(ctx, "normal"); err != nil {
		t.Fatal(err)
	}
	stored, err = repo.ListMenuCleanup(ctx)
	if err != nil || len(stored) != 1 || stored[0].Generation != "ephemeral" {
		t.Fatalf("cleanup removed unrelated message: %+v err=%v", stored, err)
	}
}

func TestMenuCleanupReusedEphemeralIDReplacesOnlyItsOwnerAndGeneration(t *testing.T) {
	repo, _ := newChatTestRepository(t, 11)
	ctx := t.Context()
	old := MenuCleanup{Generation: "old", OwnerID: 10, ChatID: -100, ThreadID: 7, EphemeralID: 17, Deadline: time.Now()}
	otherOwner := old
	otherOwner.Generation, otherOwner.OwnerID = "other-owner", 20
	otherChat := old
	otherChat.Generation, otherChat.ChatID = "other-chat", -200
	for _, menu := range []MenuCleanup{old, otherOwner, otherChat} {
		if err := repo.SaveMenuCleanup(ctx, menu); err != nil {
			t.Fatal(err)
		}
	}
	current := old
	current.Generation, current.ThreadID = "current", 9
	if err := repo.SaveMenuCleanup(ctx, current); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteMenuCleanup(ctx, old.Generation); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.ListMenuCleanup(ctx)
	if err != nil || len(stored) != 3 {
		t.Fatalf("reused ID lost another cleanup identity: %+v err=%v", stored, err)
	}
	found := make(map[string]MenuCleanup)
	for _, ref := range stored {
		found[ref.Generation] = ref
	}
	if _, exists := found[old.Generation]; exists || found[current.Generation].ThreadID != 9 || found[otherOwner.Generation].OwnerID != 20 || found[otherChat.Generation].ChatID != -200 {
		t.Fatalf("old generation affected the reused ID or another receiver: %+v", stored)
	}
}
