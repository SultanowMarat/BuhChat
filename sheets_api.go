package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

const (
	sheetНастройкиТекста = "Настройки_Текста"
	sheetКатегории       = "Категории"
	sheetДокументы       = "Документы"
	sheetПожелания       = "Пожелания"
	sheetЗаявкиIMO       = "Заявки_IMO"
	sheetПользователи    = "Пользователи"
	sheetАдмины          = "Админы"
	sheetЛогиОшибок      = "Логи_Ошибок"
)

// Заголовки листов: имя листа -> первая строка (колонки).
var sheetHeaders = map[string][]string{
	sheetНастройкиТекста: {"Ключ", "Текст"},
	sheetКатегории:       {"Название", "ID"},
	sheetДокументы:       {"ID_Категории", "Название", "Описание", "Ссылка", "File_ID"},
	sheetПожелания:       {"Дата", "Юзернейм", "ID_Юзера", "Текст"},
	sheetЗаявкиIMO:       {"Дата", "Юзернейм", "ID_Юзера", "ФИО", "Телефон", "Должность", "Источник"},
	sheetПользователи:    {"ID_Пользователя", "Юзернейм", "Дата_Регистрации"},
	sheetАдмины:          {"Юзернейм", "ID_Чата"},
	sheetЛогиОшибок:      {"Дата", "Ошибка", "Контекст"},
}

// Ключи текста из "Настройки_Текста".
const (
	keyПриветствие       = "Приветствие"
	keyОписаниеДокументы = "Описание_Документы"
	keyОписаниеПожелания = "Описание_Пожелания"
	keyОписаниеIMO       = "Описание_IMO"
	keyТекстОшибкиАнкеты = "Текст_Ошибки_Анкеты"
)

// SheetsAPI — клиент для работы с Google Sheets.
type SheetsAPI struct {
	svc           *sheets.Service
	spreadsheetID string
}

// NewSheetsAPI создаёт клиент Google Sheets.
func NewSheetsAPI(ctx context.Context, spreadsheetID, credentialsPath string) (*SheetsAPI, error) {
	svc, err := sheets.NewService(ctx, option.WithCredentialsFile(credentialsPath))
	if err != nil {
		return nil, fmt.Errorf("sheets.NewService: %w", err)
	}
	return &SheetsAPI{svc: svc, spreadsheetID: spreadsheetID}, nil
}

// EnsureSheets создаёт недостающие листы с заголовками.
func (s *SheetsAPI) EnsureSheets(ctx context.Context) error {
	spreadsheet, err := s.svc.Spreadsheets.Get(s.spreadsheetID).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("Spreadsheets.Get: %w", err)
	}

	existing := make(map[string]bool)
	for _, sh := range spreadsheet.Sheets {
		if sh.Properties != nil && sh.Properties.Title != "" {
			existing[sh.Properties.Title] = true
		}
	}

	var addReqs []*sheets.Request
	for title := range sheetHeaders {
		if existing[title] {
			continue
		}
		addReqs = append(addReqs, &sheets.Request{
			AddSheet: &sheets.AddSheetRequest{
				Properties: &sheets.SheetProperties{Title: title},
			},
		})
	}

	if len(addReqs) == 0 {
		return nil
	}

	_, err = s.svc.Spreadsheets.BatchUpdate(s.spreadsheetID, &sheets.BatchUpdateSpreadsheetRequest{
		Requests: addReqs,
	}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("BatchUpdate AddSheet: %w", err)
	}

	// Записываем заголовки для только что созданных листов.
	for title, headers := range sheetHeaders {
		if existing[title] {
			continue
		}
		if len(headers) == 0 {
			continue
		}
		lastCol := colToLetter(len(headers))
		rangeStr := fmt.Sprintf("%s!A1:%s1", title, lastCol)
		row := make([]interface{}, len(headers))
		for i, h := range headers {
			row[i] = h
		}
		vr := &sheets.ValueRange{Values: [][]interface{}{row}}

		_, err = s.svc.Spreadsheets.Values.Update(s.spreadsheetID, rangeStr, vr).
			ValueInputOption("RAW").Context(ctx).Do()
		if err != nil {
			return fmt.Errorf("Values.Update %s: %w", title, err)
		}
	}

	return nil
}

// colToLetter конвертирует индекс колонки (1-based) в букву: 1→A, 26→Z, 27→AA.
func colToLetter(n int) string {
	if n <= 0 {
		return ""
	}
	if n <= 26 {
		return string(rune('A' + n - 1))
	}
	return colToLetter((n-1)/26) + string(rune('A'+(n-1)%26))
}

// EnsureSchema создаёт недостающие листы, затем для каждого листа проверяет первую строку:
// если какой-то колонки из sheetHeaders нет — дописывает её в конец первой строки (авто-миграция).
// Вызывать при старте main.
func (s *SheetsAPI) EnsureSchema(ctx context.Context) error {
	if err := s.EnsureSheets(ctx); err != nil {
		return err
	}
	for title := range sheetHeaders {
		if err := s.ensureSheetColumns(ctx, title); err != nil {
			return fmt.Errorf("ensureSheetColumns %s: %w", title, err)
		}
	}
	return nil
}

// ensureSheetColumns читает первую строку листа, находит недостающие заголовки из sheetHeaders
// и дописывает их в конец первой строки.
func (s *SheetsAPI) ensureSheetColumns(ctx context.Context, title string) error {
	expected := sheetHeaders[title]
	if len(expected) == 0 {
		return nil
	}
	rangeStr := title + "!A1:ZZ1"
	resp, err := s.svc.Spreadsheets.Values.Get(s.spreadsheetID, rangeStr).Context(ctx).Do()
	if err != nil {
		return err
	}
	var existing []string
	if len(resp.Values) > 0 {
		for _, c := range resp.Values[0] {
			existing = append(existing, strings.TrimSpace(strCell(c)))
		}
	}
	has := make(map[string]bool)
	for _, h := range existing {
		has[h] = true
	}
	var toAdd []string
	for _, h := range expected {
		if !has[h] {
			toAdd = append(toAdd, h)
			has[h] = true
		}
	}
	if len(toAdd) == 0 {
		return nil
	}
	startCol := len(existing) + 1
	endCol := startCol + len(toAdd) - 1
	updateRange := fmt.Sprintf("%s!%s1:%s1", title, colToLetter(startCol), colToLetter(endCol))
	row := make([]interface{}, len(toAdd))
	for i, h := range toAdd {
		row[i] = h
	}
	vr := &sheets.ValueRange{Values: [][]interface{}{row}}
	_, err = s.svc.Spreadsheets.Values.Update(s.spreadsheetID, updateRange, vr).
		ValueInputOption("RAW").Context(ctx).Do()
	return err
}

// WriteSheetData записывает строки в лист, начиная с указанной (startRow 1-based). Перезаписывает ячейки.
func (s *SheetsAPI) WriteSheetData(ctx context.Context, sheet string, startRow int, rows [][]interface{}) error {
	if len(rows) == 0 {
		return nil
	}
	cols := len(rows[0])
	endCol := string(rune('A' + cols - 1))
	endRow := startRow + len(rows) - 1
	rangeStr := fmt.Sprintf("%s!A%d:%s%d", sheet, startRow, endCol, endRow)
	vr := &sheets.ValueRange{Values: rows}
	_, err := s.svc.Spreadsheets.Values.Update(s.spreadsheetID, rangeStr, vr).
		ValueInputOption("RAW").Context(ctx).Do()
	return err
}

// UpdateSettingsText записывает строки [Ключ, Текст] в лист "Настройки_Текста" начиная со 2-й строки.
func (s *SheetsAPI) UpdateSettingsText(ctx context.Context, rows [][]interface{}) error {
	if len(rows) == 0 {
		return nil
	}
	endRow := 1 + len(rows)
	rangeStr := fmt.Sprintf("%s!A2:B%d", sheetНастройкиТекста, endRow)
	vr := &sheets.ValueRange{Values: rows}
	_, err := s.svc.Spreadsheets.Values.Update(s.spreadsheetID, rangeStr, vr).
		ValueInputOption("RAW").Context(ctx).Do()
	return err
}

// GetTextSettings возвращает карту ключ -> текст из "Настройки_Текста".
func (s *SheetsAPI) GetTextSettings(ctx context.Context) (map[string]string, error) {
	rangeStr := sheetНастройкиТекста + "!A2:B"
	resp, err := s.svc.Spreadsheets.Values.Get(s.spreadsheetID, rangeStr).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("Values.Get Настройки_Текста: %w", err)
	}
	out := make(map[string]string)
	for _, row := range resp.Values {
		if len(row) < 2 {
			continue
		}
		k := strings.TrimSpace(strCell(row[0]))
		v := strings.TrimSpace(strCell(row[1]))
		if k != "" {
			out[k] = v
		}
	}
	return out, nil
}

func strCell(v interface{}) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}

// Category — категория документов.
type Category struct {
	ID   string
	Name string
}

// GetCategories возвращает категории. Пустые ID заполняются UUID и сохраняются в таблицу.
func (s *SheetsAPI) GetCategories(ctx context.Context) ([]Category, error) {
	rangeStr := sheetКатегории + "!A2:B"
	resp, err := s.svc.Spreadsheets.Values.Get(s.spreadsheetID, rangeStr).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("Values.Get Категории: %w", err)
	}

	var list []Category
	var updates []struct {
		row int
		id  string
	}
	for i, row := range resp.Values {
		rowNum := i + 2
		name := ""
		id := ""
		if len(row) >= 1 {
			name = strings.TrimSpace(strCell(row[0]))
		}
		if len(row) >= 2 {
			id = strings.TrimSpace(strCell(row[1]))
		}
		if name == "" {
			continue
		}
		if id == "" {
			id = uuid.New().String()
			updates = append(updates, struct {
				row int
				id  string
			}{rowNum, id})
		}
		list = append(list, Category{ID: id, Name: name})
	}

	for _, u := range updates {
		rangeStr := fmt.Sprintf("%s!B%d", sheetКатегории, u.row)
		vr := &sheets.ValueRange{Values: [][]interface{}{{u.id}}}
		_, err = s.svc.Spreadsheets.Values.Update(s.spreadsheetID, rangeStr, vr).
			ValueInputOption("RAW").Context(ctx).Do()
		if err != nil {
			return nil, fmt.Errorf("Values.Update Категории ID: %w", err)
		}
	}

	return list, nil
}

// Document — документ.
type Document struct {
	IDКатегории string
	Название    string
	Описание    string
	Ссылка      string
	FileID      string // Telegram File_ID архива (ZIP) для повторной отправки
	SheetRow    int    // номер строки в листе (1-based) для обновления File_ID
}

// GetDocumentsByCategory возвращает документы по ID категории.
func (s *SheetsAPI) GetDocumentsByCategory(ctx context.Context, categoryID string) ([]Document, error) {
	rangeStr := sheetДокументы + "!A2:E"
	resp, err := s.svc.Spreadsheets.Values.Get(s.spreadsheetID, rangeStr).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("Values.Get Документы: %w", err)
	}
	var list []Document
	for i, row := range resp.Values {
		if len(row) < 4 {
			continue
		}
		idCat := strings.TrimSpace(strCell(row[0]))
		if idCat != categoryID {
			continue
		}
		d := Document{
			IDКатегории: idCat,
			Название:    strings.TrimSpace(strCell(row[1])),
			Описание:    strings.TrimSpace(strCell(row[2])),
			Ссылка:      strings.TrimSpace(strCell(row[3])),
			SheetRow:    2 + i,
		}
		if len(row) >= 5 {
			d.FileID = strings.TrimSpace(strCell(row[4]))
		}
		list = append(list, d)
	}
	return list, nil
}

// UpdateDocumentFileID записывает Telegram File_ID в колонку E для строки sheetRow.
func (s *SheetsAPI) UpdateDocumentFileID(ctx context.Context, sheetRow int, fileID string) error {
	rangeStr := fmt.Sprintf("%s!E%d", sheetДокументы, sheetRow)
	vr := &sheets.ValueRange{Values: [][]interface{}{{fileID}}}
	_, err := s.svc.Spreadsheets.Values.Update(s.spreadsheetID, rangeStr, vr).
		ValueInputOption("RAW").Context(ctx).Do()
	return err
}

// AppendWish добавляет запись в "Пожелания".
func (s *SheetsAPI) AppendWish(ctx context.Context, username, userID, text string) error {
	row := []interface{}{
		time.Now().Format("2006-01-02 15:04:05"),
		username,
		userID,
		text,
	}
	return s.appendRow(ctx, sheetПожелания, row)
}

// AppendIMO добавляет заявку в "Заявки_IMO".
func (s *SheetsAPI) AppendIMO(ctx context.Context, username, userID, fio, phone, position, source string) error {
	row := []interface{}{
		time.Now().Format("2006-01-02 15:04:05"),
		username,
		userID,
		fio,
		phone,
		position,
		source,
	}
	return s.appendRow(ctx, sheetЗаявкиIMO, row)
}

func (s *SheetsAPI) appendRow(ctx context.Context, sheet string, row []interface{}) error {
	rangeStr := sheet + "!A:Z"
	vr := &sheets.ValueRange{Values: [][]interface{}{row}}
	_, err := s.svc.Spreadsheets.Values.Append(s.spreadsheetID, rangeStr, vr).
		ValueInputOption("USER_ENTERED").InsertDataOption("INSERT_ROWS").Context(ctx).Do()
	return err
}

// EnsureUser добавляет пользователя в "Пользователи", если его ещё нет.
func (s *SheetsAPI) EnsureUser(ctx context.Context, userID, username string) error {
	rangeStr := sheetПользователи + "!A2:C"
	resp, err := s.svc.Spreadsheets.Values.Get(s.spreadsheetID, rangeStr).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("Values.Get Пользователи: %w", err)
	}
	idStr := userID
	for _, row := range resp.Values {
		if len(row) >= 1 && strCell(row[0]) == idStr {
			return nil
		}
	}
	row := []interface{}{userID, username, time.Now().Format("2006-01-02 15:04:05")}
	return s.appendRow(ctx, sheetПользователи, row)
}

// GetAdminChatIDs возвращает ID чатов админов с заполненным ID_Чата (для уведомлений).
func (s *SheetsAPI) GetAdminChatIDs(ctx context.Context) ([]int64, error) {
	chatIDs, _, err := s.GetAdmins(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(chatIDs))
	for id := range chatIDs {
		ids = append(ids, id)
	}
	return ids, nil
}

// GetAdmins возвращает множество ID чатов админов (по колонке ID_Чата) и юзернеймы.
// Админ может быть добавлен по юзернейму; ID_Чата заполняется при первом /start.
func (s *SheetsAPI) GetAdmins(ctx context.Context) (chatIDs map[int64]bool, usernames map[string]bool, err error) {
	rangeStr := sheetАдмины + "!A2:B"
	resp, err := s.svc.Spreadsheets.Values.Get(s.spreadsheetID, rangeStr).Context(ctx).Do()
	if err != nil {
		return nil, nil, fmt.Errorf("Values.Get Админы: %w", err)
	}
	chatIDs = make(map[int64]bool)
	usernames = make(map[string]bool)
	for _, row := range resp.Values {
		if len(row) >= 1 {
			usernames[strings.TrimSpace(strings.TrimPrefix(strCell(row[0]), "@"))] = true
		}
		if len(row) >= 2 {
			idStr := strings.TrimSpace(strCell(row[1]))
			if idStr != "" {
				var id int64
				if _, e := fmt.Sscanf(idStr, "%d", &id); e == nil {
					chatIDs[id] = true
				} else if f, e := strconv.ParseFloat(idStr, 64); e == nil {
					chatIDs[int64(f)] = true
				}
			}
		}
	}
	return chatIDs, usernames, nil
}

// SetAdminChatID обновляет ID_Чата для строки с данным юзернеймом, если ID_Чата пуст.
func (s *SheetsAPI) SetAdminChatID(ctx context.Context, username string, chatID int64) error {
	username = strings.TrimSpace(strings.TrimPrefix(username, "@"))
	rangeStr := sheetАдмины + "!A2:B"
	resp, err := s.svc.Spreadsheets.Values.Get(s.spreadsheetID, rangeStr).Context(ctx).Do()
	if err != nil {
		return err
	}
	for i, row := range resp.Values {
		if len(row) < 1 {
			continue
		}
		u := strings.TrimSpace(strings.TrimPrefix(strCell(row[0]), "@"))
		if !strings.EqualFold(u, username) {
			continue
		}
		if len(row) >= 2 && strings.TrimSpace(strCell(row[1])) != "" {
			return nil
		}
		rowNum := i + 2
		updateRange := fmt.Sprintf("%s!B%d", sheetАдмины, rowNum)
		vr := &sheets.ValueRange{Values: [][]interface{}{{fmt.Sprintf("%d", chatID)}}}
		_, err = s.svc.Spreadsheets.Values.Update(s.spreadsheetID, updateRange, vr).
			ValueInputOption("RAW").Context(ctx).Do()
		return err
	}
	return nil
}

// GetAllUserChatIDs возвращает все ID чатов из "Пользователи" (для рассылки).
// В листе хранится ID_Пользователя — в приватном чате с ботом chat_id = user_id, используем как есть.
func (s *SheetsAPI) GetAllUserChatIDs(ctx context.Context) ([]int64, error) {
	rangeStr := sheetПользователи + "!A2:B"
	resp, err := s.svc.Spreadsheets.Values.Get(s.spreadsheetID, rangeStr).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("Values.Get Пользователи: %w", err)
	}
	var ids []int64
	seen := make(map[int64]bool)
	for _, row := range resp.Values {
		if len(row) < 1 {
			continue
		}
		var id int64
		if _, e := fmt.Sscanf(strCell(row[0]), "%d", &id); e != nil {
			continue
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// LogError пишет в "Логи_Ошибок".
func (s *SheetsAPI) LogError(ctx context.Context, errStr, context string) {
	row := []interface{}{time.Now().Format("2006-01-02 15:04:05"), errStr, context}
	_ = s.appendRow(ctx, sheetЛогиОшибок, row)
}
