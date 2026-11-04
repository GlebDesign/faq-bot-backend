# Документация backend

Backend написан на Go. Сейчас он запускает HTTP-сервер, проверяет его состояние,
обрабатывает регистрацию и вход через Supabase, хранит пользователей и сессии
в PostgreSQL. Для создания таблиц есть отдельная команда миграций.

Начать лучше с [BACKEND.md](BACKEND.md): там указаны зависимости, настройки и команды запуска.

| Файл | Что в нём описано |
| --- | --- |
| [BACKEND.md](BACKEND.md) | Установка, переменные окружения, Docker и тесты |
| [ARCHITECTURE.md](ARCHITECTURE.md) | Основные папки и связь между ними |
| [PROJECT_WALKTHROUGH.md](PROJECT_WALKTHROUGH.md) | С каких файлов начать чтение кода |
| [HTTP.md](HTTP.md) | HTTP-маршруты, ответы и остановка сервера |
| [AUTH.md](AUTH.md) | Настройка Supabase, вход и работа с сессиями |
| [DATABASE.md](DATABASE.md) | Запуск PostgreSQL, таблицы и SQL-тесты |
| [MIGRATIONS.md](MIGRATIONS.md) | Создание и обновление схемы БД |
