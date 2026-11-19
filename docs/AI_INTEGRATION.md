# AI: задачи 001–018 и связь с Go

Локальная реализация для ревью. Общий protobuf находится в `contracts/proto/faq/ai/v1/ai.proto`, JSON-схема очереди и wire fixtures — в `contracts/ai/`. Python и Go используют сгенерированные файлы из одного контракта.

## Потоки данных

```text
Доверенный Go caller
  → knowledge.Store.Submit: metadata + job + SQL outbox в одной транзакции
  → Go knowledge-worker: persistent publish + publisher confirm
  → RabbitMQ faq.jobs / document.index|document.reindex|document.delete
  → Python worker: проверка metadata → parse → chunks → embeddings
  → одна SQL-транзакция: staging generation + complete flag + receipt/result
  → persistent result publish с confirm и mandatory → ACK исходного сообщения
  → Go knowledge-worker: проверка результата → atomic activation / failure
  → Python cleanup удаляет старые generation только после подтверждённой activation

Доверенный Go caller → aiclient.Generate → unary gRPC + service credential
  → SQL scope и active generation → LangChain/Ollama → проверенные цитаты
```

`account_id` и `agent_id` обязан определять авторизованный Go-сервис. Внутренние CLI и тесты передают синтетические UUID; это не публичные HTTP endpoints. Запрос без service credential отклоняется. Deadline обязателен. Поля bounded history описаны контрактом; непустая history пока даёт `UNIMPLEMENTED`, её обработка относится к AI-020.

HTTP-маршруты документов, браузерного чата, widget/partner и их auth/quota orchestration остаются отдельными backend-задачами. Существующие Auth/CRUD агентов не заменяются. Проверенная здесь связь — настоящие Go-пакеты, очередь, общая PostgreSQL metadata и gRPC, а не имитация готового интерфейса сайта.

## Владение данными

- Шесть существующих business migrations не изменены. Дополнительную knowledge schema применяет `backend/cmd/knowledge-migrate` после `cmd/migrate`; SQL хранится в `backend/internal/knowledge/schema.sql` и выполняется транзакционно, под migration lock. Повтор безопасен.
- Go владеет `knowledge.documents`, `knowledge.jobs`, `knowledge.outbox`. Scope связан FK с существующей `public.agents(account_id,id)`. Перед созданием задания проверяется живая запись агента; текущая knowledge revision повышается в той же транзакции.
- Python пишет только `ai.generations`, `ai.chunks`, `ai.receipts`. Роль `faq_ai_runtime` читает metadata через `knowledge.ai_document_metadata`; UPDATE business-таблиц ей не разрешён.
- Роль `faq_go_knowledge_runtime` пишет lifecycle-таблицы, но не AI-таблицы. Для проверки completed result ей доступны только нужные столбцы generation и chunk generation ID.
- `current_version` — целевая версия документа, `active_version/active_generation` — подтверждённая Go версия для поиска. Во время reindex старый активный корпус остаётся доступным. Failed reindex сохраняет его и записывает ошибку; успешный result атомарно переключает указатель.
- AI проверяет metadata перед чтением источника и повторно в транзакции записи. Go и Python берут один advisory transaction lock по account/agent/document. Tombstone, другой current job или версия запрещают старому заданию запись/активацию.
- Файлы поступают через read-only source mount. Storage key берётся из проверенной metadata, ограничен корнем, SHA-256 сверяется до parsing. Загрузка произвольных URL не поддерживается. Для object storage нужен отдельный адаптер с тем же контрактом.

## Надёжность очереди

Exchanges: `faq.jobs`, `faq.job-results`, `faq.ai.retry`, `faq.ai.dead`. Рабочая очередь `faq.ai.documents`, результаты Go — `faq.go.results`. Все durable, сообщения persistent. Результаты маршрутизируются по существующему соглашению Go: `jobs.completed` и `jobs.failed`.

`attempt` в canonical envelope — попытка dispatch от Go и часть identity. Локальные транспортные повторы используют header `x-faq-attempt`; они не подменяют ожидаемую Go attempt. Retry имеет TTL и DLX обратно в `faq.jobs`, максимум три попытки по умолчанию. После исчерпания валидное задание получает durable failed receipt, подтверждённый failed result для Go и DLQ-копию. Неразбираемый/неподдерживаемый envelope уходит в DLQ без выполнения эффекта.

Receipt хранит identity hash, owner, fencing token, lease и точный result. Lease продлевается во время обработки. После commit потерянная публикация повторяет сохранённый результат; эффект не выполняется второй раз. Никакого ACK до подтверждённой публикации. Heartbeat AMQP обслуживается отдельно от потока parsing/embeddings. Connection failures приводят к reconnect с паузой.

## Локальный запуск

Команды из корня репозитория, PowerShell. Нужны Docker Desktop, Python `.venv` из `ai/setup.ps1` и модели из [LOCAL_MODELS](../ai/LOCAL_MODELS.md). Если есть обычный Go, контейнерные `go run` можно выполнить им с соответствующим локальным DATABASE_URL.

```powershell
.\ai\.venv\Scripts\python.exe .\ai\scripts\bootstrap_integration.py
docker compose --env-file ai/.runtime/integration.env -f compose.ai.yml up -d postgres rabbitmq
```

Применить сначала business migrations, затем knowledge schema к отдельной БД стенда:

```powershell
docker run --rm --network faq-ai18_default --env-file ai/.runtime/integration.env `
  -v "${PWD}:/src" -v faq-ai18-go-pkg:/go/pkg -w /src/backend golang:1.24-bookworm `
  sh -c 'export DATABASE_URL="$GO_CONTAINER_DATABASE_URL"; go run ./cmd/migrate && go run ./cmd/knowledge-migrate'
.\ai\.venv\Scripts\python.exe .\ai\scripts\prepare_integration.py
.\ai\start-local.ps1
docker compose --env-file ai/.runtime/integration.env -f compose.ai.yml --profile services up -d --build
```

Bootstrap генерирует случайные локальные пароли и service token, сохраняет прежние значения при повторе. Файл `ai/.runtime/integration.env` исключён из Git и Docker build context. Admin URL используется только при миграциях и тестах, не передаётся в Python-контейнеры. Рабочие процессы используют разные ограниченные DB roles.

Порты привязаны к localhost: PostgreSQL 55432, AMQP 55672, RabbitMQ UI 55673, gRPC 50051; Ollama — существующий localhost 11434. Имена проекта/volumes начинаются с `faq-ai18`; чужие контейнеры не меняются. Три отдельных процесса: Python gRPC, Python worker, Go coordinator. Demo backend включается только явно, подмены при отказе Ollama нет.

Состояние и остановка только своего стенда:

```powershell
docker compose --env-file ai/.runtime/integration.env -f compose.ai.yml ps
docker compose --env-file ai/.runtime/integration.env -f compose.ai.yml --profile services stop
```

Volumes и исходные файлы сохраняются после stop. Для другого стенда нужны его учётные данные/пути и согласование core metadata с владельцем backend; локальные тестовые credentials для этого не используются.

## Проверки

Python unit + настоящая PostgreSQL/pgvector, с изолированными синтетическими строками:

```powershell
.\ai\.venv\Scripts\python.exe .\ai\scripts\test_integration.py
.\ai\.venv\Scripts\python.exe .\ai\scripts\generate_contracts.py --check
```

Для codegen нужны `grpcio-tools` из lock-файла и генераторы `protoc-gen-go v1.36.6`, `protoc-gen-go-grpc 1.5.1`. Сгенерированный Python использует protobuf 7.36.2 / grpcio-tools 1.85.3. Go использует protobuf 1.36.6 / grpc 1.71.0. Fixtures проверяют одинаковые wire bytes в двух языках.

Общий Go-прогон тестов включает legacy SQL-тесты и integration knowledge. Во время него coordinator нужно остановить: тесты сами проверяют dispatch/consume и временно снимают result binding для fault injection.

```powershell
docker compose --env-file ai/.runtime/integration.env -f compose.ai.yml stop coordinator
docker run --rm --memory 1g --network faq-ai18_default --env-file ai/.runtime/integration.env `
  -e AI_GRPC_ADDR=grpc:50051 -e AI_TEST_SOURCE_ROOT=/src/ai/.runtime/integration/sources `
  -v "${PWD}:/src" -v faq-ai18-go-pkg:/go/pkg -v faq-ai18-go-build:/root/.cache/go-build `
  -w /src/backend golang:1.24-bookworm sh -c 'export TEST_KNOWLEDGE_DATABASE_URL="$GO_CONTAINER_DATABASE_URL"; export TEST_DATABASE_URL="$GO_CONTAINER_DATABASE_URL"; export AI_AMQP_URL="$AI_CONTAINER_AMQP_URL"; go test ./... -count=1'
docker compose --env-file ai/.runtime/integration.env -f compose.ai.yml --profile services up -d coordinator
```

Тесты используют реальный RabbitMQ и restricted Python DB role. Проверяются TXT/MD/text/PDF/DOCX через очередь, Unicode и provenance, чужие account/agent, redelivery, потеря result publication, delayed retry, lease fencing, delete/late index и сохранение старой версии при неудачном reindex. Контрольный синтетический набор не является независимой оценкой качества LLM.

## Передача результата

AI-001–AI-018 реализованы для локальной проверки и ревью. Общий proto, metadata/queue contract и optional knowledge migration требуют ревью владельца Go-контрактов перед переносом на общий стенд. Интеграционный барьер G04, UI/HTTP, Coverage, история диалога, policy качества и rollout не объявляются закрытыми этим изменением.

## Состояние задач для ревью

| ID | Реализованный результат |
| --- | --- |
| AI-001 | Выбор generation/embedding моделей, лицензии, digest и ресурсы |
| AI-002 | Единый unary proto, ошибки, scope/version/history fields, Go/Python wire fixtures |
| AI-003 | Настоящие Qwen generation и embeddings, фактическая размерность 1024, RAM/latency |
| AI-004 | Раздельные gRPC/worker entrypoints, конфигурация, health, Docker, вызов из Go |
| AI-005 | Go-owned metadata view, active version, доверенный scope, fencing contract |
| AI-006 | UTF-8 TXT/MD/ручной текст, нормализация, typed ошибки |
| AI-007 | Текстовые PDF, page provenance, encrypted/scanned/corrupt/resource errors |
| AI-008 | DOCX paragraphs/headings/tables, zip и XML limits, без внешних ресурсов |
| AI-009 | Стабильные versioned chunk IDs, overlap, page/heading/offset |
| AI-010 | Pin модели, batches, finite/dimension validation, bounded embedding concurrency |
| AI-011 | Транзакционный staging в pgvector, AI-only write role, неактивные данные невидимы |
| AI-012 | AMQP manual ACK/prefetch, schema/size checks, heartbeat/reconnect |
| AI-013 | Durable receipts, lease/fence, восстановление сохранённого result |
| AI-014 | Persistent result publish с confirm/mandatory, typed failed result для Go |
| AI-015 | TTL retry/backoff, max attempts, failed result + DLQ; Go terminal state — failed |
| AI-016 | Чтение проверенного файла, parse/chunk/embed/stage/result через очередь |
| AI-017 | Scoped удаление, tombstone, защита от позднего index и потерявшего lease worker |
| AI-018 | Отдельная generation, atomic activation в Go, сохранение старой при ошибке, cleanup после переключения |

Статус таблицы — реализация и локальные проверки; командное ревью/приёмка ещё не выполнены.

## Фактический прогон 19.11.2026

- **85 Python-проверок прошли**, включая настоящую PostgreSQL/pgvector, DB privileges, mid-embedding delete race, model-version switch, receipts и gRPC.
- **Полный `go test ./... -count=1` прошёл** с настоящей БД; knowledge-пакет — 124,834 секунды. Использованы контейнерные Python gRPC/worker, RabbitMQ и настоящие Qwen, без подмены LLM.
- Отдельный `TestKnowledgeCoordinatorProcess` **прошёл за 3,12 секунды**: dispatch и activation выполнил самостоятельный Go-контейнер с restricted role.
- Подтверждены успешные TXT/MD/text/PDF/DOCX задания, отказ по corrupt PDF, цена 500 → неудачный reindex сохраняет 500 → успешный reindex даёт 600 → delete скрывает знания. Проверены redelivery, потеря result binding с восстановлением из receipt, TTL retry и отсутствие чужого agent.
- `pip check`, воспроизводимый codegen и `git diff --check` прошли. Оба Docker-образа собраны, пять контейнеров своего стенда оставлены запущенными.

На этом ноутбуке время зависит от параллельной нагрузки Docker и повторной загрузки модели. Прогон подтверждает перечисленные сценарии; это не независимый benchmark или нагрузочный тест.
