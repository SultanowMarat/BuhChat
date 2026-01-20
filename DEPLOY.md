# Деплой на сервер

## Данные для подключения

| Параметр | Значение |
|----------|----------|
| **IP**   | 45.8.145.249 |
| **Логин**| root |
| **Пароль** | 12345678 |

Рекомендуется сменить пароль после первого входа.

```bash
ssh root@45.8.145.249
```

---

## Первоначальная настройка на сервере

### 1. Установка Go (если нет)

```bash
# Пример для Linux (подставьте нужную версию)
wget https://go.dev/dl/go1.21.linux-amd64.tar.gz
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.21.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
source ~/.bashrc
```

### 2. Клонирование и настройка проекта

```bash
sudo mkdir -p /opt/bugchat
sudo chown "$USER" /opt/bugchat
cd /opt/bugchat
git clone https://github.com/SultanowMarat/BuhChat.git .
# или: git clone <ваш-репозиторий> .
```

### 3. Файлы конфигурации

- Скопируйте `credentials.json` (ключ Service Account для Google Sheets) в `/opt/bugchat/`.
- Создайте `.env` на основе `env.example` и заполните `BOT_TOKEN`, `SPREADSHEET_ID`, `CREDENTIALS_PATH`, `BOT_USERNAME` и при необходимости остальное.

### 4. Сборка

```bash
cd /opt/bugchat
go build -o app .
```

### 5. Systemd

```bash
sudo cp /opt/bugchat/file_manager.service /etc/systemd/system/
# Если проект лежит не в /opt/bugchat — отредактируйте WorkingDirectory, ExecStart и EnvironmentFile в файле
sudo systemctl daemon-reload
sudo systemctl enable file_manager
sudo systemctl start file_manager
sudo systemctl status file_manager
```

### 6. Проверка логов

```bash
journalctl -u file_manager -f
```

---

## Автообновление (deploy.sh)

Скрипт `deploy.sh` предназначен для запуска **на сервере** из папки проекта:

- `git fetch` + сравнение с `origin/main`
- при наличии изменений: `systemctl stop file_manager` → `git pull` → `go build -o app .` → `systemctl start file_manager`
- запись в лист **Логи_Сервера** в Google Sheets: «Обновление до версии {hash}»

```bash
cd /opt/bugchat
chmod +x deploy.sh
./deploy.sh
```

Для автоматического деплоя по расписанию (cron, каждые 5 минут):

```bash
crontab -e
# добавить:
*/5 * * * * /opt/bugchat/deploy.sh >> /var/log/deploy_bugchat.log 2>&1
```

---

## Лист «Логи_Сервера»

Создаётся автоматически при первом запуске (`EnsureSchema`). Колонки: **Дата | Уровень | Сообщение**.

В него пишутся:

- **Старт** — запуск бота  
- **Остановка** — остановка по SIGINT/SIGTERM  
- **Info** — обновление (из `./app -log "Обновление до версии X"`)  
- **Ошибка** — ошибки загрузки (одиночный файл, bulk, превышение лимита и т.п.)

---

## Важно

- В `file_manager.service` пути заточены под `/opt/bugchat` и бинарник `app`. При другой директории или имени бинарника отредактируйте unit-файл.
- Для `deploy.sh` и `./app -log` нужны `SPREADSHEET_ID` и `CREDENTIALS_PATH` в `.env`.
- Сервис перезапускается при падении (`Restart=always`, `RestartSec=5`).
