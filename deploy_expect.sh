#!/usr/bin/expect -f
# Деплой с автоматической подстановкой пароля. Запуск: ./deploy_expect.sh
# Пароль: 12345678 (при смене — поменять в переменной pass)

set timeout 120
set pass "12345678"
set host "root@45.8.145.249"
set dir "/opt/bugchat"
set script_dir [file dirname [info script]]
cd $script_dir

if {![file exists "app"]} {
    puts "Сборка app (linux/amd64)..."
    exec sh -c {GOOS=linux GOARCH=amd64 go build -o app .}
}

puts "1. Копирование app на сервер..."
spawn scp -o StrictHostKeyChecking=no -o ConnectTimeout=25 app $host:$dir/app
expect "password:"
send "$pass\r"
expect eof

puts "2. Установка systemd и запуск..."
spawn ssh -o StrictHostKeyChecking=no -o ConnectTimeout=25 $host "cp $dir/file_manager.service /etc/systemd/system/; systemctl daemon-reload; systemctl enable file_manager; systemctl start file_manager; sleep 2; systemctl status file_manager --no-pager"
expect "password:"
send "$pass\r"
expect eof

puts "\n3. Готово. Логи: ssh $host 'journalctl -u file_manager -f'"
