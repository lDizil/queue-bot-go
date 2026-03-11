package main

import (
	"context"
	"fmt"
	
	h "queuebot/bot"
	c "queuebot/config"

	"github.com/go-telegram/bot"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := c.Load()
	
	if err != nil {
		fmt.Printf("Ошибка чтения переменных viper-ом: %v", err)
		return
	}

	b, err := bot.New(cfg.TelegramToken)


	b.RegisterHandler(bot.HandlerTypeMessageText, "/start", bot.MatchTypeExact, h.StartHandler)

	b.Start(ctx)
}
