package main

import (
	"context"
	"fmt"
	"log"

	h "queuebot/handlers"
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
	handlers := h.NewBotHandler(database, cfg.TotalSlotsInQueue, cfg.AmountOfButtonsInRow, cfg.Delay)

	b, err := bot.New(cfg.TelegramToken)

	if err != nil {
		log.Fatal("Ошибка при создании бота:", err)
	}

	b.RegisterHandler(bot.HandlerTypeMessageText, "/start", bot.MatchTypeExact, handlers.StartHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/queue", bot.MatchTypeExact, handlers.SendQueueMessage)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "Join:", bot.MatchTypePrefix, handlers.JoinToPosition)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "JoinBusySlot:", bot.MatchTypePrefix, handlers.JoinBusySlot)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "JoinFirstFreeslot", bot.MatchTypePrefix, handlers.JoinClosestFreeSlot)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "LeaveFromQueue", bot.MatchTypePrefix, handlers.LeaveQueue)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "SendQueueAgain", bot.MatchTypePrefix, handlers.SendQueueAgain)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "ActualQueue", bot.MatchTypeExact, handlers.ActualQueueInfo)
	
	b.RegisterHandler(bot.HandlerTypeMessageText, "/edit_schedule", bot.MatchTypeExact, handlers.EditScheduleReplyText)


	log.Println("Бот запущен и готов к работе")
	
	b.Start(ctx)
}
