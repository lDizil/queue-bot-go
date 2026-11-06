package config

import (
	"errors"
	"fmt"
	"os"

	"queuebot/settings"

	"github.com/spf13/viper"
)

type Config struct {
	TelegramToken   string `mapstructure:"bot_token"`
	ChatId          int    `mapstructure:"bot_chat_id"`
	AdminsID        string `mapstructure:"authorized_user_id"`
	DeveloperUserID int64  `mapstructure:"developer_user_id"`

	DBUser string `mapstructure:"db_user"`
	DBPass string `mapstructure:"db_password"`
	DBName string `mapstructure:"db_name"`
	DBPort string `mapstructure:"db_port"`
	DBHost string `mapstructure:"db_host"`

	Settings settings.Values `mapstructure:",squash"`
}

var environmentKeys = map[string][]string{
	"bot_token":                 {"BOT_TOKEN"},
	"bot_chat_id":               {"BOT_CHAT_ID"},
	"authorized_user_id":        {"AUTHORIZED_USER_ID"},
	"developer_user_id":         {"DEVELOPER_USER_ID"},
	"db_user":                   {"DB_USER"},
	"db_password":               {"DB_PASSWORD"},
	"db_name":                   {"DB_NAME"},
	"db_port":                   {"DB_PORT"},
	"db_host":                   {"DB_HOST"},
	"total_slots_in_queue":      {"TOTAL_SLOTS_IN_QUEUE"},
	"amount_of_buttons_in_row":  {"AMOUNT_OF_BUTTONS_IN_ROW"},
	"delay_update_queue":        {"DELAY_UPDATE_QUEUE", "DELAY_QUEUE"},
	"edit_session_timeout":      {"EDIT_SESSION_TIMEOUT", "EXIRED_SES_TIME"},
	"week1_date":                {"WEEK1_DATE"},
	"week1_type":                {"WEEK1_TYPE"},
	"scheduler_tick_interval":   {"SCHEDULER_TICK_INTERVAL"},
	"one_shot_delay":            {"ONE_SHOT_DELAY"},
	"one_shot_duration":         {"ONE_SHOT_DURATION"},
	"moderation_block_duration": {"MODERATION_BLOCK_DURATION"},
	"update_workers":            {"UPDATE_WORKERS"},
}

func Load() (Config, error) {
	values := viper.New()
	setDefaults(values)
	if err := mergePublicSettings(values); err != nil {
		return Config{}, err
	}

	if err := mergeLegacyEnvironment(values); err != nil {
		return Config{}, err
	}
	values.AllowEmptyEnv(true)
	for key, names := range environmentKeys {
		if err := values.BindEnv(append([]string{key}, names...)...); err != nil {
			return Config{}, fmt.Errorf("bind environment for %s", key)
		}
	}

	var cfg Config
	if err := values.Unmarshal(&cfg); err != nil {
		return Config{}, errors.New("invalid configuration values; check config.yaml and environment variables")
	}
	if cfg.DeveloperUserID <= 0 {
		return Config{}, errors.New("DEVELOPER_USER_ID must be positive")
	}
	if err := cfg.Settings.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

func mergePublicSettings(values *viper.Viper) error {
	public := viper.New()
	public.SetConfigFile("config.yaml")
	if err := public.ReadInConfig(); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return errors.New("cannot read config.yaml")
	}
	var decoded settings.Values
	if err := public.UnmarshalExact(&decoded); err != nil {
		return errors.New("config.yaml accepts only public queue and calendar settings; infrastructure and access settings must use environment variables")
	}
	if err := values.MergeConfigMap(public.AllSettings()); err != nil {
		return errors.New("cannot merge public settings from config.yaml")
	}
	return nil
}

func setDefaults(values *viper.Viper) {
	defaults := settings.Defaults()
	values.SetDefault("developer_user_id", int64(789915524))
	values.SetDefault("total_slots_in_queue", defaults.TotalSlotsInQueue)
	values.SetDefault("amount_of_buttons_in_row", defaults.AmountOfButtonsInRow)
	values.SetDefault("delay_update_queue", defaults.DelayUpdateQueue)
	values.SetDefault("edit_session_timeout", defaults.TimeForExpiredEditSes)
	values.SetDefault("week1_date", defaults.Week1Date)
	values.SetDefault("week1_type", defaults.Week1Type)
	values.SetDefault("scheduler_tick_interval", defaults.SchedulerTickInterval)
	values.SetDefault("one_shot_delay", defaults.OneShotDelay)
	values.SetDefault("one_shot_duration", defaults.OneShotDuration)
	values.SetDefault("update_workers", defaults.UpdateWorkers)
	values.SetDefault("moderation_block_duration", defaults.ModerationBlockDuration)
}

func mergeLegacyEnvironment(values *viper.Viper) error {
	legacy := viper.New()
	legacy.SetConfigFile(".env")
	legacy.SetConfigType("env")
	if err := legacy.ReadInConfig(); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return errors.New("cannot read .env")
	}

	overrides := make(map[string]any)
	for key, names := range environmentKeys {
		for _, name := range names {
			if legacy.IsSet(name) {
				overrides[key] = legacy.Get(name)
				break
			}
		}
	}
	if err := values.MergeConfigMap(overrides); err != nil {
		return errors.New("cannot merge legacy environment settings")
	}
	return nil
}
