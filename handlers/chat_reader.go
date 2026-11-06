package handlers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"queuebot/chats"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

var _ chats.Reader = (*ChatReader)(nil)

type ChatReader struct {
	bot   *bot.Bot
	botID int64
}

func NewChatReader(telegram *bot.Bot, botID int64) *ChatReader {
	return &ChatReader{bot: telegram, botID: botID}
}

func (reader *ChatReader) ReadChat(ctx context.Context, chatID int64) (chats.Info, error) {
	if reader.bot == nil || reader.botID <= 0 || chatID >= 0 {
		return chats.Info{}, errors.New("a group chat and initialized bot are required")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	chat, err := reader.bot.GetChat(ctx, &bot.GetChatParams{ChatID: chatID})
	if err != nil {
		return chats.Info{}, fmt.Errorf("read Telegram chat: %w", err)
	}
	if chat == nil || chat.ID != chatID || (chat.Type != models.ChatTypeGroup && chat.Type != models.ChatTypeSupergroup) {
		return chats.Info{}, errors.New("the selected chat must be a Telegram group")
	}
	member, err := reader.bot.GetChatMember(ctx, &bot.GetChatMemberParams{ChatID: chatID, UserID: reader.botID})
	if err != nil {
		return chats.Info{}, fmt.Errorf("read bot membership: %w", err)
	}
	if !reader.canWrite(chat, member) {
		return chats.Info{}, errors.New("the bot must be a member allowed to send text messages")
	}
	return chats.Info{Title: chat.Title}, nil
}

func (reader *ChatReader) canWrite(chat *models.ChatFullInfo, member *models.ChatMember) bool {
	if member == nil {
		return false
	}
	switch member.Type {
	case models.ChatMemberTypeOwner:
		return member.Owner != nil && member.Owner.User != nil && member.Owner.User.ID == reader.botID
	case models.ChatMemberTypeAdministrator:
		return member.Administrator != nil && member.Administrator.User.ID == reader.botID
	case models.ChatMemberTypeMember:
		return member.Member != nil && member.Member.User != nil && member.Member.User.ID == reader.botID && (chat.Permissions == nil || chat.Permissions.CanSendMessages)
	case models.ChatMemberTypeRestricted:
		return member.Restricted != nil && member.Restricted.User != nil && member.Restricted.User.ID == reader.botID && member.Restricted.IsMember && member.Restricted.CanSendMessages
	default:
		return false
	}
}
