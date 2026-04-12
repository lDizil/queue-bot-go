# Queue Bot (Go)

Telegram-бот для управления очередями студентов на сдачу лабораторных работ.

Бот автоматически открывает очередь по расписанию (с учётом чётности недели), рассылает уведомления за 5 минут и за 1 минуту до начала, после закрытия - сбрасывает всё для следующего раза. Очередь можно открыть и вне расписания одноразовой командой.

## Стек

| Компонент | Технология |
|-----------|-----------|
| Язык | Go 1.24 |
| Telegram API | [go-telegram/bot](https://github.com/go-telegram/bot) v1.19 |
| База данных | PostgreSQL 16 |
| Драйвер БД | pgx/v5 |
| Миграции | golang-migrate/migrate v4 |
| Конфигурация | Viper (`.env`) |
| Деплой | Docker Compose + GitHub Actions |

## Возможности

- Расписание с чётными / нечётными неделями
- Уведомления за 5 мин и за 1 мин до открытия очереди
- Retry-логика планировщика (до 3 попыток на каждое событие)
- Запись в очередь, выход, занятие первого свободного слота
- Очередь привязана к топику (`thread_id`) в супергруппе
- Одноразовая очередь `/one_shot_queue` - открывается немедленно, запись удаляется после закрытия
- Редактирование расписания прямо из Telegram (inline-меню)
- Middleware: AdminOnly, EditSession, QueueOpen

## Быстрый старт (локально)

### 1. Клонировать репозиторий

```bash
git clone https://github.com/<your-user>/queue-bot-go.git
cd queue-bot-go
```

### 2. Создать `.env`

Скопируй `.env.example` в `.env` и заполни:

```bash
cp .env.example .env
```

| Переменная | Описание |
|------------|----------|
| `BOT_TOKEN` | Токен бота от @BotFather |
| `BOT_CHAT_ID` | ID чата / супергруппы, куда бот постит очередь |
| `AUTHORIZED_USER_ID` | ID администраторов через запятую, напр. `123456,789012` |
| `QUEUEBOT_PORT` | Порт контейнера бота (напр. `5001`) |
| `DB_USER` | Имя пользователя PostgreSQL |
| `DB_PASSWORD` | Пароль PostgreSQL |
| `DB_NAME` | Имя базы данных |
| `DB_PORT` | Порт PostgreSQL (напр. `5432`) |
| `DB_HOST` | Хост БД; для Docker Compose - `postgres` |
| `PGADMIN_PORT` | Порт pgAdmin (напр. `5050`) |
| `PGADMIN_EMAIL` | Email для входа в pgAdmin |
| `PGADMIN_PASS` | Пароль для входа в pgAdmin |
| `PGADMIN_REQUIRE_LOGIN` | `True` в проде, `False` локально |
| `PGADMIN_REQUIRE_PASS` | `True` в проде, `False` локально |
| `TOTAL_SLOTS_IN_QUEUE` | Количество слотов в очереди |
| `AMOUNT_OF_BUTTONS_IN_ROW` | Число кнопок в строке inline-клавиатуры |
| `DELAY_QUEUE` | Задержка перед обновлением очереди (напр. `400ms`) |
| `EXIRED_SES_TIME` | Таймаут сессии редактирования (напр. `4m`) |
| `WEEK1_DATE` | Опорная дата для чётности недели (напр. `2026-04-10`) |
| `WEEK1_TYPE` | Тип опорной недели: `odd` или `even` |
| `SCHEDULER_TICK_INTERVAL` | Интервал тика планировщика (напр. `1s`) |

> **`DB_HOST`**: при запуске через Docker Compose должен быть `postgres` (имя сервиса).  
> Локально без Docker - `localhost`.

### 3. Запустить через Docker Compose

```bash
docker compose up -d --build
```

Контейнеры:

| Контейнер | Назначение |
|-----------|-----------|
| `queuebot` | Сам бот |
| `queuebot_db` | PostgreSQL |
| `queuebot_pgadmin` | pgAdmin 4 (веб-интерфейс БД) |

Миграции применяются автоматически при старте бота.

## Команды бота

### Пользователи

| Команда | Описание |
|---------|----------|
| `/start` | Показать приветствие |

### Администраторы

| Команда | Описание |
|---------|----------|
| `/edit_schedule` | Открыть меню редактирования расписания |
| `/one_shot_queue` | Открыть одноразовую очередь немедленно |

## Деплой на сервер (GitHub Actions)

Пуш в ветку `main` автоматически деплоит бота на сервер через SSH.

### Необходимые секреты в GitHub репозитории

| Секрет | Значение |
|--------|----------|
| `GO_BOT_SERVER_HOST` | IP или домен сервера |
| `GO_BOT_SERVER_USER` | SSH-пользователь |
| `GO_BOT_SSH_PRIVATE_KEY` | Приватный SSH-ключ |

### Настройка сервера (один раз)

1. **Сгенерировать SSH-ключ** (если нет):
   ```bash
   ssh-keygen -t ed25519 -C "github-actions"
   ```

2. **Добавить публичный ключ в `authorized_keys`**:
   ```bash
   cat ~/.ssh/id_ed25519.pub >> ~/.ssh/authorized_keys
   ```

3. **Добавить приватный ключ в GitHub Secrets** (`GO_BOT_SSH_PRIVATE_KEY`):
   ```bash
   cat ~/.ssh/id_ed25519
   ```

4. **Клонировать репозиторий на сервере**:
   ```bash
   git clone https://github.com/<your-user>/queue-bot-go.git ~/queue-bot-go
   ```

5. **Создать `.env` на сервере**:
   ```bash
   cp ~/queue-bot-go/.env.example ~/queue-bot-go/.env
   # Заполнить .env
   ```

После этого любой пуш в `main` автоматически пересоберёт и перезапустит контейнеры.

## Структура проекта

```
queue-bot-go/
├── cmd/server/main.go              # Точка входа, регистрация хендлеров
├── config/config.go                # Загрузка конфига через Viper
├── db/
│   ├── db.go                       # Подключение к БД, запуск миграций
│   ├── models.go                   # Модели данных
│   ├── queue_repo.go               # CRUD для очереди
│   └── schedule_repo.go            # CRUD для расписания
├── handlers/                       # Обработчики команд и callback-ов
├── middleware/
│   ├── admin.go                    # AdminOnly
│   ├── chain.go                    # Цепочка middleware
│   ├── edit_session.go             # EditSession
│   └── queue_open.go               # QueueOpen
├── scheduler/scheduler.go          # Планировщик с retry-логикой
├── migrations/                     # SQL миграции (golang-migrate)
├── Dockerfile
├── docker-compose.yml
├── .env.example
└── .github/workflows/deploy.yml    # CI/CD
```
