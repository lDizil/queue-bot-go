package main

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	c "queuebot/config"
	db "queuebot/db"
	h "queuebot/handlers"
	"queuebot/middleware"
	s "queuebot/scheduler"

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

	week1Date, err := time.Parse("2006-01-02", cfg.Week1Date)
	if err != nil {
		log.Fatal("Ошибка парсинга даты первой недели:", err)
	}

	database := db.NewDBRepo(pool)
	handlers := h.NewBotHandler(database, cfg.TotalSlotsInQueue, cfg.AmountOfButtonsInRow, cfg.DelayUpdateQueue, cfg.TimeForExpiredEditSes, week1Date, cfg.Week1Type)

	b, err := bot.New(cfg.TelegramToken, bot.WithInitialOffset(-1), bot.WithDefaultHandler(handlers.StateHandler))

	if err != nil {
		log.Fatal("Ошибка при создании бота:", err)
	}

	handlers.SetBot(b)

	sched := s.NewScheduler(database, b, handlers, int64(cfg.ChatId), week1Date, cfg.Week1Type, cfg.SchedulerTickInterval)

	schedules, err := database.GetAllSchedulesWithNotifications(ctx)
	if err != nil {
		log.Fatal("Ошибка загрузки расписаний:", err)
	}

	handlers.SetScheduler(sched)

	for _, schedule := range schedules {
		if schedule.QueueMesID != nil {
			handlers.SetQueueMessage(schedule.ID, *schedule.QueueMesID)
		}
		sched.ScheduleNext(ctx, schedule)
	}

	adminIDStrs := strings.Split(cfg.AdminsID, ",")
	adminIDs := make([]int64, 0, len(adminIDStrs))
	for _, s := range adminIDStrs {
		id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			log.Fatalf("Неверный формат ID администратора: %q", s)
		}
		adminIDs = append(adminIDs, id)
	}

	adminMw := middleware.AdminOnly(adminIDs)
	editSesMw := middleware.EditSession(handlers)
	queueOpenMw := middleware.QueueOpen(handlers)

	editHandler := func(h bot.HandlerFunc) bot.HandlerFunc {
		return middleware.Chain(h, adminMw, editSesMw)
	}
	queueHandler := func(h bot.HandlerFunc) bot.HandlerFunc {
		return middleware.Chain(h, queueOpenMw)
	}
	queueHandlerAdmin := func(h bot.HandlerFunc) bot.HandlerFunc {
		return middleware.Chain(h, queueOpenMw, adminMw)
	}
	adminHandler := func(h bot.HandlerFunc) bot.HandlerFunc {
		return middleware.Chain(h, adminMw)
	}

	b.RegisterHandler(bot.HandlerTypeMessageText, "/start", bot.MatchTypeExact, handlers.StartHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "/queue", bot.MatchTypeExact, adminHandler(handlers.SendQueueMessage))
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "Join:", bot.MatchTypePrefix, queueHandler(handlers.JoinToPosition))
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "JoinBusySlot:", bot.MatchTypePrefix, queueHandler(handlers.JoinBusySlot))
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "JoinFirstFreeslot:", bot.MatchTypePrefix, queueHandler(handlers.JoinClosestFreeSlot))
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "LeaveFromQueue:", bot.MatchTypePrefix, queueHandler(handlers.LeaveQueue))
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "SendQueueAgain:", bot.MatchTypePrefix, queueHandlerAdmin(handlers.SendQueueAgain))
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "ActualQueue", bot.MatchTypeExact, handlers.ActualQueueInfo)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "CloseQueue", bot.MatchTypeExact, handlers.QueueClosed)

	b.RegisterHandler(bot.HandlerTypeMessageText, "/edit_schedule", bot.MatchTypeExact, adminHandler(handlers.EditScheduleReplyText))
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "AddNewSchedule", bot.MatchTypeExact, editHandler(handlers.AddNewSchedule))
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "LeaveEditSchedule", bot.MatchTypeExact, editHandler(handlers.LeaveEditSchedule))
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "Back:", bot.MatchTypePrefix, editHandler(handlers.Back))
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "EditSchedule:", bot.MatchTypePrefix, editHandler(handlers.EditScheduleEntry))
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "DeleteEntry:", bot.MatchTypePrefix, editHandler(handlers.DeleteScheduleEntry))
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "EditTypeOfWeek:", bot.MatchTypePrefix, editHandler(handlers.ChangeWeekType))
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "EditDayOfWeek:", bot.MatchTypePrefix, editHandler(handlers.GenerateChangeWeekDayMenu))
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "ChWeekDay:", bot.MatchTypePrefix, editHandler(handlers.ChangeWeekDay))
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "EditTime:", bot.MatchTypePrefix, editHandler(handlers.EditTimeMenu))
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "HandleTime", bot.MatchTypePrefix, editHandler(handlers.EditTime))
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "EditThreadID:", bot.MatchTypePrefix, editHandler(handlers.EditThreadID))
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "EditDescription:", bot.MatchTypePrefix, editHandler(handlers.EditDescription))

	log.Println("Бот запущен и готов к работе")

	b.Start(ctx)
}
