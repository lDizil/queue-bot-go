package main

import (
	"context"
	"fmt"
	"log"

	h "queuebot/bot"
	c "queuebot/config"
	db "queuebot/db"

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

	databaseUrl := fmt.Sprintf("postgresql://%s:%s@%s:%s/%s?sslmode=disable", cfg.DBUser, cfg.DBPass, cfg.DBHost, cfg.DBPort, cfg.DBName)
	
	pool, err := db.SetUpDBConn(databaseUrl)
	if err != nil {
		log.Fatal("Ошибка подключения к БД:", err)
	}

	err = db.RunMigrations(databaseUrl)
	if err != nil {
		log.Fatal("Ошибка миграций:", err)
	}
	
	database := db.NewDBRepo(pool)
	handlers := h.NewBotHandler(database)

	b, err := bot.New(cfg.TelegramToken)

	if err != nil {
		log.Fatal("Ошибка при создании бота:", err)
	}

	b.RegisterHandler(bot.HandlerTypeMessageText, "/start", bot.MatchTypeExact, handlers.StartHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/queue", bot.MatchTypeExact, handlers.SendQueueMessage)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "Join:", bot.MatchTypePrefix, handlers.JoinQueue)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "LeaveFromQueue", bot.MatchTypePrefix, handlers.LeaveQueue)

	log.Println("Бот запущен и готов к работе")
	
	b.Start(ctx)
}
