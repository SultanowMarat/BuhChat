package main

import (
	"os"
	"strconv"
	"strings"
)

// Config — конфигурация приложения из переменных окружения.
type Config struct {
	BotToken        string
	BotUsername     string // без @ (опционально)
	SpreadsheetID   string
	CredentialsPath string
	CacheTTLMin     int
	YandexMaxMB     int64
}

// LoadConfig загружает конфигурацию из .env-подобных переменных.
// Переменные можно задать через .env файл (загрузка через godotenv) или в окружении.
func LoadConfig() (*Config, error) {
	c := &Config{
		BotToken:        os.Getenv("BOT_TOKEN"),
		BotUsername:     strings.TrimSpace(strings.TrimPrefix(os.Getenv("BOT_USERNAME"), "@")),
		SpreadsheetID:   strings.TrimSpace(os.Getenv("SPREADSHEET_ID")),
		CredentialsPath: os.Getenv("CREDENTIALS_PATH"),
	}
	if c.CredentialsPath == "" {
		c.CredentialsPath = "credentials.json"
	}

	// CACHE_TTL_MIN и YANDEX_MAX_MB — из .env, иначе 5 и 50
	if v := os.Getenv("CACHE_TTL_MIN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.CacheTTLMin = n
		} else {
			c.CacheTTLMin = 5
		}
	} else {
		c.CacheTTLMin = 5
	}
	if v := os.Getenv("YANDEX_MAX_MB"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			c.YandexMaxMB = n
		} else {
			c.YandexMaxMB = 50
		}
	} else {
		c.YandexMaxMB = 50
	}

	return c, nil
}
