package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"queuebot/access"
	"queuebot/chats"
	"queuebot/config"
	"queuebot/db"
	"queuebot/handlers"
	"queuebot/queue"
	"queuebot/scheduler"
	"queuebot/settings"

	"github.com/go-telegram/bot"
)

const readinessPath = "/tmp/queuebot-ready"

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	if err := os.Remove(readinessPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear previous readiness state: %w", err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	databaseURL := url.URL{Scheme: "postgres", User: url.UserPassword(cfg.DBUser, cfg.DBPass), Host: net.JoinHostPort(cfg.DBHost, cfg.DBPort), Path: "/" + cfg.DBName, RawQuery: "sslmode=disable"}
	pool, err := db.SetUpDBConn(databaseURL.String())
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()
	if err := db.RunMigrations(databaseURL.String()); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	repository := db.NewDBRepo(pool)
	if err := repository.BackfillChatIDs(ctx, int64(cfg.ChatId)); err != nil {
		return fmt.Errorf("initialize schedule chats: %w", err)
	}
	manager, err := settings.NewManager(ctx, repository, cfg.Settings)
	if err != nil {
		return fmt.Errorf("load runtime settings: %w", err)
	}
	adminIDs, err := parseAdminIDs(cfg.AdminsID, cfg.DeveloperUserID)
	if err != nil {
		return err
	}
	permissions := access.NewService(repository, cfg.DeveloperUserID)
	if err := permissions.Initialize(ctx, adminIDs); err != nil {
		return fmt.Errorf("initialize access: %w", err)
	}
	h := handlers.NewBotHandler(ctx, repository, manager, cfg.DeveloperUserID, permissions)
	b, err := h.NewTelegramBot(cfg.TelegramToken, bot.WithSkipGetMe(), bot.WithHTTPClient(30*time.Second, &http.Client{Timeout: 40 * time.Second}))
	if err != nil {
		return fmt.Errorf("initialize Telegram bot: %w", err)
	}
	var queues *queue.Service
	defer func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()
		if err := h.Shutdown(shutdownCtx); err != nil {
			log.Printf("drain Telegram updates: %v", err)
		}
		if queues != nil {
			queues.Stop()
		}
	}()
	initCtx, initCancel := context.WithTimeout(ctx, 10*time.Second)
	me, err := b.GetMe(initCtx)
	initCancel()
	if err != nil {
		return fmt.Errorf("initialize Telegram bot: %w", err)
	}
	h.SetBot(b)
	chatService := chats.NewService(repository, handlers.NewChatReader(b, me.ID))
	if err := chatService.Initialize(ctx, int64(cfg.ChatId)); err != nil {
		return fmt.Errorf("initialize chats: %w", err)
	}
	h.SetChatService(chatService)
	if err := h.BootstrapMenuCleanup(ctx, b); err != nil {
		return fmt.Errorf("initialize service menus: %w", err)
	}
	queues = queue.NewService(context.WithoutCancel(ctx), repository, manager, h)
	h.SetQueueService(queues)
	sched := scheduler.NewScheduler(ctx, repository, queues, manager)
	h.SetScheduler(sched)
	h.RegisterHandlers(b, me.Username)
	if err := sched.Start(ctx); err != nil {
		return fmt.Errorf("restore schedules: %w", err)
	}
	if err := os.WriteFile(readinessPath, []byte("ready\n"), 0600); err != nil {
		return fmt.Errorf("mark initialized bot ready: %w", err)
	}
	defer os.Remove(readinessPath)
	log.Print("Бот запущен и готов к работе")
	b.Start(ctx)
	return nil
}

func parseAdminIDs(raw string, developerID int64) ([]int64, error) {
	ids := []int64{developerID}
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("AUTHORIZED_USER_ID must contain positive Telegram user IDs")
		}
		if id != developerID {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
