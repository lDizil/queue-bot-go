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
	handlers := h.NewBotHandler(database, cfg.TotalSlotsInQueue, cfg.AmountOfButtonsInRow, cfg.DelayUpdateQueue, cfg.TimeForExpiredEditSes)

	b, err := bot.New(cfg.TelegramToken,  bot.WithInitialOffset(-1), bot.WithDefaultHandler(handlers.StateHandler))

	if err != nil {
		log.Fatal("Ошибка при создании бота:", err)
	}
	
	handlers.SetBot(b)

	b.RegisterHandler(bot.HandlerTypeMessageText, "/start", bot.MatchTypeExact, handlers.StartHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/queue", bot.MatchTypeExact, handlers.SendQueueMessage)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "Join:", bot.MatchTypePrefix, handlers.JoinToPosition)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "JoinBusySlot:", bot.MatchTypePrefix, handlers.JoinBusySlot)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "JoinFirstFreeslot", bot.MatchTypeExact, handlers.JoinClosestFreeSlot)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "LeaveFromQueue", bot.MatchTypeExact, handlers.LeaveQueue)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "SendQueueAgain", bot.MatchTypeExact, handlers.SendQueueAgain)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "ActualQueue", bot.MatchTypeExact, handlers.ActualQueueInfo)
	
	b.RegisterHandler(bot.HandlerTypeMessageText, "/edit_schedule", bot.MatchTypeExact, handlers.EditScheduleReplyText)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "AddNewSchedule", bot.MatchTypeExact, handlers.AddNewSchedule)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "LeaveEditSchedule", bot.MatchTypeExact, handlers.LeaveEditSchedule)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "Back:", bot.MatchTypePrefix, handlers.Back)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "EditSchedule:", bot.MatchTypePrefix, handlers.EditScheduleEntry)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "DeleteEntry:", bot.MatchTypePrefix, handlers.DeleteScheduleEntry)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "EditTypeOfWeek:", bot.MatchTypePrefix, handlers.ChangeWeekType)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "EditDayOfWeek:", bot.MatchTypePrefix, handlers.GenerateChangeWeekDayMenu)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "ChWeekDay:", bot.MatchTypePrefix, handlers.ChangeWeekDay)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "EditTime:", bot.MatchTypePrefix, handlers.EditTimeMenu)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "HandleTime", bot.MatchTypePrefix, handlers.EditTime)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "EditThreadID:", bot.MatchTypePrefix, handlers.EditThreadID)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "EditDescription:", bot.MatchTypePrefix, handlers.EditDescription)

	
	log.Println("Бот запущен и готов к работе")
	
	b.Start(ctx)
}
