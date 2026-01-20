package main

import (
	"context"
	"encoding/csv"
	"log"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	tele "gopkg.in/telebot.v3"
)

const (
	minFreeBytes    = 100 * 1024 * 1024 // 100 МБ — минимум свободного места для скачивания
	cleanupMaxAge   = 30 * time.Minute  // удалять файлы в /tmp старше 30 минут
	cleanupInterval = 1 * time.Hour     // проверка раз в час
)

func main() {
	_ = loadEnv()

	// -log "сообщение" — запись в Логи_Сервера и выход (для deploy.sh)
	for i := 1; i < len(os.Args)-1; i++ {
		if os.Args[i] == "-log" {
			cfg, err := LoadConfig()
			if err != nil || cfg.SpreadsheetID == "" {
				log.Fatal("Для -log нужны SPREADSHEET_ID и CREDENTIALS_PATH в .env")
			}
			ctx := context.Background()
			api, err := NewSheetsAPI(ctx, cfg.SpreadsheetID, cfg.CredentialsPath)
			if err != nil {
				log.Fatalf("Sheets API: %v", err)
			}
			_ = api.EnsureSchema(ctx)
			if err := api.LogToSheets(ctx, "Info", os.Args[i+1]); err != nil {
				log.Fatalf("LogToSheets: %v", err)
			}
			os.Exit(0)
		}
	}

	for _, a := range os.Args[1:] {
		if a == "-fill-settings" {
			fillSettings()
			os.Exit(0)
		}
		if a == "-fill-test-data" {
			fillTestData()
			os.Exit(0)
		}
	}

	cfg, err := LoadConfig()
	if err != nil || cfg.BotToken == "" || cfg.SpreadsheetID == "" {
		log.Fatal("Нужны BOT_TOKEN и SPREADSHEET_ID (и CREDENTIALS_PATH к ключу Service Account). См. env.example.")
	}

	ctx := context.Background()
	sheetsAPI, err := NewSheetsAPI(ctx, cfg.SpreadsheetID, cfg.CredentialsPath)
	if err != nil {
		log.Fatalf("Sheets API: %v", err)
	}

	if err := sheetsAPI.EnsureSchema(ctx); err != nil {
		log.Fatalf("EnsureSchema: %v", err)
	}

	cache := newCache(sheetsAPI, cfg.CacheTTLMin)
	cache.reload(ctx)

	yd := NewYandexDownloader(cfg.YandexMaxMB * 1024 * 1024)

	fsm := newFSM()

	app := &App{
		Sheets:  sheetsAPI,
		Yandex:  yd,
		Cfg:     cfg,
		GetText: cache.getText,
		GetCategories: func() ([]Category, error) {
			return cache.getCategories(ctx)
		},
		IsAdmin: func(chatID int64, username string) bool {
			return cache.isAdmin(chatID, username)
		},
		GetState:   fsm.get,
		SetState:   fsm.set,
		ResetState: fsm.reset,
		LogError: func(e, c string) {
			sheetsAPI.LogError(context.Background(), e, c)
		},
		OnReload: func() {
			cache.reload(ctx)
		},
	}

	pref := tele.Settings{Token: cfg.BotToken, Poller: &tele.LongPoller{Timeout: 10 * time.Second}}
	bot, err := tele.NewBot(pref)
	if err != nil {
		log.Fatalf("telebot: %v", err)
	}

	RegisterHandlers(bot, app)
	go StartCleanupWorker()
	log.Println("Бот запущен.")
	_ = sheetsAPI.LogToSheets(ctx, "Старт", "Бот запущен")

	go bot.Start()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	_ = sheetsAPI.LogToSheets(context.Background(), "Остановка", "Бот остановлен")
	os.Exit(0)
}

// getFreeSpaceBytes возвращает свободное место в байтах для пути (например os.TempDir()).
// На Windows и при ошибке возвращает большое значение, чтобы не блокировать скачивание.
func getFreeSpaceBytes(path string) (uint64, error) {
	if runtime.GOOS == "windows" {
		return math.MaxUint64, nil
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return math.MaxUint64, nil
	}
	return st.Bavail * uint64(st.Bsize), nil
}

// StartCleanupWorker раз в час удаляет bugchat-*, каталоги bulk_* и single_* старше 30 минут.
func StartCleanupWorker() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	dir := os.TempDir()
	for range ticker.C {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		now := time.Now()
		for _, e := range entries {
			name := e.Name()
			path := filepath.Join(dir, name)
			if e.IsDir() {
				if strings.HasPrefix(name, "bulk_") || strings.HasPrefix(name, "single_") {
					info, err := e.Info()
					if err != nil {
						continue
					}
					if now.Sub(info.ModTime()) >= cleanupMaxAge {
						_ = os.RemoveAll(path)
					}
				}
				continue
			}
			if !strings.HasPrefix(name, "bugchat-") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if now.Sub(info.ModTime()) < cleanupMaxAge {
				continue
			}
			_ = os.Remove(path)
		}
	}
}

func loadEnv() error {
	data, err := os.ReadFile(".env")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.Index(line, "=")
		if i <= 0 {
			continue
		}
		k, v := strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:])
		v = strings.Trim(v, "\"'")
		if k != "" && os.Getenv(k) == "" {
			_ = os.Setenv(k, v)
		}
	}
	return nil
}

func fillSettings() {
	cfg, err := LoadConfig()
	if err != nil || cfg.SpreadsheetID == "" {
		log.Fatal("Для -fill-settings нужны SPREADSHEET_ID и CREDENTIALS_PATH в .env. BOT_TOKEN не обязателен.")
	}
	ctx := context.Background()
	api, err := NewSheetsAPI(ctx, cfg.SpreadsheetID, cfg.CredentialsPath)
	if err != nil {
		log.Fatalf("Sheets API: %v", err)
	}
	if err := api.EnsureSchema(ctx); err != nil {
		log.Fatalf("EnsureSchema: %v", err)
	}
	f, err := os.Open("settings_text.example.csv")
	if err != nil {
		log.Fatalf("Открой settings_text.example.csv: %v (запускай из корня проекта)", err)
	}
	defer f.Close()
	recs, err := csv.NewReader(f).ReadAll()
	if err != nil {
		log.Fatalf("CSV: %v", err)
	}
	var rows [][]interface{}
	for i, r := range recs {
		if i == 0 && len(r) >= 2 && r[0] == "Ключ" && r[1] == "Текст" {
			continue
		}
		if len(r) >= 2 && strings.TrimSpace(r[0]) != "" {
			rows = append(rows, []interface{}{strings.TrimSpace(r[0]), strings.TrimSpace(r[1])})
		}
	}
	if len(rows) == 0 {
		log.Fatal("В CSV нет строк с Ключ/Текст.")
	}
	if err := api.UpdateSettingsText(ctx, rows); err != nil {
		log.Fatalf("UpdateSettingsText: %v", err)
	}
	log.Printf("Записано %d строк в лист «Настройки_Текста».", len(rows))
}

func fillTestData() {
	cfg, err := LoadConfig()
	if err != nil || cfg.SpreadsheetID == "" {
		log.Fatal("Для -fill-test-data нужны SPREADSHEET_ID и CREDENTIALS_PATH в .env. BOT_TOKEN не обязателен.")
	}
	ctx := context.Background()
	api, err := NewSheetsAPI(ctx, cfg.SpreadsheetID, cfg.CredentialsPath)
	if err != nil {
		log.Fatalf("Sheets API: %v", err)
	}
	if err := api.EnsureSchema(ctx); err != nil {
		log.Fatalf("EnsureSchema: %v", err)
	}

	// Категории: Название, ID (фиксированные ID для привязки документов)
	categories := [][]interface{}{
		{"Внутренние документы", "test-cat-internal"},
		{"Полезные ссылки", "test-cat-links"},
	}
	if err := api.WriteSheetData(ctx, sheetКатегории, 2, categories); err != nil {
		log.Fatalf("Категории: %v", err)
	}
	log.Printf("Категории: записано %d строк.", len(categories))

	// Документы: ID_Категории, Название, Описание, Ссылка
	documents := [][]interface{}{
		{"test-cat-internal", "Регламент", "Внутренний регламент организации.", "https://example.com/reglament.pdf"},
		{"test-cat-internal", "Инструкция по ОТ", "Инструкция по охране труда.", "https://example.com/ot.pdf"},
		{"test-cat-links", "Яндекс.Диск (тест)", "При ссылке на disk.yandex.ru бот попытается скачать файл; при ошибке отправит ссылку.", "https://disk.yandex.ru/i/placeholder"},
		{"test-cat-links", "Памятка", "Памятка для новичков.", "https://example.com/pamyatka.pdf"},
	}
	if err := api.WriteSheetData(ctx, sheetДокументы, 2, documents); err != nil {
		log.Fatalf("Документы: %v", err)
	}
	log.Printf("Документы: записано %d строк.", len(documents))

	// Админы: одна строка-подсказка (замените на свой @username или добавьте свою строку)
	if err := api.appendRow(ctx, sheetАдмины, []interface{}{"ЗАМЕНИТЕ_НА_СВОЙ_ЮЗЕРНЕЙМ", ""}); err != nil {
		log.Fatalf("Админы: %v", err)
	}
	log.Print("Админы: добавлена строка «ЗАМЕНИТЕ_НА_СВОЙ_ЮЗЕРНЕЙМ» — замените на свой @username или допишите себя вручную.")

	log.Print("Тестовые данные записаны. Запустите бота (go run .) и проверьте: /start, Список документов, Пожелания, Запросить доступ в IMO.")
}

type cache struct {
	mu        sync.RWMutex
	texts     map[string]string
	cats      []Category
	chatIDs   map[int64]bool
	usernames map[string]bool
	expires   time.Time
	ttl       time.Duration
	sheets    *SheetsAPI
}

func newCache(s *SheetsAPI, ttlMin int) *cache {
	return &cache{sheets: s, ttl: time.Duration(ttlMin) * time.Minute}
}

func (c *cache) reload(ctx context.Context) {
	texts, _ := c.sheets.GetTextSettings(ctx)
	cats, _ := c.sheets.GetCategories(ctx)
	chatIDs, usernames, _ := c.sheets.GetAdmins(ctx)
	// Юзернеймы в нижнем регистре для регистронезависимого isAdmin
	usernamesNorm := make(map[string]bool)
	for k := range usernames {
		usernamesNorm[strings.ToLower(k)] = true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.texts = texts
	c.cats = cats
	c.chatIDs = chatIDs
	c.usernames = usernamesNorm
	c.expires = time.Now().Add(c.ttl)
}

func (c *cache) ensure(ctx context.Context) {
	c.mu.Lock()
	if time.Now().Before(c.expires) {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	c.reload(ctx)
}

func (c *cache) getText(k string) string {
	c.ensure(context.Background())
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.texts[k]
}

func (c *cache) getCategories(ctx context.Context) ([]Category, error) {
	c.ensure(ctx)
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Category, len(c.cats))
	copy(out, c.cats)
	return out, nil
}

func (c *cache) isAdmin(chatID int64, username string) bool {
	c.ensure(context.Background())
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.chatIDs[chatID] {
		return true
	}
	u := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(username, "@")))
	return u != "" && c.usernames[u]
}

type fsm struct {
	mu    sync.RWMutex
	state map[int64]string
}

func newFSM() *fsm { return &fsm{state: make(map[int64]string)} }

func (f *fsm) get(uid int64) string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.state[uid]
}

func (f *fsm) set(uid int64, s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s == "" {
		delete(f.state, uid)
	} else {
		f.state[uid] = s
	}
}

func (f *fsm) reset(uid int64) { f.set(uid, "") }
