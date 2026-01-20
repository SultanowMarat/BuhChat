package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"html"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"
)

const telegramMaxBytes = 50 * 1024 * 1024 // 50 МБ — лимит Telegram для документов от бота

// App — зависимости для обработчиков (определён в main.go).
type App struct {
	Sheets        *SheetsAPI
	Yandex        *YandexDownloader
	Cfg           *Config
	GetText       func(string) string
	GetCategories func() ([]Category, error)
	IsAdmin       func(chatID int64, username string) bool
	GetState      func(int64) string
	SetState      func(int64, string)
	ResetState    func(int64)
	LogError      func(err, ctx string)
	OnReload      func()
}

// RegisterHandlers регистрирует все обработчики и middleware.
func RegisterHandlers(b *tele.Bot, app *App) {
	// Middleware: админские команды только для админов.
	b.Use(func(next tele.HandlerFunc) tele.HandlerFunc {
		return func(c tele.Context) error {
			text := c.Text()
			if text == "/send" || strings.HasPrefix(text, "/send ") || text == "/reload" {
				u := ""
				if c.Sender() != nil {
					u = c.Sender().Username
				}
				if !app.IsAdmin(c.Chat().ID, u) {
					return nil
				}
			}
			return next(c)
		}
	})

	// /start — deep-link dl_XXX для скачивания или приветствие.
	// Удаляем сообщение /start из чата, чтобы в истории не оставалось /start dl_UUID.
	b.Handle("/start", func(c tele.Context) error {
		payload := strings.TrimSpace(strings.TrimPrefix(c.Text(), "/start"))
		if c.Message() != nil {
			_ = c.Bot().Delete(c.Message())
		}
		if strings.HasPrefix(payload, "dl_") {
			handleDeepLink(c, app)
			return nil
		}

		log.Printf("[ /start] chat=%d", c.Chat().ID)
		msg := app.GetText(keyПриветствие)
		if msg == "" {
			msg = "Добрый день!"
		}
		if err := c.Send(msg, mainMenuReply(app)); err != nil {
			log.Printf("[ /start] Send failed: %v", err)
			return err
		}
		if c.Sender() == nil {
			return nil
		}
		app.ResetState(c.Sender().ID)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := app.Sheets.EnsureUser(ctx, fmt.Sprintf("%d", c.Sender().ID), c.Sender().Username); err != nil {
			app.LogError(err.Error(), "EnsureUser /start")
		}
		u := c.Sender().Username
		if app.IsAdmin(c.Chat().ID, u) {
			if err := app.Sheets.SetAdminChatID(ctx, u, c.Chat().ID); err != nil {
				app.LogError(err.Error(), "SetAdminChatID")
			}
			setCommandsForChat(c.Bot(), c.Chat().ID, true)
		} else {
			setCommandsForChat(c.Bot(), c.Chat().ID, false)
		}
		return nil
	})

	// Текстовые кнопки главного меню и /send (сбрасывают FSM при смене раздела).
	b.Handle(tele.OnText, func(c tele.Context) error {
		txt := strings.TrimSpace(c.Text())
		if txt == "/send" {
			return c.Send("Использование: /send <текст рассылки>")
		}
		if strings.HasPrefix(txt, "/send ") {
			return onSend(c, app, strings.TrimSpace(strings.TrimPrefix(txt, "/send ")))
		}
		switch txt {
		case "Список документов":
			app.ResetState(c.Sender().ID)
			return onListDocs(c, app, nil)
		case "Пожелания":
			app.ResetState(c.Sender().ID)
			return onWishStart(c, app)
		case "Запросить доступ в IMO":
			app.ResetState(c.Sender().ID)
			return onIMOStart(c, app)
		}

		// FSM: ожидание пожелания или IMO.
		switch app.GetState(c.Sender().ID) {
		case "wish":
			app.ResetState(c.Sender().ID)
			return onWishSubmit(c, app, txt)
		case "imo":
			return onIMOSubmit(c, app, txt)
		}

		return nil
	})

	// Inline: категории и документы. telebot кладёт в callback_data "\f" + Unique + "|" + Data.
	// Если свой handler не найден, приходит сырой data; убираем "\f" и разбираем.
	b.Handle(tele.OnCallback, func(c tele.Context) error {
		data := strings.TrimPrefix(c.Callback().Data, "\f")
		if data == "" {
			return nil
		}
		if data == "back_cats" {
			_ = c.Respond(&tele.CallbackResponse{})
			app.ResetState(c.Sender().ID)
			return onListDocs(c, app, nil)
		}
		if strings.HasPrefix(data, "cat|") {
			app.ResetState(c.Sender().ID)
			return onCategorySelect(c, app, strings.TrimPrefix(data, "cat|"))
		}
		if strings.HasPrefix(data, "dl_all|") {
			_ = c.Respond(&tele.CallbackResponse{})
			app.ResetState(c.Sender().ID)
			handleDlAll(c, app, strings.TrimPrefix(data, "dl_all|"))
			return nil
		}
		return nil
	})

	// /reload — сброс кэша (только админ).
	b.Handle("/reload", func(c tele.Context) error {
		return onReload(c, app)
	})
}

func setCommandsForChat(b *tele.Bot, chatID int64, admin bool) {
	cmds := []tele.Command{{Text: "start", Description: "Начать"}}
	if admin {
		cmds = append(cmds, tele.Command{Text: "send", Description: "Рассылка"}, tele.Command{Text: "reload", Description: "Сброс кэша"})
	}
	scope := tele.CommandScope{Type: tele.CommandScopeChat, ChatID: chatID}
	_ = b.SetCommands(cmds, scope)
}

func mainMenuReply(app *App) *tele.ReplyMarkup {
	m := &tele.ReplyMarkup{ResizeKeyboard: true}
	m.Reply(
		m.Row(m.Text("Список документов"), m.Text("Пожелания")),
		m.Row(m.Text("Запросить доступ в IMO")),
	)
	return m
}

func onListDocs(c tele.Context, app *App, editMsg *tele.Message) error {
	cats, err := app.GetCategories()
	if err != nil {
		app.LogError(err.Error(), "GetCategories")
		return c.Send("Не удалось загрузить категории.")
	}
	desc := app.GetText(keyОписаниеДокументы)
	if desc == "" {
		desc = "Выберите категорию:"
	}
	if len(cats) == 0 {
		if editMsg != nil {
			_, _ = c.Bot().Edit(editMsg, desc+"\n\nКатегории пока не добавлены.", tele.NoPreview)
			return nil
		}
		return c.Send(desc+"\n\nКатегории пока не добавлены.", tele.NoPreview)
	}
	m := &tele.ReplyMarkup{}
	var rows []tele.Row
	for _, cat := range cats {
		rows = append(rows, m.Row(m.Data(cat.Name, "cat", cat.ID)))
	}
	m.Inline(rows...)
	if editMsg != nil {
		_, err := c.Bot().Edit(editMsg, desc, m, tele.ModeHTML, tele.NoPreview)
		if err != nil {
			return c.Send(desc, m, tele.NoPreview)
		}
		return nil
	}
	return c.Send(desc, m, tele.NoPreview)
}

func onCategorySelect(c tele.Context, app *App, categoryID string) error {
	_ = c.Respond(&tele.CallbackResponse{})
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	docs, err := app.Sheets.GetDocumentsByCategory(ctx, categoryID)
	if err != nil {
		app.LogError(err.Error(), "GetDocumentsByCategory")
		if c.Message() != nil {
			_, _ = c.Bot().Edit(c.Message(), "Ошибка загрузки", tele.NoPreview)
		} else {
			_, _ = c.Bot().Send(c.Chat(), "Ошибка загрузки", tele.NoPreview)
		}
		return nil
	}
	if len(docs) == 0 {
		m := &tele.ReplyMarkup{}
		m.Inline(m.Row(m.Data("« Назад", "back_cats")))
		if c.Message() != nil {
			_, _ = c.Bot().Edit(c.Message(), "В этой категории пока нет документов.", m, tele.NoPreview)
		} else {
			_, _ = c.Bot().Send(c.Chat(), "В этой категории пока нет документов.", m, tele.NoPreview)
		}
		return nil
	}

	desc := app.GetText(keyОписаниеДокументы)
	var text string
	if desc != "" {
		text = desc + "\n\n"
	}

	botUsername := strings.TrimSpace(strings.TrimPrefix(app.Cfg.BotUsername, "@"))
	var blocks []string
	for idx, d := range docs {
		block := "Название: <b>" + html.EscapeString(d.Название) + "</b>\n\n"
		block += "Описание: <i>" + html.EscapeString(d.Описание) + "</i>"
		if link := strings.TrimSpace(d.Ссылка); link != "" && botUsername != "" {
			payload := base64.URLEncoding.EncodeToString([]byte(categoryID + "|" + strconv.Itoa(idx)))
			block += "\n\n<a href=\"https://t.me/" + html.EscapeString(botUsername) + "?start=dl_" + html.EscapeString(payload) + "\">Скачать файл</a>"
		}
		blocks = append(blocks, block)
	}
	text += strings.Join(blocks, "\n\n")

	hasLink := false
	for _, d := range docs {
		if strings.TrimSpace(d.Ссылка) != "" {
			hasLink = true
			break
		}
	}
	markup := &tele.ReplyMarkup{}
	btnBack := markup.Data("« Назад", "back_cats")
	if hasLink {
		markup.Inline(markup.Row(markup.Data("Скачать все", "dl_all|"+categoryID), btnBack))
	} else {
		markup.Inline(markup.Row(btnBack))
	}

	opts := []interface{}{markup, tele.ModeHTML, tele.NoPreview}
	if c.Message() != nil {
		_, err := c.Bot().Edit(c.Message(), text, opts...)
		if err == nil {
			return nil
		}
	}
	return c.Send(text, opts...)
}

// runProxyArchive: при наличии FileID — отправка по FileID; иначе скачивание с Яндекса, ZIP, отправка и сохранение File_ID.
// Удаляет statusMsg и временные файлы. При свободном месте < 100 МБ или ошибках — краткие сообщения без лишних «Ссылка:».
func runProxyArchive(ctx context.Context, bot *tele.Bot, chat tele.Recipient, app *App, categoryID string, idx int, statusMsg *tele.Message) {
	if statusMsg != nil {
		defer func() { _ = bot.Delete(statusMsg) }()
	}

	docs, err := app.Sheets.GetDocumentsByCategory(ctx, categoryID)
	if err != nil || idx < 0 || idx >= len(docs) {
		return
	}
	d := &docs[idx]
	link := strings.TrimSpace(d.Ссылка)
	docName := strings.TrimSpace(d.Название)
	if docName == "" {
		docName = "document"
	}
	if link == "" {
		return
	}

	// Быстрая отправка по сохранённому File_ID
	if d.FileID != "" {
		doc := &tele.Document{
			File:     tele.File{FileID: d.FileID},
			FileName: sanitizeZipName(docName) + ".zip",
			Caption:  "Файл: " + docName,
		}
		_, _ = bot.Send(chat, doc)
		return
	}

	// Проверка свободного места
	if free, err := getFreeSpaceBytes(os.TempDir()); err == nil && free < minFreeBytes {
		_, _ = bot.Send(chat, "Место на сервере ограничено, скачайте по ссылке: "+link, tele.NoPreview)
		return
	}

	if app.Yandex == nil {
		_, _ = bot.Send(chat, "Скачайте по ссылке: "+link, tele.NoPreview)
		return
	}

	size, err := app.Yandex.GetFileSize(ctx, link)
	if err == ErrNotYandexDisk {
		_, _ = bot.Send(chat, "Скачайте по ссылке: "+link, tele.NoPreview)
		return
	}
	if err == nil && size > 0 && size > telegramMaxBytes {
		_, _ = bot.Send(chat, "Файл слишком велик для отправки архивом (лимит Telegram 50МБ). Пожалуйста, скачайте его напрямую: "+link, tele.NoPreview)
		return
	}

	data, filename, err := app.Yandex.GetFile(ctx, link)
	if err == ErrNotYandexDisk {
		_, _ = bot.Send(chat, "Скачайте по ссылке: "+link, tele.NoPreview)
		return
	}
	if err == ErrFileTooLarge {
		_, _ = bot.Send(chat, "Файл слишком велик для отправки архивом (лимит Telegram 50МБ). Пожалуйста, скачайте его напрямую: "+link, tele.NoPreview)
		return
	}
	if err != nil {
		app.LogError(err.Error(), "GetFile proxy")
		_, _ = bot.Send(chat, "Не удалось подготовить файл.")
		return
	}

	zipPath, zipDir, err := ZipBytesToTemp(data, filename, sanitizeZipName(docName)+".zip")
	if err != nil {
		app.LogError(err.Error(), "ZipBytesToTemp")
		_, _ = bot.Send(chat, "Не удалось подготовить файл.")
		return
	}
	defer os.RemoveAll(zipDir)

	zipFileName := sanitizeZipName(docName) + ".zip"
	doc := &tele.Document{
		File:     tele.FromDisk(zipPath),
		FileName: zipFileName,
		Caption:  "Файл: " + docName,
	}
	msg, err := bot.Send(chat, doc)
	if err != nil {
		app.LogError(err.Error(), "Send document zip")
		_, _ = bot.Send(chat, "Не удалось подготовить файл.")
		return
	}
	if msg != nil && msg.Document != nil && msg.Document.FileID != "" {
		_ = app.Sheets.UpdateDocumentFileID(ctx, d.SheetRow, msg.Document.FileID)
	}
}

func handleDeepLink(c tele.Context, app *App) {
	payload := strings.TrimSpace(strings.TrimPrefix(c.Text(), "/start"))
	if !strings.HasPrefix(payload, "dl_") {
		return
	}
	b, err := base64.URLEncoding.DecodeString(strings.TrimPrefix(payload, "dl_"))
	if err != nil {
		return
	}
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 {
		return
	}
	categoryID := parts[0]
	idx, err := strconv.Atoi(parts[1])
	if err != nil || idx < 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	docs, err := app.Sheets.GetDocumentsByCategory(ctx, categoryID)
	if err != nil || idx >= len(docs) || strings.TrimSpace(docs[idx].Ссылка) == "" {
		return
	}

	statusMsg, _ := c.Bot().Send(c.Chat(), "⏳ Подготавливаю файл, это может занять несколько секунд...")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		runProxyArchive(ctx, c.Bot(), c.Chat(), app, categoryID, idx, statusMsg)
	}()
}

func handleDlAll(c tele.Context, app *App, categoryID string) {
	statusMsg := c.Message()
	if statusMsg != nil {
		_, _ = c.Bot().Edit(statusMsg, "⏳ Начинаю сборку архива...", tele.NoPreview)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		runBulkDownload(ctx, c.Bot(), c.Chat(), app, categoryID, statusMsg)
	}()
}

func runBulkDownload(ctx context.Context, bot *tele.Bot, chat tele.Recipient, app *App, categoryID string, statusMsg *tele.Message) {
	editStatus := func(text string) {
		if statusMsg != nil {
			_, _ = bot.Edit(statusMsg, text, tele.NoPreview)
		}
	}
	docs, err := app.Sheets.GetDocumentsByCategory(ctx, categoryID)
	if err != nil {
		app.LogError(err.Error(), "GetDocumentsByCategory bulk")
		editStatus("Не удалось собрать архив.")
		return
	}
	var items []BulkItem
	for _, d := range docs {
		link := strings.TrimSpace(d.Ссылка)
		if link == "" {
			continue
		}
		name := sanitizeZipName(d.Название)
		if name == "" {
			name = "document"
		}
		if !strings.Contains(filepath.Base(name), ".") {
			if u, e := url.Parse(link); e == nil && u != nil {
				ext := filepath.Ext(u.Path)
				if ext != "" {
					name = name + ext
				}
			}
		}
		items = append(items, BulkItem{URL: link, Filename: name})
	}
	if len(items) == 0 {
		editStatus("В категории нет файлов для скачивания.")
		return
	}
	var categoryName string
	if cats, _ := app.GetCategories(); cats != nil {
		for _, cat := range cats {
			if cat.ID == categoryID {
				categoryName = cat.Name
				break
			}
		}
	}
	if categoryName == "" {
		categoryName = "Archive"
	}

	zipPath, bulkDir, err := BulkDownloadAndZip(ctx, app.Yandex, items, categoryName, telegramMaxBytes, minFreeBytes)
	if err != nil {
		if err == ErrArchiveTooLarge {
			editStatus("⚠️ Общий размер файлов превышает 50 МБ. Пожалуйста, скачайте файлы по отдельности.")
			return
		}
		app.LogError(err.Error(), "BulkDownloadAndZip")
		editStatus("Не удалось собрать архив.")
		return
	}
	defer os.RemoveAll(bulkDir)

	doc := &tele.Document{
		File:     tele.FromDisk(zipPath),
		FileName: filepath.Base(zipPath),
		Caption:  "Архив: " + categoryName,
	}
	if _, err := bot.Send(chat, doc, tele.NoPreview); err != nil {
		app.LogError(err.Error(), "BulkDownload Send")
		editStatus("Не удалось отправить архив.")
		return
	}
	editStatus("📦 Архив собран и отправлен ниже.")
}

func sanitizeZipName(s string) string {
	const bad = `/\:*?"<>|`
	out := strings.Map(func(r rune) rune {
		if strings.ContainsRune(bad, r) {
			return -1
		}
		return r
	}, s)
	out = strings.TrimSpace(out)
	if out == "" {
		return "document"
	}
	return out
}

// notifyAdmins отправляет сообщение всем админам с заполненным ID_Чата. Вызывать в горутине.
func notifyAdmins(bot *tele.Bot, app *App, msg string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ids, err := app.Sheets.GetAdminChatIDs(ctx)
	if err != nil {
		app.LogError(err.Error(), "GetAdminChatIDs notify")
		return
	}
	if len(ids) == 0 {
		app.LogError("notifyAdmins: 0 админов с заполненным ID_Чата (админ должен хотя бы раз нажать /start)", "notify")
		return
	}
	for _, id := range ids {
		if _, err := bot.Send(&tele.Chat{ID: id}, msg); err != nil {
			app.LogError(err.Error(), "notify admin "+fmt.Sprintf("%d", id))
		}
	}
}

func onWishStart(c tele.Context, app *App) error {
	app.SetState(c.Sender().ID, "wish")
	msg := app.GetText(keyОписаниеПожелания)
	if msg == "" {
		msg = "Напишите ваше пожелание:"
	}
	return c.Send(msg)
}

func onWishSubmit(c tele.Context, app *App, text string) error {
	if text == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	username := c.Sender().Username
	if username == "" {
		username = c.Sender().FirstName
	}
	err := app.Sheets.AppendWish(ctx, username, fmt.Sprintf("%d", c.Sender().ID), text)
	if err != nil {
		app.LogError(err.Error(), "AppendWish")
		return c.Send("Не удалось сохранить. Попробуйте позже.")
	}
	// Уведомление админам в фоне
	display := username
	if c.Sender().Username != "" {
		display = "@" + c.Sender().Username
	}
	userID := fmt.Sprintf("%d", c.Sender().ID)
	msg := fmt.Sprintf("📝 Новое пожелание\nОт: %s (id: %s)\n\n%s", display, userID, text)
	go notifyAdmins(c.Bot(), app, msg)
	return c.Send("Спасибо! Ваше пожелание сохранено.")
}

func onIMOStart(c tele.Context, app *App) error {
	app.SetState(c.Sender().ID, "imo")
	msg := app.GetText(keyОписаниеIMO)
	if msg == "" {
		msg = "Введите данные (каждое поле с новой строки):\n1. ФИО\n2. Телефон\n3. Должность\n4. Источник"
	}
	return c.Send(msg)
}

func onIMOSubmit(c tele.Context, app *App, text string) error {
	lines := strings.Split(text, "\n")
	var parts []string
	for _, s := range lines {
		s = strings.TrimSpace(s)
		if s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) < 4 {
		msg := app.GetText(keyТекстОшибкиАнкеты)
		if msg == "" {
			msg = "Нужно минимум 4 строки: ФИО, Телефон, Должность, Источник."
		}
		return c.Send(msg)
	}
	app.ResetState(c.Sender().ID)
	fio, phone, pos, src := parts[0], parts[1], parts[2], strings.Join(parts[3:], " ")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	username := c.Sender().Username
	if username == "" {
		username = c.Sender().FirstName
	}
	err := app.Sheets.AppendIMO(ctx, username, fmt.Sprintf("%d", c.Sender().ID), fio, phone, pos, src)
	if err != nil {
		app.LogError(err.Error(), "AppendIMO")
		return c.Send("Не удалось сохранить заявку. Попробуйте позже.")
	}
	// Уведомление админам в фоне
	display := username
	if c.Sender().Username != "" {
		display = "@" + c.Sender().Username
	}
	userID := fmt.Sprintf("%d", c.Sender().ID)
	msg := fmt.Sprintf("📋 Новая заявка IMO\nОт: %s (id: %s)\nФИО: %s\nТелефон: %s\nДолжность: %s\nИсточник: %s", display, userID, fio, phone, pos, src)
	go notifyAdmins(c.Bot(), app, msg)
	return c.Send("Заявка принята. Спасибо!")
}

func onSend(c tele.Context, app *App, text string) error {
	if text == "" {
		return c.Send("Использование: /send <текст рассылки>")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	chatIDs, err := app.Sheets.GetAllUserChatIDs(ctx)
	if err != nil {
		app.LogError(err.Error(), "GetAllUserChatIDs")
		return c.Send("Ошибка загрузки списка пользователей.")
	}
	var failed int
	for _, id := range chatIDs {
		_, err := c.Bot().Send(&tele.Chat{ID: id}, text)
		if err != nil {
			failed++
			app.LogError(err.Error(), "Send broadcast to "+fmt.Sprintf("%d", id))
		}
	}
	return c.Send(fmt.Sprintf("Рассылка завершена. Отправлено: %d, ошибок: %d", len(chatIDs)-failed, failed))
}

func onReload(c tele.Context, app *App) error {
	if app.OnReload != nil {
		app.OnReload()
	}
	return c.Send("Кэш сброшен.")
}
