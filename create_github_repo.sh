#!/bin/bash

# Скрипт для создания репозитория Buh_Chat_bot на GitHub

REPO_NAME="Buh_Chat_bot"
DESCRIPTION="Bug Chat Bot repository"
PRIVATE="${PRIVATE:-false}"

# Проверка наличия токена
if [ -z "$GITHUB_TOKEN" ]; then
    echo "Error: GITHUB_TOKEN environment variable is not set"
    echo "Please set it with: export GITHUB_TOKEN=your_token"
    exit 1
fi

# Создание репозитория через GitHub API
RESPONSE=$(curl -s -w "\n%{http_code}" -X POST \
    -H "Authorization: token $GITHUB_TOKEN" \
    -H "Accept: application/vnd.github.v3+json" \
    -H "Content-Type: application/json" \
    -d "{
        \"name\": \"$REPO_NAME\",
        \"description\": \"$DESCRIPTION\",
        \"private\": $PRIVATE
    }" \
    https://api.github.com/user/repos)

HTTP_CODE=$(echo "$RESPONSE" | tail -n1)
BODY=$(echo "$RESPONSE" | sed '$d')

if [ "$HTTP_CODE" -eq 201 ]; then
    echo "✅ Repository created successfully!"
    echo ""
    echo "$BODY" | grep -o '"html_url":"[^"]*"' | cut -d'"' -f4
else
    echo "Error: Failed to create repository (HTTP $HTTP_CODE)"
    echo "$BODY"
    exit 1
fi