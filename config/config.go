package config

import (
	"os"

	"github.com/spf13/viper"
)

type Config struct {
	TelegramToken string `mapstructure:"BOT_TOKEN"`
	ChatId        string `mapstructure:"BOT_CHAT_ID"`
	AdminsID      string `mapstructure:"AUTHORIZED_USER_ID"`
	DBUser        string `mapstructure:"DB_USER"`
	DBPass        string `mapstructure:"DB_PASSWORD"`
	DBName        string `mapstructure:"DB_NAME"`
	DBPort        string `mapstructure:"DB_PORT"`
	DBHost        string `mapstructure:"DB_HOST"`
}

func Load() (Config, error) {
	viper.SetConfigFile(".env")
	viper.SetConfigType("env")
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(*os.PathError); !ok {
			return Config{}, err
		}
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}
