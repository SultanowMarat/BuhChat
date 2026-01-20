#!/bin/bash
# Скрипт для завершения деплоя, когда SSH снова доступен.
# Запускать из папки проекта: ./finish_deploy.sh

set -e
HOST="root@45.8.145.249"
DIR="/opt/bugchat"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

if [ ! -f app ]; then
  echo "Сборка app для linux/amd64..."
  GOOS=linux GOARCH=amd64 go build -o app .
fi

echo "1. Копирование бинарника app на сервер..."
scp -o StrictHostKeyChecking=no app "$HOST:$DIR/app"

echo "2. Установка systemd и запуск..."
ssh -o StrictHostKeyChecking=no "$HOST" "grep -q /usr/local/go/bin /root/.bashrc || echo 'export PATH=\$PATH:/usr/local/go/bin' >> /root/.bashrc; cp $DIR/file_manager.service /etc/systemd/system/; systemctl daemon-reload; systemctl enable file_manager; systemctl start file_manager; systemctl status file_manager --no-pager"

echo "3. Готово. Логи: ssh $HOST 'journalctl -u file_manager -f'"
