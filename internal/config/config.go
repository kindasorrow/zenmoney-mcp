package config

import (
	"errors"
	"os"
	"strings"
)

// Config — настройки из окружения; в контейнер их кладёт docker compose из .env.
type Config struct {
	// Token не логируется и не попадает в ответы тулов.
	Token  string
	DBPath string
}

// defaultDBPath лежит на volume, который compose монтирует в /data.
const defaultDBPath = "/data/zm.db"

func Load() *Config {
	return &Config{
		// Пробелы после копирования из браузера дают 401, будто токен протух.
		Token:  strings.TrimSpace(os.Getenv("ZENMONEY_TOKEN")),
		DBPath: getEnv("ZENMONEY_DB", defaultDBPath),
	}
}

// Validate ловит пустой токен на старте: иначе сервер выглядел бы рабочим, а каждый тул падал.
func (c *Config) Validate() error {
	if c.Token == "" {
		return errors.New("config: не задан ZENMONEY_TOKEN, получить токен можно на https://zerro.app/token")
	}

	return nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
