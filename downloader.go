package main

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

var ErrArchiveTooLarge = errors.New("archive exceeds 50 MB")

// BulkItem — URL и имя файла для bulk-архива.
type BulkItem struct {
	URL      string
	Filename string
}

func sanitizeCategoryForZip(s string) string {
	const bad = `/\:*?"<>|`
	out := strings.Map(func(r rune) rune {
		if strings.ContainsRune(bad, r) {
			return -1
		}
		if r == ' ' || r == '\t' {
			return '_'
		}
		return r
	}, s)
	out = strings.TrimSpace(out)
	if out == "" {
		return "archive"
	}
	return out
}

func sanitizeBulkFilename(s string) string {
	const bad = `/\:*?"<>|`
	out := strings.Map(func(r rune) rune {
		if strings.ContainsRune(bad, r) {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(out)
}

// BulkDownloadAndZip последовательно скачивает файлы в /tmp/bulk_{uuid}/, упаковывает в ZIP.
// maxArchiveBytes — лимит суммы размеров (50 МБ); при превышении — ErrArchiveTooLarge.
// minFreeBytes — минимум свободного места для старта.
// Возвращает (zipPath, bulkDir, nil). bulkDir нужно удалить (os.RemoveAll) после отправки.
// При любой ошибке bulkDir очищается внутри и возвращается ("", "", err).
func BulkDownloadAndZip(ctx context.Context, yandex *YandexDownloader, items []BulkItem, categoryName string, maxArchiveBytes, minFreeBytes int64) (zipPath, bulkDir string, err error) {
	if yandex == nil || len(items) == 0 {
		return "", "", fmt.Errorf("yandex or items empty")
	}
	tmp := os.TempDir()
	if free, _ := getFreeSpaceBytes(tmp); free < uint64(minFreeBytes) {
		return "", "", fmt.Errorf("not enough disk space")
	}
	baseDir := filepath.Join(tmp, "bulk_"+uuid.New().String())
	if err := os.MkdirAll(baseDir, 0700); err != nil {
		return "", "", err
	}
	cleanup := func() { _ = os.RemoveAll(baseDir) }

	used := make(map[string]bool)
	var writtenPaths []string
	var total int64

	for i, it := range items {
		base := sanitizeBulkFilename(it.Filename)
		if base == "" {
			base = "file_" + strconv.Itoa(i)
		}
		ext := filepath.Ext(base)
		baseNoExt := strings.TrimSuffix(base, ext)
		finalName := base
		counter := 0
		for used[finalName] {
			counter++
			if ext != "" {
				finalName = baseNoExt + "_" + strconv.Itoa(counter) + ext
			} else {
				finalName = baseNoExt + "_" + strconv.Itoa(counter)
			}
		}
		used[finalName] = true
		destPath := filepath.Join(baseDir, finalName)

		n, err := yandex.DownloadToFile(ctx, it.URL, destPath)
		if err != nil {
			cleanup()
			return "", "", err
		}
		total += n
		if total > maxArchiveBytes {
			cleanup()
			return "", "", ErrArchiveTooLarge
		}
		writtenPaths = append(writtenPaths, destPath)
	}

	zipName := sanitizeCategoryForZip(categoryName) + ".zip"
	if zipName == ".zip" {
		zipName = "archive.zip"
	}
	zipPath = filepath.Join(baseDir, zipName)
	zf, err := os.Create(zipPath)
	if err != nil {
		cleanup()
		return "", "", err
	}
	zw := zip.NewWriter(zf)
	for _, p := range writtenPaths {
		innerName := filepath.Base(p)
		fh := &zip.FileHeader{Name: innerName, Method: zip.Deflate}
		w, err := zw.CreateHeader(fh)
		if err != nil {
			_ = zw.Close()
			_ = zf.Close()
			cleanup()
			return "", "", err
		}
		f, err := os.Open(p)
		if err != nil {
			_ = zw.Close()
			_ = zf.Close()
			cleanup()
			return "", "", err
		}
		_, _ = io.Copy(w, f)
		_ = f.Close()
	}
	if err := zw.Close(); err != nil {
		_ = zf.Close()
		cleanup()
		return "", "", err
	}
	if err := zf.Close(); err != nil {
		cleanup()
		return "", "", err
	}
	return zipPath, baseDir, nil
}

// ZipBytesToTemp создаёт во временной папке /tmp/single_{uuid}/ файл из data, упаковывает его в ZIP.
// innerFilename — имя файла внутри архива; zipFilename — имя .zip. Возвращает (путь к zip, путь к папке для RemoveAll).
func ZipBytesToTemp(data []byte, innerFilename, zipFilename string) (zipPath, dir string, err error) {
	innerFilename = filepath.Base(innerFilename)
	if innerFilename == "" || innerFilename == "." {
		innerFilename = "document"
	}
	zipFilename = filepath.Base(zipFilename)
	if zipFilename == "" || zipFilename == "." {
		zipFilename = "archive.zip"
	}
	dir = filepath.Join(os.TempDir(), "single_"+uuid.New().String())
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", "", err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	rawPath := filepath.Join(dir, innerFilename)
	if err := os.WriteFile(rawPath, data, 0600); err != nil {
		cleanup()
		return "", "", err
	}
	zipPath = filepath.Join(dir, zipFilename)
	zf, err := os.Create(zipPath)
	if err != nil {
		cleanup()
		return "", "", err
	}
	zw := zip.NewWriter(zf)
	fh := &zip.FileHeader{Name: innerFilename, Method: zip.Deflate}
	w, err := zw.CreateHeader(fh)
	if err != nil {
		_ = zw.Close()
		_ = zf.Close()
		cleanup()
		return "", "", err
	}
	f, _ := os.Open(rawPath)
	_, _ = io.Copy(w, f)
	_ = f.Close()
	_ = zw.Close()
	_ = zf.Close()
	return zipPath, dir, nil
}
