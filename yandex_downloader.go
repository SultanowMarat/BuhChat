package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	ErrNotYandexDisk  = errors.New("url is not a Yandex Disk link")
	ErrDirectNotFound = errors.New("could not get direct download link")
	ErrFileTooLarge   = errors.New("file exceeds size limit")
)

// YandexDownloader получает прямую ссылку и скачивает файлы с Яндекс.Диска.
type YandexDownloader struct {
	client  *http.Client
	maxSize int64
}

// NewYandexDownloader создаёт загрузчик с лимитом размера в байтах.
func NewYandexDownloader(maxSizeBytes int64) *YandexDownloader {
	return &YandexDownloader{
		client: &http.Client{
			Timeout: 120 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		},
		maxSize: maxSizeBytes,
	}
}

// isYandexDiskURL возвращает true, если ссылка ведёт на disk.yandex.
func isYandexDiskURL(s string) bool {
	return strings.Contains(s, "disk.yandex.ru") || strings.Contains(s, "disk.yandex.com")
}

// reDirectURL ищет ссылку на downloader.disk.yandex в HTML/тексте.
var reDirectURL = regexp.MustCompile(`https://downloader\.disk\.yandex\.[a-z.]+/disk/[^"'\s<>]+`)

// GetFileSize возвращает размер файла в байтах по публичной ссылке Яндекс.Диска.
// Для не-Яндекс URL возвращает ErrNotYandexDisk. Если Content-Length неизвестен, возвращает -1, nil.
func (y *YandexDownloader) GetFileSize(ctx context.Context, shareURL string) (int64, error) {
	shareURL = strings.TrimSpace(shareURL)
	if shareURL == "" {
		return 0, fmt.Errorf("пустая ссылка")
	}
	if !isYandexDiskURL(shareURL) {
		return 0, ErrNotYandexDisk
	}
	direct, err := y.GetDirectURL(ctx, shareURL)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, direct, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; rv:109.0) Gecko/20100101 Firefox/119.0")
	resp, err := y.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound {
		return 0, fmt.Errorf("HEAD %s: %d", direct, resp.StatusCode)
	}
	if resp.ContentLength >= 0 {
		return resp.ContentLength, nil
	}
	return -1, nil
}

// GetDirectURL возвращает прямую ссылку на скачивание. Для Yandex Диска — URL downloader.disk.yandex; для остальных — исходный URL.
func (y *YandexDownloader) GetDirectURL(ctx context.Context, shareURL string) (string, error) {
	shareURL = strings.TrimSpace(shareURL)
	if shareURL == "" {
		return "", fmt.Errorf("пустая ссылка")
	}
	if !isYandexDiskURL(shareURL) {
		return shareURL, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, shareURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; rv:109.0) Gecko/20100101 Firefox/119.0")
	resp, err := y.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "" && strings.Contains(loc, "downloader.disk.yandex") {
		return loc, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	direct := reDirectURL.FindString(string(body))
	if direct != "" {
		return direct, nil
	}
	// Fallback: Cloud API для публичных ресурсов (не требует OAuth)
	if href := y.getDirectViaCloudAPI(ctx, shareURL); href != "" {
		return href, nil
	}
	return "", ErrDirectNotFound
}

// getDirectViaCloudAPI получает прямую ссылку через Cloud API (public_key).
// Возвращает пустую строку при ошибке.
func (y *YandexDownloader) getDirectViaCloudAPI(ctx context.Context, shareURL string) string {
	u := "https://cloud-api.yandex.net/v1/disk/public/resources/download?public_key=" + url.QueryEscape(shareURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; rv:109.0) Gecko/20100101 Firefox/119.0")
	resp, err := y.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var out struct {
		Href string `json:"href"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<14)).Decode(&out) != nil {
		return ""
	}
	return strings.TrimSpace(out.Href)
}

// GetFile скачивает файл по публичной ссылке Яндекс.Диска.
// Возвращает: данные, имя файла, ошибка. При ошибке или размер > maxSize вызывающий отправит ссылку текстом.
func (y *YandexDownloader) GetFile(ctx context.Context, shareURL string) (data []byte, filename string, err error) {
	shareURL = strings.TrimSpace(shareURL)
	if !isYandexDiskURL(shareURL) {
		return nil, "", ErrNotYandexDisk
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, shareURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; rv:109.0) Gecko/20100101 Firefox/119.0")

	resp, err := y.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	// Редирект сразу на скачивание.
	if loc := resp.Header.Get("Location"); loc != "" && (strings.Contains(loc, "downloader.disk.yandex") || strings.HasPrefix(loc, "https://")) {
		return y.downloadByURL(ctx, loc)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, "", err
	}
	direct := reDirectURL.FindString(string(body))
	if direct == "" {
		if href := y.getDirectViaCloudAPI(ctx, shareURL); href != "" {
			return y.downloadByURL(ctx, href)
		}
		return nil, "", ErrDirectNotFound
	}
	return y.downloadByURL(ctx, direct)
}

func (y *YandexDownloader) downloadByURL(ctx context.Context, downloadURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, downloadURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; rv:109.0) Gecko/20100101 Firefox/119.0")

	resp, err := y.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound {
		return nil, "", fmt.Errorf("HEAD %s: %d", downloadURL, resp.StatusCode)
	}

	size := resp.ContentLength
	if size > 0 && size > y.maxSize {
		return nil, "", ErrFileTooLarge
	}

	filename := "document"
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if i := strings.Index(cd, "filename="); i >= 0 {
			s := strings.Trim(cd[i+9:], " \"'")
			if end := strings.IndexAny(s, "; \t\n"); end > 0 {
				s = s[:end]
			}
			if s != "" {
				filename = s
			}
		}
	}

	reqGet, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, "", err
	}
	reqGet.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; rv:109.0) Gecko/20100101 Firefox/119.0")

	respGet, err := y.client.Do(reqGet)
	if err != nil {
		return nil, "", err
	}
	defer respGet.Body.Close()

	if respGet.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("GET %s: %d", downloadURL, respGet.StatusCode)
	}

	// Ограничение по размеру при чтении.
	limit := y.maxSize
	if respGet.ContentLength > 0 && respGet.ContentLength > limit {
		return nil, "", ErrFileTooLarge
	}
	r := io.LimitReader(respGet.Body, limit+1)
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > y.maxSize {
		return nil, "", ErrFileTooLarge
	}
	return data, filename, nil
}
