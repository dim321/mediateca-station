# `fetch_screens` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** По команде станции `fetch_screens` прочитать активные DHCP-аренды из dnsmasq и отправить пары MAC/IP в уже существующий `POST /api/agent/v1/command_results`.

**Architecture:** Парсер leases не знает про Hub, файл и ADB: на вход байты и время, на выход пары либо стабильный код ошибки. Чтение файла идёт через подменяемый reader. `commands.FetchScreens` только собирает `hub.CommandResult`. Агент маршрутизирует `type == fetch_screens` мимо `commands.Executor`, чтобы не трогать экран, ADB и VLC. Доставка остаётся в текущем `runCommands`: отдельной очереди результатов команд в репозитории нет (durable queue есть только у play events в `internal/report`). HTTP 200 помечает `command_id` выполненным; сеть, timeout и 5xx не помечают, и следующий poll читает файл снова. HTTP 401 по-прежнему ставит паузу токена. HTTP 422 пишется структурированным логом без секретов и тоже не помечает доставку. `runCommands` уже запускается из `pollCommands` в отдельной goroutine — этот порядок не переносить в основной цикл.

**Tech Stack:** Go 1.25 (`go.mod`), стандартная библиотека (`net/netip`, `encoding/json`, `net/http`), тесты `testing` + `httptest`. Форматтер — `gofmt`. Отдельного golangci-lint / Makefile в репозитории нет.

**Spec:** `docs/superpowers/specs/2026-10-13-station-agent-fetch-screens-implementation-spec.md`

Один план, не несколько. Парсер, чтение файла, JSON Hub и маршрутизация по отдельности не отвечают на команду.

## Global Constraints

- Команда `fetch_screens`: `screen_id` обязан быть `null`, `params` не содержит параметров.
- Экранные операции, ADB и VLC для неё не запускаются.
- Неизвестные типы команд продолжают обрабатываться по текущим правилам агента (`commands.Executor` → `unknown_type`).
- Файл: `/var/lib/misc/dnsmasq.leases`. Строка: `<expires> <mac_address> <ip_address> <hostname> <client_id>`.
- Пустые строки игнорировать. `expires = 0` — бессрочная аренда. Включать только бессрочные или неистёкшие на момент чтения (`expires > now`; `expires == now` уже истекла).
- Только шестибайтовый MAC в colon-форме и IPv4 host-адрес. MAC канонизировать в `aa:bb:cc:dd:ee:ff`.
- Полный повтор пары дедуплицировать. Активный MAC с разными IP или активный IP с разными MAC — ошибка всего файла, без частичного списка. Истёкшие строки в конфликте не участвуют.
- Непустая строка неправильной структуры или с некорректными обязательными полями — неуспех.
- Время парсера инъецируется. Тесты не читают настоящий `/var/lib/misc/dnsmasq.leases`.
- Успех: `ok: true`, `error: null`, `finished_at` — UTC RFC3339, `result.screens[]` с `mac_address` и `ip_address`. Пустой массив допустим. Порядок `screens` не контракт. Не отправлять hostname, expiry, client id.
- Ошибка: `ok: false`, поле `result` отсутствует. Коды: `lease_file_missing`, `lease_file_unreadable`, `invalid_lease_file`. В `error` нет пути, содержимого leases, токена и других секретов.
- `fetch_screens` с не-null `screen_id` не читает файл и не вызывает executor. Код ответа: `invalid_command` (это не код файла аренд).
- Bearer token и HTTP-клиент — существующие `hub.Client`.
- Доставлено только после HTTP 200. Сеть / timeout / 5xx оставляют команду непомеченной для повторного poll. Новую очередь результатов не создавать.
- HTTP 401 — `hub.ErrUnauthorized` и `pauseToken`, как у остальных команд.
- HTTP 422 — лог со `status` и `command_ids`, без тела ответа, пути и токена; команду не помечать.
- Повтор уже доставленного `command_id` — текущий `already_done` без повторного чтения файла. Повтор до HTTP 200 заново читает файл и не трогает эфир.
- Не блокировать воспроизведение, загрузку медиа и `play_events` на время retry.
- Broadcast Hub из этого задания не менять.
- Не запускать агент от root ради leases и не рекомендовать world-readable файл. В unit только добавить `SupplementaryGroups=dnsmasq-leases`, не ослабляя `User=mediateca` и `NoNewPrivileges=true`.
- Обнаружение экранов запускает оператор на Hub.
- Проверки репозитория: `gofmt -w` на изменённых Go-файлах и `go test ./...`. В финале также `go test -race ./...`.

---

## File map

| Path | Responsibility |
| --- | --- |
| `internal/leases/parse.go` | Байты + `time.Time` → `[]Pair` или `*CodeError`. Без сети, Hub, ADB и диска. |
| `internal/leases/parse_test.go` | Правила строк, срока, MAC/IPv4, дедупа и конфликтов. |
| `internal/leases/file.go` | Чтение пути через `Read`; отсутствие и ошибка чтения → коды файла. |
| `internal/leases/file_test.go` | Reader подменяется; настоящий lease-файл не открывается. |
| `internal/hub/client.go` | `screen_id: null` в `Command`; `CommandResult.Result`; `HTTPStatusError` для не-200. |
| `internal/hub/client_test.go` | Точный JSON успеха и ошибки; 422 / 5xx / сеть. |
| `internal/commands/fetch_screens.go` | Сборка `CommandResult` из пар или кода. Executor не менять. |
| `internal/commands/fetch_screens_test.go` | Контракт `result.screens` и отсутствие частичного списка. |
| `internal/agent/agent.go` | Поле `leaseFile`, ветка в `runCommands`, лог 422. |
| `internal/agent/agent_test.go` | Маршрут мимо executor, retry, идемпотентность, play events не ждут POST. |
| `README.md` | Доступ к leases, коды ошибок, `SupplementaryGroups`. |

`hub.Command.ScreenID` остаётся `int`. Все текущие литералы `ScreenID: 7` и `commands.Executor` не переписывать. Факт JSON `null` хранится в новом поле `ScreenAbsent bool `json:"-"``. Иначе `encoding/json` не может положить `null` в `int` и отбрасывает весь `GET /commands`.

---

### Task 1: Parser dnsmasq leases

**Files:**
- Create: `internal/leases/parse.go`
- Test: `internal/leases/parse_test.go`

**Interfaces:**
- Consumes: ничего
- Produces:
  - `type Pair struct { MAC string; IP string }`
  - `type CodeError struct { Code string }` с `Error() string` = `Code`
  - `const CodeMissing = "lease_file_missing"`
  - `const CodeUnreadable = "lease_file_unreadable"`
  - `const CodeInvalid = "invalid_lease_file"`
  - `func Parse(data []byte, now time.Time) ([]Pair, error)` — успех возвращает не-nil срез (пустой файл → `[]Pair{}`); ошибка возвращает `nil, *CodeError`

- [ ] **Step 1: Write the failing test**

Создать `internal/leases/parse_test.go`:

```go
package leases

import (
	"errors"
	"testing"
	"time"
)

func fixedNow() time.Time {
	return time.Unix(1_700_000_000, 0).UTC()
}

func requireInvalid(t *testing.T, data string) {
	t.Helper()
	pairs, err := Parse([]byte(data), fixedNow())
	if pairs != nil {
		t.Fatalf("pairs %+v", pairs)
	}
	var ce *CodeError
	if !errors.As(err, &ce) || ce.Code != CodeInvalid {
		t.Fatalf("err=%v", err)
	}
	if err.Error() != CodeInvalid {
		t.Fatalf("error string %q", err.Error())
	}
}

func TestParseOneActiveLease(t *testing.T) {
	data := "1791903600 AA:BB:CC:DD:EE:FF 192.168.1.10 living-room-tv *\r\n"
	pairs, err := Parse([]byte(data), fixedNow())
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 1 || pairs[0] != (Pair{MAC: "aa:bb:cc:dd:ee:ff", IP: "192.168.1.10"}) {
		t.Fatalf("%+v", pairs)
	}
}

func TestParseSeveralLeasesOrderIndependent(t *testing.T) {
	first := "" +
		"1791903600 11:22:33:44:55:66 10.0.0.2 beta *\n" +
		"1791903600 aa:bb:cc:dd:ee:ff 10.0.0.1 alpha *\n"
	second := "" +
		"1791903600 aa:bb:cc:dd:ee:ff 10.0.0.1 alpha *\n" +
		"1791903600 11:22:33:44:55:66 10.0.0.2 beta *\n"
	a, err := Parse([]byte(first), fixedNow())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse([]byte(second), fixedNow())
	if err != nil {
		t.Fatal(err)
	}
	got := map[Pair]int{}
	for _, p := range a {
		got[p]++
	}
	for _, p := range b {
		got[p]--
	}
	for _, n := range got {
		if n != 0 {
			t.Fatalf("a=%+v b=%+v", a, b)
		}
	}
	if len(a) != 2 || len(b) != 2 {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
}

func TestParsePermanentLease(t *testing.T) {
	pairs, err := Parse([]byte("0 aa:bb:cc:dd:ee:ff 192.168.1.10 host *\n"), fixedNow())
	if err != nil || len(pairs) != 1 {
		t.Fatalf("err=%v pairs=%+v", err, pairs)
	}
}

func TestParseExpiredExcluded(t *testing.T) {
	now := fixedNow()
	data := "1699999999 aa:bb:cc:dd:ee:01 10.0.0.1 old *\n" +
		"1700000000 aa:bb:cc:dd:ee:02 10.0.0.2 boundary *\n" +
		"1700000001 aa:bb:cc:dd:ee:03 10.0.0.3 future *\n"
	pairs, err := Parse([]byte(data), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 1 || pairs[0].MAC != "aa:bb:cc:dd:ee:03" || pairs[0].IP != "10.0.0.3" {
		t.Fatalf("%+v", pairs)
	}
}

func TestParseEmptyFile(t *testing.T) {
	pairs, err := Parse(nil, fixedNow())
	if err != nil || pairs == nil || len(pairs) != 0 {
		t.Fatalf("err=%v pairs=%#v", err, pairs)
	}
	pairs, err = Parse([]byte(""), fixedNow())
	if err != nil || pairs == nil || len(pairs) != 0 {
		t.Fatalf("err=%v pairs=%#v", err, pairs)
	}
}

func TestParseBlankLinesIgnored(t *testing.T) {
	data := "\n  \n\t\n0 aa:bb:cc:dd:ee:ff 192.168.1.10 host *\n\n"
	pairs, err := Parse([]byte(data), fixedNow())
	if err != nil || len(pairs) != 1 {
		t.Fatalf("err=%v pairs=%+v", err, pairs)
	}
}

func TestParseInvalidLines(t *testing.T) {
	cases := []string{
		"not-a-time aa:bb:cc:dd:ee:ff 192.168.1.10 host *",
		"-1 aa:bb:cc:dd:ee:ff 192.168.1.10 host *",
		"100 aa:bb:cc:dd:ee 192.168.1.10 host *",
		"100 aa:bb:cc:dd:ee:ff:01 192.168.1.10 host *",
		"100 AA-BB-CC-DD-EE-FF 192.168.1.10 host *",
		"100 aabbccddeeff 192.168.1.10 host *",
		"100 aa:bb:cc:dd:gg 192.168.1.10 host *",
		"100 aa:bb:cc:dd:ee:ff 192.168.1 host *",
		"100 aa:bb:cc:dd:ee:ff 192.168.1.256 host *",
		"100 aa:bb:cc:dd:ee:ff 192.168.001.010 host *",
		"100 aa:bb:cc:dd:ee:ff ::1 host *",
		"100 aa:bb:cc:dd:ee:ff 192.168.1.10/24 host *",
		"100 aa:bb:cc:dd:ee:ff 192.168.1.10 host",
		"100 aa:bb:cc:dd:ee:ff 192.168.1.10 host * extra",
	}
	for _, line := range cases {
		t.Run(line, func(t *testing.T) {
			requireInvalid(t, line+"\n")
		})
	}
}

func TestParseActiveMACIPConflicts(t *testing.T) {
	requireInvalid(t, ""+
		"0 aa:bb:cc:dd:ee:ff 192.168.1.10 a *\n"+
		"0 aa:bb:cc:dd:ee:ff 192.168.1.11 b *\n")
	requireInvalid(t, ""+
		"0 aa:bb:cc:dd:ee:01 192.168.1.10 a *\n"+
		"0 aa:bb:cc:dd:ee:02 192.168.1.10 b *\n")
}

func TestParseExpiredRowDoesNotConflict(t *testing.T) {
	data := "1 aa:bb:cc:dd:ee:ff 192.168.1.10 old *\n" +
		"0 aa:bb:cc:dd:ee:ff 192.168.1.11 current *\n"
	pairs, err := Parse([]byte(data), fixedNow())
	if err != nil || len(pairs) != 1 || pairs[0].IP != "192.168.1.11" {
		t.Fatalf("err=%v pairs=%+v", err, pairs)
	}
}

func TestParseDuplicatePairDeduped(t *testing.T) {
	data := "" +
		"0 aa:bb:cc:dd:ee:ff 192.168.1.10 host *\n" +
		"0 AA:BB:CC:DD:EE:FF 192.168.1.10 other *\n"
	pairs, err := Parse([]byte(data), fixedNow())
	if err != nil || len(pairs) != 1 {
		t.Fatalf("err=%v pairs=%+v", err, pairs)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/leases/ -count=1`

Expected: FAIL, сборка не находит пакет `mediateca-station/internal/leases` (`directory not found` / `no Go files`). После появления тестового файла без `parse.go` ожидаемый текст компилятора: `undefined: Parse`.

- [ ] **Step 3: Write minimal implementation**

Создать `internal/leases/parse.go`:

```go
package leases

import (
	"net/netip"
	"strconv"
	"strings"
	"time"
)

const (
	CodeMissing    = "lease_file_missing"
	CodeUnreadable = "lease_file_unreadable"
	CodeInvalid    = "invalid_lease_file"
)

type Pair struct {
	MAC string
	IP  string
}

type CodeError struct {
	Code string
}

func (e *CodeError) Error() string {
	if e == nil {
		return ""
	}
	return e.Code
}

func Parse(data []byte, now time.Time) ([]Pair, error) {
	deadline := now.UTC().Unix()
	seen := map[Pair]struct{}{}
	macIP := map[string]string{}
	ipMAC := map[string]string{}
	active := []Pair{}

	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 5 {
			return nil, invalid()
		}
		expires, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil || expires < 0 {
			return nil, invalid()
		}
		mac, err := canonicalMAC(fields[1])
		if err != nil {
			return nil, invalid()
		}
		ip, err := canonicalIPv4(fields[2])
		if err != nil {
			return nil, invalid()
		}
		if expires != 0 && expires <= deadline {
			continue
		}
		pair := Pair{MAC: mac, IP: ip}
		if _, ok := seen[pair]; ok {
			continue
		}
		if prev, ok := macIP[mac]; ok && prev != ip {
			return nil, invalid()
		}
		if prev, ok := ipMAC[ip]; ok && prev != mac {
			return nil, invalid()
		}
		seen[pair] = struct{}{}
		macIP[mac] = ip
		ipMAC[ip] = mac
		active = append(active, pair)
	}
	return active, nil
}

func invalid() error {
	return &CodeError{Code: CodeInvalid}
}

func canonicalMAC(raw string) (string, error) {
	parts := strings.Split(raw, ":")
	if len(parts) != 6 {
		return "", invalid()
	}
	var b [6]byte
	for i, part := range parts {
		if len(part) != 2 {
			return "", invalid()
		}
		n, err := strconv.ParseUint(part, 16, 8)
		if err != nil {
			return "", invalid()
		}
		b[i] = byte(n)
	}
	return sprintfMAC(b), nil
}

func sprintfMAC(b [6]byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, 17)
	for i := 0; i < 6; i++ {
		out[i*3] = hex[b[i]>>4]
		out[i*3+1] = hex[b[i]&0x0f]
		if i < 5 {
			out[i*3+2] = ':'
		}
	}
	return string(out)
}

func canonicalIPv4(raw string) (string, error) {
	addr, err := netip.ParseAddr(raw)
	if err != nil || !addr.Is4() {
		return "", invalid()
	}
	return addr.String(), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `gofmt -w internal/leases/parse.go internal/leases/parse_test.go && go test ./internal/leases/ -count=1`

Expected: `ok mediateca-station/internal/leases`

- [ ] **Step 5: Commit**

```bash
git add internal/leases/parse.go internal/leases/parse_test.go
git commit -m "$(cat <<'EOF'
feat(leases): parse active dnsmasq leases

EOF
)"
```

---

### Task 2: Чтение файла аренд

**Files:**
- Create: `internal/leases/file.go`
- Test: `internal/leases/file_test.go`

**Interfaces:**
- Consumes: `Parse`, `CodeMissing`, `CodeUnreadable`, `CodeInvalid`, `Pair`, `CodeError` из Task 1
- Produces:
  - `const DefaultPath = "/var/lib/misc/dnsmasq.leases"`
  - `type File struct { Path string; Read func(string) ([]byte, error); Now func() time.Time }`
  - `func (f File) Active() ([]Pair, error)`
  - `Read == nil` означает `os.ReadFile`. `Now == nil` означает `time.Now`. `Path == ""` означает `DefaultPath`
  - `os.ErrNotExist` → `*CodeError{Code: CodeMissing}`. Любая другая ошибка reader → `*CodeError{Code: CodeUnreadable}`
  - `Error()` этих значений равен только коду и не содержит путь

- [ ] **Step 1: Write the failing test**

Создать `internal/leases/file_test.go`:

```go
package leases

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestActiveMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.leases")
	_, err := (File{Path: path, Now: fixedNow}).Active()
	var ce *CodeError
	if !errors.As(err, &ce) || ce.Code != CodeMissing {
		t.Fatalf("%v", err)
	}
	if err.Error() != CodeMissing || stringsContainsPath(err.Error(), path) {
		t.Fatalf("error leaked path: %q", err.Error())
	}
}

func TestActiveUnreadableDirectory(t *testing.T) {
	dir := t.TempDir()
	_, err := (File{Path: dir, Now: fixedNow}).Active()
	var ce *CodeError
	if !errors.As(err, &ce) || ce.Code != CodeUnreadable {
		t.Fatalf("%v", err)
	}
	if stringsContainsPath(err.Error(), dir) {
		t.Fatalf("error leaked path: %q", err.Error())
	}
}

func TestActivePermissionViaReader(t *testing.T) {
	path := "/var/lib/misc/dnsmasq.leases"
	_, err := (File{
		Path: path,
		Now:  fixedNow,
		Read: func(string) ([]byte, error) {
			return nil, os.ErrPermission
		},
	}).Active()
	var ce *CodeError
	if !errors.As(err, &ce) || ce.Code != CodeUnreadable {
		t.Fatalf("%v", err)
	}
}

func TestActiveUsesInjectedReaderNotRealLeases(t *testing.T) {
	var got string
	pairs, err := (File{
		Path: DefaultPath,
		Now:  fixedNow,
		Read: func(path string) ([]byte, error) {
			got = path
			return []byte("0 aa:bb:cc:dd:ee:ff 10.0.0.2 host *\n"), nil
		},
	}).Active()
	if err != nil || got != DefaultPath || len(pairs) != 1 || pairs[0].IP != "10.0.0.2" {
		t.Fatalf("path=%s err=%v pairs=%+v", got, err, pairs)
	}
}

func stringsContainsPath(msg, path string) bool {
	return strings.Contains(msg, path)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/leases/ -count=1 -run 'TestActive'`

Expected: FAIL, `undefined: File`.

- [ ] **Step 3: Write minimal implementation**

Создать `internal/leases/file.go`:

```go
package leases

import (
	"errors"
	"os"
	"time"
)

const DefaultPath = "/var/lib/misc/dnsmasq.leases"

type File struct {
	Path string
	Read func(string) ([]byte, error)
	Now  func() time.Time
}

func (f File) Active() ([]Pair, error) {
	path := f.Path
	if path == "" {
		path = DefaultPath
	}
	read := f.Read
	if read == nil {
		read = os.ReadFile
	}
	now := f.Now
	if now == nil {
		now = time.Now
	}
	data, err := read(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, &CodeError{Code: CodeMissing}
		}
		return nil, &CodeError{Code: CodeUnreadable}
	}
	return Parse(data, now())
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `gofmt -w internal/leases/file.go internal/leases/file_test.go && go test ./internal/leases/ -count=1`

Expected: `ok mediateca-station/internal/leases`

- [ ] **Step 5: Commit**

```bash
git add internal/leases/file.go internal/leases/file_test.go
git commit -m "$(cat <<'EOF'
feat(leases): read the lease file through an injected reader

EOF
)"
```

---

### Task 3: JSON команд и результатов Hub

**Files:**
- Modify: `internal/hub/client.go` (`Command` около строк 60–77, `PostCommandResults` около строк 231–248)
- Test: `internal/hub/client_test.go`

**Interfaces:**
- Consumes: существующие `Command`, `CommandResult`, `PostCommandResults`, `FetchCommands`, `ErrUnauthorized`
- Produces:
  - `Command.ScreenAbsent bool `json:"-"`` — `true`, если в JSON `screen_id` отсутствует или равен `null`
  - `func (c *Command) UnmarshalJSON(data []byte) error`
  - `type ScreenEndpoint struct { MACAddress string `json:"mac_address"`; IPAddress string `json:"ip_address"` }`
  - `type ScreensResult struct { Screens []ScreenEndpoint `json:"screens"` }`
  - у `CommandResult` поле `Result *ScreensResult `json:"result,omitempty"``; `Error` остаётся без `omitempty`, чтобы успех сериализовал `"error":null`
  - `type HTTPStatusError struct { Status int; Op string }` и `func (e *HTTPStatusError) Error() string`
  - `PostCommandResults`: 401 → `ErrUnauthorized`; любой другой не-200 → `*HTTPStatusError{Status: code, Op: "command_results"}` без тела ответа; сеть → исходная ошибка `http.Client`, не `*HTTPStatusError`

Существующий `TestFetchCommands` и `TestPostCommandResults` не ломать: числовой `screen_id` по-прежнему заполняет `ScreenID`.

- [ ] **Step 1: Write the failing test**

Добавить в `internal/hub/client_test.go`:

```go
func TestFetchCommandsNumericAndNullScreenID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("auth %q", r.Header.Get("Authorization"))
		}
		io.WriteString(w, `{"commands":[`+
			`{"id":"cmd_screen","screen_id":7,"type":"wake","params":{},"issued_at":"2026-10-13T14:00:00Z"},`+
			`{"id":"cmd_station","screen_id":null,"type":"fetch_screens","params":{},"issued_at":"2026-10-13T14:00:00Z"}`+
			`]}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	c.HTTP = srv.Client()
	list, err := c.FetchCommands(context.Background(), "")
	if err != nil || len(list.Commands) != 2 {
		t.Fatalf("%v %+v", err, list)
	}
	screen := list.Commands[0]
	if screen.ScreenAbsent || screen.ScreenID != 7 || screen.Type != "wake" {
		t.Fatalf("%+v", screen)
	}
	station := list.Commands[1]
	if !station.ScreenAbsent || station.ScreenID != 0 || station.Type != "fetch_screens" {
		t.Fatalf("%+v", station)
	}
}

func TestPostCommandResultsScreensJSON(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agent/v1/command_results" {
			t.Fatalf("path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("auth %q", r.Header.Get("Authorization"))
		}
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	c.HTTP = srv.Client()
	err := c.PostCommandResults(context.Background(), []CommandResult{{
		CommandID:  "cmd_7b03539f-6ea8-4cf0-a719-1831b833f639",
		OK:         true,
		FinishedAt: time.Date(2026, 10, 13, 14, 0, 3, 0, time.UTC),
		Result: &ScreensResult{Screens: []ScreenEndpoint{{
			MACAddress: "aa:bb:cc:dd:ee:ff",
			IPAddress:  "192.168.1.10",
		}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := compactJSON(t, body)
	want := `{"results":[{"command_id":"cmd_7b03539f-6ea8-4cf0-a719-1831b833f639","ok":true,"error":null,"finished_at":"2026-10-13T14:00:03Z","result":{"screens":[{"mac_address":"aa:bb:cc:dd:ee:ff","ip_address":"192.168.1.10"}]}}]}`
	if got != want {
		t.Fatalf("got %s", got)
	}
}

func TestPostCommandResultsEmptyScreens(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	c.HTTP = srv.Client()
	err := c.PostCommandResults(context.Background(), []CommandResult{{
		CommandID:  "cmd_empty",
		OK:         true,
		FinishedAt: time.Date(2026, 10, 13, 14, 0, 3, 0, time.UTC),
		Result:     &ScreensResult{Screens: []ScreenEndpoint{}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compactJSON(t, body), `"screens":[]`) || strings.Contains(string(body), `"screens":null`) {
		t.Fatalf("%s", body)
	}
}

func TestPostCommandResultsErrorOmitsResult(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	c.HTTP = srv.Client()
	msg := "invalid_lease_file"
	err := c.PostCommandResults(context.Background(), []CommandResult{{
		CommandID:  "cmd_7b03539f-6ea8-4cf0-a719-1831b833f639",
		OK:         false,
		Error:      &msg,
		FinishedAt: time.Date(2026, 10, 13, 14, 0, 3, 0, time.UTC),
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := compactJSON(t, body)
	want := `{"results":[{"command_id":"cmd_7b03539f-6ea8-4cf0-a719-1831b833f639","ok":false,"error":"invalid_lease_file","finished_at":"2026-10-13T14:00:03Z"}]}`
	if got != want || strings.Contains(got, "screens") || strings.Contains(got, "result") {
		t.Fatalf("%s", got)
	}
}

func TestPostCommandResultsStatus422DoesNotIncludeBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		io.WriteString(w, `{"error":"aa:bb:cc:dd:ee:ff","token":"tok"}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	c.HTTP = srv.Client()
	err := c.PostCommandResults(context.Background(), []CommandResult{{
		CommandID: "cmd_1", OK: true, FinishedAt: time.Date(2026, 10, 13, 14, 0, 3, 0, time.UTC),
	}})
	var status *HTTPStatusError
	if !errors.As(err, &status) || status.Status != http.StatusUnprocessableEntity || status.Op != "command_results" {
		t.Fatalf("%v", err)
	}
	if strings.Contains(err.Error(), "aa:bb") || strings.Contains(err.Error(), "tok") {
		t.Fatalf("leaked %q", err.Error())
	}
}

func TestPostCommandResultsStatus5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	c.HTTP = srv.Client()
	err := c.PostCommandResults(context.Background(), []CommandResult{{
		CommandID: "cmd_1", OK: true, FinishedAt: time.Date(2026, 10, 13, 14, 0, 3, 0, time.UTC),
	}})
	var status *HTTPStatusError
	if !errors.As(err, &status) || status.Status != http.StatusBadGateway {
		t.Fatalf("%v", err)
	}
}

func TestPostCommandResultsNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	c := New(srv.URL, "tok")
	c.HTTP = srv.Client()
	srv.Close()
	err := c.PostCommandResults(context.Background(), []CommandResult{{
		CommandID: "cmd_1", OK: true, FinishedAt: time.Date(2026, 10, 13, 14, 0, 3, 0, time.UTC),
	}})
	var status *HTTPStatusError
	if err == nil || errors.As(err, &status) || errors.Is(err, ErrUnauthorized) {
		t.Fatalf("%v", err)
	}
}

func compactJSON(t *testing.T, raw []byte) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}
```

В импорты `internal/hub/client_test.go` добавить `"bytes"`, если его ещё нет. `"encoding/json"`, `"errors"`, `"io"`, `"net/http"`, `"strings"`, `"time"` уже есть.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/hub/ -count=1 -run 'TestFetchCommandsNumericAndNullScreenID|TestPostCommandResultsScreensJSON|TestPostCommandResultsEmptyScreens|TestPostCommandResultsErrorOmitsResult|TestPostCommandResultsStatus422DoesNotIncludeBody|TestPostCommandResultsStatus5xx|TestPostCommandResultsNetworkError'`

Expected: FAIL компиляции: `undefined: ScreenAbsent` / `undefined: ScreensResult` / `undefined: HTTPStatusError`.

- [ ] **Step 3: Write minimal implementation**

В `internal/hub/client.go` заменить `Command` и `CommandResult`:

```go
type Command struct {
	ID           string         `json:"id"`
	ScreenID     int            `json:"screen_id"`
	ScreenAbsent bool           `json:"-"`
	Type         string         `json:"type"`
	Params       map[string]any `json:"params"`
	IssuedAt     string         `json:"issued_at"`
}

func (c *Command) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID       string          `json:"id"`
		ScreenID json.RawMessage `json:"screen_id"`
		Type     string          `json:"type"`
		Params   map[string]any  `json:"params"`
		IssuedAt string          `json:"issued_at"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	c.ID = raw.ID
	c.Type = raw.Type
	c.Params = raw.Params
	c.IssuedAt = raw.IssuedAt
	if len(raw.ScreenID) == 0 || string(raw.ScreenID) == "null" {
		c.ScreenAbsent = true
		c.ScreenID = 0
		return nil
	}
	if err := json.Unmarshal(raw.ScreenID, &c.ScreenID); err != nil {
		return fmt.Errorf("screen_id: %w", err)
	}
	c.ScreenAbsent = false
	return nil
}

type ScreenEndpoint struct {
	MACAddress string `json:"mac_address"`
	IPAddress  string `json:"ip_address"`
}

type ScreensResult struct {
	Screens []ScreenEndpoint `json:"screens"`
}

type CommandResult struct {
	CommandID  string         `json:"command_id"`
	OK         bool           `json:"ok"`
	Error      *string        `json:"error"`
	FinishedAt time.Time      `json:"finished_at"`
	Result     *ScreensResult `json:"result,omitempty"`
}

type HTTPStatusError struct {
	Status int
	Op     string
}

func (e *HTTPStatusError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("hub %s: status %d", e.Op, e.Status)
}
```

Заменить хвост `PostCommandResults` после проверки `ErrUnauthorized`:

```go
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return &HTTPStatusError{Status: resp.StatusCode, Op: "command_results"}
	}
	return nil
```

Тело ответа в ошибку не читать и не логировать. `defer resp.Body.Close()` оставить как сейчас.

- [ ] **Step 4: Run test to verify it passes**

Run: `gofmt -w internal/hub/client.go internal/hub/client_test.go && go test ./internal/hub/ -count=1`

Expected: `ok mediateca-station/internal/hub`

- [ ] **Step 5: Commit**

```bash
git add internal/hub/client.go internal/hub/client_test.go
git commit -m "$(cat <<'EOF'
feat(hub): decode station commands and screen results

EOF
)"
```

---

### Task 4: Сборка результата `fetch_screens`

**Files:**
- Create: `internal/commands/fetch_screens.go`
- Test: `internal/commands/fetch_screens_test.go`
- Modify: не трогать `internal/commands/executor.go`

**Interfaces:**
- Consumes: `leases.Pair`, `leases.CodeError`, `leases.CodeMissing`, `leases.CodeUnreadable`, `leases.CodeInvalid`, `hub.Command`, `hub.CommandResult`, `hub.ScreenEndpoint`, `hub.ScreensResult`
- Produces: `func FetchScreens(cmd hub.Command, read func() ([]leases.Pair, error), now time.Time) hub.CommandResult`
  - `cmd.ScreenAbsent == false` → `ok: false`, `error: "invalid_command"`, `Result: nil`, `read` не вызывается
  - `read` вернул `*leases.CodeError` с одним из трёх кодов файла → этот код в `error`, `Result: nil` даже если срез пар не пустой
  - любая другая ошибка → `invalid_lease_file`, текст исходной ошибки в JSON не попадает
  - успех → `OK: true`, `Error: nil`, `Result.Screens` не nil; пустой ввод даёт пустой не-nil срез
  - `FinishedAt` = `now.UTC()`

- [ ] **Step 1: Write the failing test**

Создать `internal/commands/fetch_screens_test.go`:

```go
package commands

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"mediateca-station/internal/hub"
	"mediateca-station/internal/leases"
)

func fetchNow() time.Time {
	return time.Date(2026, 10, 13, 14, 0, 3, 0, time.UTC)
}

func TestFetchScreensSuccessJSON(t *testing.T) {
	res := FetchScreens(hub.Command{
		ID: "cmd_7b03539f-6ea8-4cf0-a719-1831b833f639", Type: "fetch_screens", ScreenAbsent: true,
	}, func() ([]leases.Pair, error) {
		return []leases.Pair{{MAC: "aa:bb:cc:dd:ee:ff", IP: "192.168.1.10"}}, nil
	}, fetchNow())
	if !res.OK || res.Error != nil || res.Result == nil {
		t.Fatalf("%+v", res)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"command_id":"cmd_7b03539f-6ea8-4cf0-a719-1831b833f639","ok":true,"error":null,"finished_at":"2026-10-13T14:00:03Z","result":{"screens":[{"mac_address":"aa:bb:cc:dd:ee:ff","ip_address":"192.168.1.10"}]}}`
	if string(raw) != want {
		t.Fatalf("%s", raw)
	}
}

func TestFetchScreensEmptyScreens(t *testing.T) {
	res := FetchScreens(hub.Command{ID: "cmd_empty", ScreenAbsent: true}, func() ([]leases.Pair, error) {
		return []leases.Pair{}, nil
	}, fetchNow())
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"screens":[]`) || strings.Contains(string(raw), "hostname") {
		t.Fatalf("%s", raw)
	}
}

func TestFetchScreensErrorOmitsPartialList(t *testing.T) {
	res := FetchScreens(hub.Command{ID: "cmd_bad", ScreenAbsent: true}, func() ([]leases.Pair, error) {
		return []leases.Pair{{MAC: "aa:bb:cc:dd:ee:ff", IP: "192.168.1.10"}}, &leases.CodeError{Code: leases.CodeInvalid}
	}, fetchNow())
	if res.OK || res.Result != nil || res.Error == nil || *res.Error != leases.CodeInvalid {
		t.Fatalf("%+v", res)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "screens") || strings.Contains(string(raw), "aa:bb") || strings.Contains(string(raw), "result") {
		t.Fatalf("%s", raw)
	}
}

func TestFetchScreensFileCodes(t *testing.T) {
	codes := []string{leases.CodeMissing, leases.CodeUnreadable, leases.CodeInvalid}
	for _, code := range codes {
		res := FetchScreens(hub.Command{ID: "cmd", ScreenAbsent: true}, func() ([]leases.Pair, error) {
			return nil, &leases.CodeError{Code: code}
		}, fetchNow())
		if res.Error == nil || *res.Error != code || res.Result != nil {
			t.Fatalf("%s %+v", code, res)
		}
	}
}

func TestFetchScreensUnknownReadErrorHidesText(t *testing.T) {
	res := FetchScreens(hub.Command{ID: "cmd", ScreenAbsent: true}, func() ([]leases.Pair, error) {
		return nil, errors.New("open /var/lib/misc/dnsmasq.leases: permission denied token=super-secret-token")
	}, fetchNow())
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error == nil || *res.Error != leases.CodeInvalid {
		t.Fatalf("%+v", res)
	}
	if strings.Contains(string(raw), "/var/lib") || strings.Contains(string(raw), "super-secret-token") {
		t.Fatalf("%s", raw)
	}
}

func TestFetchScreensNonNullScreenDoesNotRead(t *testing.T) {
	res := FetchScreens(hub.Command{ID: "cmd", ScreenID: 7, ScreenAbsent: false, Type: "fetch_screens"}, func() ([]leases.Pair, error) {
		t.Fatal("read called")
		return nil, nil
	}, fetchNow())
	if res.OK || res.Result != nil || res.Error == nil || *res.Error != "invalid_command" {
		t.Fatalf("%+v", res)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/commands/ -count=1 -run 'TestFetchScreens'`

Expected: FAIL, `undefined: FetchScreens`.

- [ ] **Step 3: Write minimal implementation**

Создать `internal/commands/fetch_screens.go`:

```go
package commands

import (
	"errors"
	"time"

	"mediateca-station/internal/hub"
	"mediateca-station/internal/leases"
)

func FetchScreens(cmd hub.Command, read func() ([]leases.Pair, error), now time.Time) hub.CommandResult {
	finished := now.UTC()
	if !cmd.ScreenAbsent {
		msg := "invalid_command"
		return hub.CommandResult{CommandID: cmd.ID, OK: false, Error: &msg, FinishedAt: finished}
	}
	pairs, err := read()
	if err != nil {
		msg := publicLeaseCode(err)
		return hub.CommandResult{CommandID: cmd.ID, OK: false, Error: &msg, FinishedAt: finished}
	}
	screens := make([]hub.ScreenEndpoint, 0, len(pairs))
	for _, pair := range pairs {
		screens = append(screens, hub.ScreenEndpoint{MACAddress: pair.MAC, IPAddress: pair.IP})
	}
	return hub.CommandResult{
		CommandID:  cmd.ID,
		OK:         true,
		FinishedAt: finished,
		Result:     &hub.ScreensResult{Screens: screens},
	}
}

func publicLeaseCode(err error) string {
	var ce *leases.CodeError
	if errors.As(err, &ce) {
		switch ce.Code {
		case leases.CodeMissing, leases.CodeUnreadable, leases.CodeInvalid:
			return ce.Code
		}
	}
	return leases.CodeInvalid
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `gofmt -w internal/commands/fetch_screens.go internal/commands/fetch_screens_test.go && go test ./internal/commands/ -count=1`

Expected: `ok mediateca-station/internal/commands`. Старые тесты executor остаются зелёными: `fetch_screens` в `Executor.Run` не добавлять.

- [ ] **Step 5: Commit**

```bash
git add internal/commands/fetch_screens.go internal/commands/fetch_screens_test.go
git commit -m "$(cat <<'EOF'
feat(commands): build fetch_screens command results

EOF
)"
```

---

### Task 5: Маршрутизация и доставка в агенте

**Files:**
- Modify: `internal/agent/agent.go` (импорты, `newAgent`, `runCommands`)
- Test: `internal/agent/agent_test.go` (`fakeHub`, новые тесты)

**Interfaces:**
- Consumes: `commands.FetchScreens`, `leases.File`, `leases.DefaultPath`, `hub.HTTPStatusError`, `hub.ErrUnauthorized`, `hub.Command.ScreenAbsent`, существующие `commandAlreadyDone` / `markCommandDone` / `pauseToken`
- Produces: поле `leaseFile leases.File` у `Agent`. В `runCommands` команда с `Type == "fetch_screens"` не передаётся в `commandRunner`. Успешный `ok: true` после HTTP 200 помечается done. `ok: false` после HTTP 200 не помечается (как и прочие неуспешные команды): Hub сам решает, слать ли id снова. Сеть и `*hub.HTTPStatusError` не помечают done. 401 вызывает `pauseToken`. 422 логируется как `hub command_results rejected` с атрибутами `status` и `command_ids`.

Поведение `already_done` не менять: после пометки done повторный poll не вызывает `FetchScreens` и не читает файл.

- [ ] **Step 1: Write the failing test**

В `fakeHub` добавить поле `holdResults func()`. Заменить `PostCommandResults`:

```go
func (f *fakeHub) PostCommandResults(_ context.Context, results []hub.CommandResult) error {
	if f.holdResults != nil {
		f.holdResults()
	}
	f.mu.Lock()
	f.commandResults = append(f.commandResults, results...)
	err := f.postResultsErr
	f.mu.Unlock()
	return err
}
```

Добавить импорты `"encoding/json"` и `"strings"` в `internal/agent/agent_test.go`, если их нет.

Добавить тесты:

```go
func stationFetch(id string) hub.Command {
	return hub.Command{ID: id, Type: "fetch_screens", ScreenAbsent: true}
}

func leaseLine() []byte {
	return []byte("0 aa:bb:cc:dd:ee:ff 192.168.1.10 living-room-tv *\n")
}

func TestFetchScreensDoesNotRunScreenExecutor(t *testing.T) {
	dir := t.TempDir()
	h := &fakeHub{}
	exec := &fakeCmdExec{}
	a, _ := newTestAgent(t, testCfg(dir), h, &fakePlayer{})
	a.cmdExec = exec
	a.now = func() time.Time { return time.Date(2026, 10, 13, 14, 0, 3, 0, time.UTC) }
	reads := 0
	a.leaseFile.Read = func(string) ([]byte, error) {
		reads++
		return leaseLine(), nil
	}
	raw := []byte(`{"commands":[{"id":"cmd_fetch","screen_id":null,"type":"fetch_screens","params":{},"issued_at":"2026-10-13T14:00:00Z"},{"id":"cmd_unknown","screen_id":7,"type":"nope","issued_at":"2026-10-13T14:00:00Z"}]}`)
	var resp hub.CommandsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	a.runCommands(context.Background(), resp.Commands)
	exec.mu.Lock()
	runs := append([]hub.Command(nil), exec.runs...)
	exec.mu.Unlock()
	if len(runs) != 1 || runs[0].ID != "cmd_unknown" || runs[0].Type != "nope" {
		t.Fatalf("runs %+v", runs)
	}
	if reads != 1 {
		t.Fatalf("reads %d", reads)
	}
	h.mu.Lock()
	posted := append([]hub.CommandResult(nil), h.commandResults...)
	h.mu.Unlock()
	if len(posted) != 2 || !posted[0].OK || posted[0].Result == nil || len(posted[0].Result.Screens) != 1 {
		t.Fatalf("%+v", posted)
	}
	if posted[0].Result.Screens[0].MACAddress != "aa:bb:cc:dd:ee:ff" || posted[0].Result.Screens[0].IPAddress != "192.168.1.10" {
		t.Fatalf("%+v", posted[0].Result.Screens)
	}
	if !a.commandAlreadyDone("cmd_fetch") {
		t.Fatal("successful fetch was not marked delivered")
	}
}

func TestRunCommandsFetchScreensDelivery(t *testing.T) {
	dir := t.TempDir()
	h := &fakeHub{postResultsErr: errors.New("dial tcp")}
	exec := &fakeCmdExec{}
	a, buf := newTestAgent(t, testCfg(dir), h, &fakePlayer{})
	a.cmdExec = exec
	reads := 0
	a.leaseFile.Read = func(string) ([]byte, error) {
		reads++
		return leaseLine(), nil
	}
	a.runCommands(context.Background(), []hub.Command{stationFetch("cmd_1")})
	if a.commandAlreadyDone("cmd_1") || reads != 1 {
		t.Fatalf("done=%v reads=%d", a.commandAlreadyDone("cmd_1"), reads)
	}

	h.mu.Lock()
	h.postResultsErr = &hub.HTTPStatusError{Status: 503, Op: "command_results"}
	h.mu.Unlock()
	a.runCommands(context.Background(), []hub.Command{stationFetch("cmd_1")})
	if a.commandAlreadyDone("cmd_1") || reads != 2 {
		t.Fatalf("done=%v reads=%d", a.commandAlreadyDone("cmd_1"), reads)
	}

	buf.Reset()
	h.mu.Lock()
	h.postResultsErr = &hub.HTTPStatusError{Status: 422, Op: "command_results"}
	h.mu.Unlock()
	a.runCommands(context.Background(), []hub.Command{stationFetch("cmd_1")})
	if a.commandAlreadyDone("cmd_1") || reads != 3 {
		t.Fatalf("done=%v reads=%d", a.commandAlreadyDone("cmd_1"), reads)
	}
	logText := buf.String()
	if !strings.Contains(logText, "422") || !strings.Contains(logText, "cmd_1") {
		t.Fatalf("log %s", logText)
	}
	if strings.Contains(logText, "super-secret-token") || strings.Contains(logText, "/var/lib/misc/dnsmasq.leases") || strings.Contains(logText, "aa:bb:cc:dd:ee:ff") {
		t.Fatalf("log leaked %s", logText)
	}

	h.mu.Lock()
	h.postResultsErr = nil
	h.mu.Unlock()
	a.runCommands(context.Background(), []hub.Command{stationFetch("cmd_1")})
	if !a.commandAlreadyDone("cmd_1") || reads != 4 {
		t.Fatalf("done=%v reads=%d", a.commandAlreadyDone("cmd_1"), reads)
	}
	a.runCommands(context.Background(), []hub.Command{stationFetch("cmd_1")})
	if reads != 4 {
		t.Fatalf("delivered command re-read the lease file, reads=%d", reads)
	}
	exec.mu.Lock()
	if len(exec.runs) != 0 {
		t.Fatalf("executor %+v", exec.runs)
	}
	exec.mu.Unlock()
}

func TestFetchScreensResult401Pauses(t *testing.T) {
	dir := t.TempDir()
	h := &fakeHub{postResultsErr: hub.ErrUnauthorized}
	a, _ := newTestAgent(t, testCfg(dir), h, &fakePlayer{})
	a.leaseFile.Read = func(string) ([]byte, error) { return leaseLine(), nil }
	a.runCommands(context.Background(), []hub.Command{stationFetch("cmd_1")})
	if !a.reportPaused || a.commandAlreadyDone("cmd_1") {
		t.Fatalf("paused=%v done=%v", a.reportPaused, a.commandAlreadyDone("cmd_1"))
	}
}

func TestFetchScreensRetryDoesNotBlockPlayEvents(t *testing.T) {
	dir := t.TempDir()
	entered := make(chan struct{})
	release := make(chan struct{})
	h := &fakeHub{
		cfg:            hub.Config{StationID: 1},
		notMod:         true,
		post:           201,
		postResultsErr: errors.New("dial tcp"),
		commands:       []hub.Command{stationFetch("cmd_1")},
		holdResults: func() {
			close(entered)
			<-release
		},
	}
	a, _ := newTestAgent(t, testCfg(dir), h, &fakePlayer{})
	a.cmdExec = &fakeCmdExec{}
	a.leaseFile.Read = func(string) ([]byte, error) { return leaseLine(), nil }
	ev := report.Event{ScreenID: 7, MediaAssetID: 19, StartedAt: time.Date(2026, 10, 13, 14, 0, 3, 0, time.UTC)}
	if err := a.queue.Enqueue(ev, "7:19:2026-10-13T14:00:03Z"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		a.poll(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("poll blocked on command_results")
	}
	if len(h.posts) != 1 || h.posts[0] != 201 {
		t.Fatalf("play posts %+v", h.posts)
	}
	close(release)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("command result was not attempted")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -count=1 -run 'TestFetchScreens|TestRunCommandsFetchScreensDelivery'`

Expected: FAIL. `a.leaseFile` не существует, поэтому тесты не компилируются (`a.leaseFile undefined`). Это и есть ожидаемый красный прогон.

- [ ] **Step 3: Write minimal implementation**

В импорты `internal/agent/agent.go` добавить `"net/http"` уже есть; добавить `"mediateca-station/internal/leases"`. Пакет `commands` уже импортирован.

В структуру `Agent` рядом с `cmdExec` добавить:

```go
	leaseFile leases.File
```

В `newAgent` сразу после `a := &Agent{ ... }` и до `return a, nil` (после `a.loadDisk()` тоже допустимо, лишь бы `a.now` уже был задан):

```go
	a.leaseFile = leases.File{
		Path: leases.DefaultPath,
		Now:  func() time.Time { return a.now() },
	}
```

Заменить `runCommands` целиком. Параметр назвать `cmds`, не `commands`: иначе вызов `commands.FetchScreens` попадёт в срез, а не в пакет.

```go
func (a *Agent) runCommands(ctx context.Context, cmds []hub.Command) {
	exec := a.commandExecutor()
	results := make([]hub.CommandResult, 0, len(cmds))
	for _, cmd := range cmds {
		if a.commandAlreadyDone(cmd.ID) {
			msg := "already_done"
			results = append(results, hub.CommandResult{
				CommandID:  cmd.ID,
				OK:         true,
				Error:      &msg,
				FinishedAt: time.Now().UTC(),
			})
			continue
		}
		if cmd.Type == "fetch_screens" {
			results = append(results, commands.FetchScreens(cmd, a.leaseFile.Active, a.now()))
			continue
		}
		results = append(results, exec.Run(ctx, cmd))
	}
	if err := a.hub.PostCommandResults(ctx, results); errors.Is(err, hub.ErrUnauthorized) {
		a.pauseToken()
		return
	} else if err != nil {
		var status *hub.HTTPStatusError
		if errors.As(err, &status) && status.Status == http.StatusUnprocessableEntity {
			a.log.Error("hub command_results rejected", "status", status.Status, "command_ids", commandIDs(results))
			return
		}
		a.log.Error("hub command_results", "err", err.Error(), "command_ids", commandIDs(results))
		return
	}
	for _, res := range results {
		if res.OK && (res.Error == nil || *res.Error != "already_done") {
			a.markCommandDone(res.CommandID)
		}
	}
}

func commandIDs(results []hub.CommandResult) []string {
	ids := make([]string, len(results))
	for i, res := range results {
		ids[i] = res.CommandID
	}
	return ids
}
```

`pollCommands` оставить с `go a.runCommands(...)`. Не добавлять запись результатов на диск и не ждать эту goroutine в `poll` / `tick` / `drain`.

- [ ] **Step 4: Run test to verify it passes**

Run: `gofmt -w internal/agent/agent.go internal/agent/agent_test.go && go test ./internal/agent/ -count=1`

Expected: `ok mediateca-station/internal/agent`

- [ ] **Step 5: Commit**

```bash
git add internal/agent/agent.go internal/agent/agent_test.go
git commit -m "$(cat <<'EOF'
feat(agent): route fetch_screens without blocking playback

EOF
)"
```

---

### Task 6: Документация доступа к leases и полный прогон

**Files:**
- Modify: `README.md` (unit около строк 73–78; после таблицы command types, перед `## Docker`)

**Interfaces:**
- Consumes: коды `lease_file_missing`, `lease_file_unreadable`, `invalid_lease_file`, `invalid_command`; путь `/var/lib/misc/dnsmasq.leases`; группа `dnsmasq-leases`
- Produces: операторская инструкция, по которой процесс читает leases не от root и не через world-readable файл

- [ ] **Step 1: Update the operator unit**

В блок `useradd` раздела Mini-PC, после создания пользователя `mediateca`, добавить:

```bash
sudo groupadd --system dnsmasq-leases
sudo usermod -a -G dnsmasq-leases mediateca
```

В unit, не удаляя `User=mediateca` и `NoNewPrivileges=true`, добавить одну строку:

```ini
[Service]
ExecStart=/usr/local/bin/mediateca-station -config /etc/mediateca-station/config.yaml
Restart=always
RestartSec=2
User=mediateca
SupplementaryGroups=dnsmasq-leases
NoNewPrivileges=true
```

`usermod -a -G` и `SupplementaryGroups=` нужны вместе: группа должна существовать до старта unit, иначе systemd не запустит сервис. Пустая группа на лабораторной машине без DHCP допустима.

- [ ] **Step 2: Document `fetch_screens`**

Сразу после таблицы per-screen команд и абзаца про HTTP 401 вставить раздел:

```markdown
### Station command: `fetch_screens`

Hub sends `fetch_screens` with `screen_id: null` and empty `params`. The
operator starts screen discovery on the Hub. Station does not start ADB, VLC,
or a screen stream for this command. It reads active DHCP leases and posts
`command_results`.

The process needs read access to `/var/lib/misc/dnsmasq.leases`. Do not run
the agent as root for this file. Do not make the lease file world-readable
(`chmod 644` or `666` is not the setup).

```bash
sudo chgrp dnsmasq-leases /var/lib/misc/dnsmasq.leases
sudo chmod 640 /var/lib/misc/dnsmasq.leases
```

dnsmasq rewrites the lease file and may reset its group. After a rewrite,
confirm the mode is still `640` and the group is still `dnsmasq-leases`.
Keep that ownership in the dnsmasq setup (service `Group=`, tmpfiles, or an
ACL owned by DHCP), not by loosening the station unit. Leave `User=mediateca`
and `NoNewPrivileges=true` in place. The only station-unit change for this
file is `SupplementaryGroups=dnsmasq-leases`.

`command_results.error` is one of these codes. The text does not include the
file path or lease rows:

| code | meaning |
| --- | --- |
| `lease_file_missing` | the lease file is not there |
| `lease_file_unreadable` | the file exists but could not be opened or read fully |
| `invalid_lease_file` | a row is malformed, a MAC or IPv4 is invalid, or two active rows disagree on a MAC or an IP |

`invalid_command` means `fetch_screens` arrived with a non-null `screen_id`.
No screen operation runs.
```

- [ ] **Step 3: Run the repository checks**

Run:

```bash
gofmt -l .
go test ./...
go test -race ./...
```

Expected: `gofmt -l` печатает пусто. Оба `go test` заканчиваются без `FAIL`. Отдельного linter-конфига в репозитории нет, golangci-lint не добавлять.

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "$(cat <<'EOF'
docs: explain lease-file access for fetch_screens

EOF
)"
```
