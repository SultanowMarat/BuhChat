#!/bin/bash
# deploy.sh — автообновление и перезапуск. Запускать на сервере из папки проекта.
# Использование: ./deploy.sh

set -e
PROJECT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$PROJECT_DIR"

git fetch origin
LOCAL=$(git rev-parse HEAD 2>/dev/null || true)
REMOTE=$(git rev-parse origin/main 2>/dev/null || true)

if [ -z "$LOCAL" ] || [ -z "$REMOTE" ]; then
  echo "Не удалось получить хэши (git rev-parse). Убедитесь, что ветка origin/main существует."
  exit 1
fi

if [ "$LOCAL" = "$REMOTE" ]; then
  echo "Изменений нет. Выход."
  exit 0
fi

echo "Обнаружены изменения. Останавливаю file_manager..."
systemctl stop file_manager || true

git pull origin main

echo "Сборка..."
export PATH="$PATH:/usr/local/go/bin"
go build -o app .

echo "Запуск file_manager..."
systemctl start file_manager

HASH=$(git rev-parse --short HEAD)
echo "Запись в Логи_Сервера: Обновление до версии $HASH"
# Вызов приложения в режиме -log для записи в Google Sheets
if [ -f .env ]; then set -a; . ./.env; set +a; fi
./app -log "Обновление до версии $HASH" 2>/dev/null || true

echo "Готово. Версия: $HASH"
