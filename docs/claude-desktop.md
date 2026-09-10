# Подключение rxmcp к Claude Desktop: пошагово

Инструкция для обычного пользователя Directum RX. Нужны: приложение Claude Desktop, адрес вашего RX и ваша учётная запись. Администратор не нужен, если у вас есть вход по паролю или вы умеете скопировать cookie из браузера (ниже показано, это две минуты).

## 1. Скачайте rxmcp

Со страницы [drxinfra.ru/rxmcp](https://drxinfra.ru/rxmcp) или из [Releases на GitHub](https://github.com/drxinfra/rxmcp/releases). Распакуйте архив в постоянное место, например:

- macOS: `~/Documents/rxmcp/rxmcp`
- Windows: `C:\Tools\rxmcp\rxmcp.exe`

**macOS** блокирует скачанные программы без подписи. Один раз в Терминале:

```sh
cd ~/Documents/rxmcp
xattr -d com.apple.quarantine rxmcp
chmod +x rxmcp
```

**Windows**: при первом запуске SmartScreen спросит, нажмите «Подробнее» → «Выполнить в любом случае».

## 2. Выберите способ входа

### Вариант А. Логин и пароль

Подходит, если вы входите в RX по логину и паролю (не через кнопку «Войти через корпоративный аккаунт»). Понадобятся `RXMCP_LOGIN` и `RXMCP_PASSWORD`.

### Вариант Б. Cookie из браузера

Подходит всем, в том числе при входе через Keycloak, SSO или домен. Пароль никуда не копируется, используется уже открытая сессия браузера. Минус: сессия живёт ограниченное время (обычно от нескольких часов до нескольких дней), потом cookie надо взять заново.

Как получить cookie в Chrome, Edge, Яндекс Браузере:

1. Войдите в RX в браузере как обычно.
2. Нажмите **F12** (или ⌥⌘I на macOS), откроется панель разработчика.
3. Вкладка **Application** (в русском интерфейсе «Приложение»). Слева раздел **Cookies**, выберите адрес вашего RX.
4. В таблице найдите строку **`sungero_client`**. Дважды щёлкните по ячейке **Value**, выделится всё значение, скопируйте (⌘C / Ctrl+C). Это длинная строка из букв и цифр.
5. Значение для настроек: `sungero_client=` плюс скопированная строка, без пробелов:

```
sungero_client=MIIDeg...длинная_строка...
```

В Safari: меню Разработка → Показать веб-инспектор → вкладка «Хранилище» → Cookies. Если меню «Разработка» нет, включите его в Настройки → Дополнения.

Cookie это ключ от вашей сессии. Не пересылайте её никому и не храните в общих местах.

## 3. Проверьте подключение (по желанию, но полезно)

В Терминале (macOS) или PowerShell (Windows). Команда `check` только читает и показывает, что доступно.

macOS, вариант с паролем:

```sh
cd ~/Documents/rxmcp
export RXMCP_URL=https://rx.company.ru/Integration
export RXMCP_LOGIN=ivanov
export RXMCP_PASSWORD='ваш пароль'
./rxmcp check
```

macOS, вариант с cookie (значение в одинарных кавычках, в нём есть точки с запятой):

```sh
export RXMCP_URL=https://rx.company.ru/Integration
export RXMCP_AUTH=cookie
export RXMCP_LOGIN=ivanov
export RXMCP_COOKIE='sungero_client=MIIDeg...'
./rxmcp check
```

Windows PowerShell:

```powershell
cd C:\Tools\rxmcp
$env:RXMCP_URL="https://rx.company.ru/Integration"
$env:RXMCP_AUTH="cookie"
$env:RXMCP_LOGIN="ivanov"
$env:RXMCP_COOKIE="sungero_client=MIIDeg..."
.\rxmcp.exe check
```

Адрес `RXMCP_URL` это адрес сервиса интеграции: обычно адрес вашего RX плюс `/Integration` (в некоторых установках `/integration`, регистр важен только если так настроил администратор; попробуйте оба). Ожидаемый результат: десять строк с галочками и ваше имя во второй строке.

## 4. Пропишите rxmcp в Claude Desktop

Откройте файл настроек: в Claude Desktop **Settings → Developer → Edit Config**. Файл называется `claude_desktop_config.json` и лежит:

- macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`
- Windows: `%APPDATA%\Claude\claude_desktop_config.json`

В новых версиях Claude Desktop в этом файле уже есть блок `"preferences"`. Не трогайте его, добавьте рядом блок `"mcpServers"`. Итоговый файл выглядит так (вариант с cookie):

```json
{
  "preferences": { "...": "то, что уже было в файле, оставьте как есть" },
  "mcpServers": {
    "rx": {
      "command": "/Users/ivanov/Documents/rxmcp/rxmcp",
      "env": {
        "RXMCP_URL": "https://rx.company.ru/Integration",
        "RXMCP_AUTH": "cookie",
        "RXMCP_LOGIN": "ivanov",
        "RXMCP_TZ": "Europe/Moscow",
        "RXMCP_COOKIE": "sungero_client=MIIDeg..."
      }
    }
  }
}
```

Вариант с паролем: вместо `RXMCP_AUTH` и `RXMCP_COOKIE` укажите `"RXMCP_PASSWORD": "ваш пароль"`.

На Windows путь пишется с двойными обратными косыми: `"C:\\Tools\\rxmcp\\rxmcp.exe"`.

Если файла нет или он пустой, вставьте только блок `mcpServers` в фигурных скобках. Команда `rxmcp install` печатает готовый фрагмент с вашими значениями.

Чтобы помощник мог не только читать, но и выполнять задания, отправлять и прекращать задачи, добавьте в `env` строку `"RXMCP_ALLOW_WRITE": "1"`. Перед каждым таким действием помощник пересказывает, что сделает.

## 5. Перезапустите Claude Desktop

Полностью: на macOS **⌘Q**, на Windows выход через значок в трее, затем запуск заново. Просто закрыть окно недостаточно.

В новом чате в списке подключений (значок инструментов под полем ввода) появится **rx**. Первые вопросы для проверки:

- «Кто я в RX и что у меня в работе?»
- «Что у меня просрочено?»
- «Найди документ … и перескажи его»

## 6. Если что-то не так

| Что видите | Что это значит | Что делать |
|---|---|---|
| «rx» нет в списке или помечен ошибкой | Claude не смог запустить rxmcp | Settings → Developer, там журнал сервера. Чаще всего неверный путь к файлу или сломанный JSON (потерянная кавычка или запятая) |
| «RX отклонил учётку (401)» | пароль неверный, у логина нет типа «пароль», или cookie истекла | Для cookie: возьмите новую по шагу 2 и замените в файле, перезапустите Claude |
| «нет связи с RX» | адрес недоступен с вашего компьютера | Проверьте, открывается ли RX в браузере, VPN, адрес в `RXMCP_URL` |
| «пользователь с логином … не найден» | логин в `RXMCP_LOGIN` не совпадает с логином в RX | Посмотрите логин в RX (профиль) или укажите `RXMCP_USER_ID` |
| При отправке задачи «нужен срок» | RX не создаёт задачу без срока | Назовите помощнику срок, например «завтра» |
| macOS: «не удаётся открыть, разработчик не проверен» | карантин не снят | Повторите команду `xattr` из шага 1 |

Вопросы и ошибки: [github.com/drxinfra/rxmcp/issues](https://github.com/drxinfra/rxmcp/issues).
