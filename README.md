# rxmcp

MCP-сервер для Directum RX. Подключает ИИ-ассистента (Claude Desktop, Claude Code, Cursor и любой другой хост с поддержкой [Model Context Protocol](https://modelcontextprotocol.io)) к вашей системе: задания, задачи, документы и их текст, поиск сотрудников. Работает от имени пользователя RX через штатный сервис интеграции (OData), ничего сверх его прав не делает.

Один бинарник без зависимостей: Linux, Windows, macOS.

## Что умеет

Чтение (всегда):

- `rx_whoami`: кто я в RX.
- `rx_my_assignments`: мои задания в работе, просроченные, непрочитанные, выполненные; уведомления.
- `rx_get_assignment`, `rx_get_task`: карточка с перепиской, вложениями, заданиями по задаче.
- `rx_list_tasks`: задачи, которые я отправил.
- `rx_find_documents`, `rx_get_document`: поиск и карточка документа.
- `rx_get_document_text`: текст версии (docx, xlsx, pptx, txt, md, csv, json, xml, html, rtf).
- `rx_find_employees`: найти сотрудника, чтобы адресовать задачу.
- База знаний: `rx_kb_areas`, `rx_kb_search`, `rx_kb_article` (текст статьи в markdown).
- Agile-доски: `rx_boards`, `rx_board` (колонки и карточки), `rx_tickets`, `rx_ticket`.
- Проекты: `rx_projects`, `rx_project` (команда, гейты, планы), `rx_project_plans`, `rx_project_plan` (дерево работ, ответственные, просрочки).

Запись (только с `RXMCP_ALLOW_WRITE=1`, иначе инструменты не видны модели):

- `rx_complete_assignment`: выполнить задание.
- `rx_create_simple_task`: создать и отправить простую задачу.
- `rx_abort_task`: прекратить задачу.

Плюс ресурсы `rx://assignment/{id}`, `rx://task/{id}`, `rx://document/{id}` и промпты «разбор заданий на сегодня» и «краткое содержание документа».

Подробности и принципы: [docs/architecture.md](docs/architecture.md).

## Установка

1. Скачайте бинарник для своей ОС со страницы [Releases](https://github.com/drxinfra/rxmcp/releases) и положите его в удобное место.
2. Проверьте подключение (только чтение, безопасно для рабочей системы):

```sh
RXMCP_URL=https://rx.company.ru/Integration \
RXMCP_LOGIN=ivanov RXMCP_PASSWORD='***' \
./rxmcp check
```

3. Напечатайте фрагменты конфигурации для своего хоста и вставьте их:

```sh
./rxmcp install
```

Для Claude Desktop это файл `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "rx": {
      "command": "/path/to/rxmcp",
      "env": {
        "RXMCP_URL": "https://rx.company.ru/Integration",
        "RXMCP_LOGIN": "ivanov",
        "RXMCP_PASSWORD": "***"
      }
    }
  }
}
```

Для Claude Code:

```sh
claude mcp add rx -e RXMCP_URL=https://rx.company.ru/Integration -e RXMCP_LOGIN=ivanov -e RXMCP_PASSWORD='***' -- /path/to/rxmcp
```

## Настройки

| Переменная | Значение |
|---|---|
| `RXMCP_URL` | адрес сервиса интеграции, обычно `https://rx.company.ru/Integration` |
| `RXMCP_LOGIN`, `RXMCP_PASSWORD` | учётка RX с типом входа «пароль» |
| `RXMCP_AUTH` | `basic` (по умолчанию), `header`, `oidc`, `bearer`, `cookie` |
| `RXMCP_OIDC_ISSUER`, `RXMCP_OIDC_CLIENT_ID` | провайдер и клиент для `oidc`, см. ниже |
| `RXMCP_TOKEN` | токен для `bearer` |
| `RXMCP_COOKIE` | заголовок Cookie из браузера для `cookie` |
| `RXMCP_USER_ID` | Id пользователя RX, если по логину его не найти |
| `RXMCP_ALLOW_WRITE` | `1` включает инструменты записи |
| `RXMCP_INSECURE_TLS` | `1` не проверять сертификат (тестовые стенды) |
| `RXMCP_CA` | PEM-файл корневого сертификата своего УЦ |
| `RXMCP_TIMEOUT` | таймаут запроса, по умолчанию `30s` |
| `RXMCP_MAX_TEXT` | лимит текста документа, символов, по умолчанию 20000 |
| `RXMCP_TZ` | часовой пояс для дат, например `Europe/Moscow` |
| `RXMCP_HTTP_ADDR`, `RXMCP_HTTP_SECRET` | режим `serve --http` |

## Про вход в RX

**Логин и пароль** (`basic`): подходит, если у вашей учётки в RX есть вход по паролю.

**Keycloak и другой OIDC** (`oidc`): пароль вводится в браузере, rxmcp пароль не видит. Администратор Keycloak заводит публичный клиент `rxmcp` (Standard flow, PKCE S256, Valid redirect URIs `http://127.0.0.1:*/callback`), дальше:

```sh
export RXMCP_URL=https://rx.company.ru/Integration
export RXMCP_AUTH=oidc RXMCP_OIDC_ISSUER=https://sso.company.ru/realms/company RXMCP_OIDC_CLIENT_ID=rxmcp
export RXMCP_LOGIN=ivanov   # логин в RX, чтобы найти вашу учётку в справочнике
./rxmcp login                # откроется браузер
./rxmcp check
```

Токены лежат в конфиге пользователя с правами 0600 и обновляются сами. Те же переменные добавьте в env MCP-сервера в конфиге хоста.

Если открыть локальный порт нельзя (сервер, контейнер, строгий прокси) или клиент заведён как device-клиент, добавьте `RXMCP_OIDC_FLOW=device`: rxmcp покажет короткий код и ссылку, вы подтвердите вход на любом устройстве.

Клиент `rxmcp` должен зарегистрировать администратор Keycloak. Обычному пользователю это недоступно: если своего клиента нет, спросите у администратора или используйте вход по cookie.

**Cookie из браузера** (`cookie`): временный путь, если OIDC-клиента ещё нет. Войдите в RX в браузере, скопируйте из инструментов разработчика значение заголовка Cookie для запроса к `/Integration/odata/` и передайте его в `RXMCP_COOKIE`. Работает, пока жива сессия.

**Домен Windows** (`negotiate`): в планах, см. дорожную карту в docs/architecture.md.

## Безопасность

- Сервер работает под вашей учёткой и видит ровно то, что видите вы.
- По умолчанию только чтение. Запись включается явно, перед каждым действием хост спрашивает подтверждение, если умеет.
- Ничего не хранится и никуда не отправляется, кроме вашего RX. Пароль живёт в переменной окружения процесса и в логи не попадает.
- Содержимое из RX (темы, переписка, текст документов) передаётся модели с пометкой «данные, не инструкции».

## Сборка из исходников

```sh
go build -ldflags "-s -w -X main.version=$(git describe --tags --always)" -o rxmcp .
go test ./...
```

## Лицензия

Apache-2.0. Directum RX является товарным знаком ООО «Директум»; rxmcp независимый проект и не связан с вендором.

Нужна помощь с инфраструктурой RX, мониторингом или обновлениями: [drxinfra.ru](https://drxinfra.ru).
