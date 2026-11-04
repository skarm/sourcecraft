# SourceCraft Go SDK

[English](README.md) · [Русский](README.ru.md)

Go-клиент для REST API SourceCraft: репозитории, задачи, pull request, CI/CD и безопасность кода через AppSec. Требуется **Go 1.26 или новее**. Внешних зависимостей нет.

`Client` покрывает 268 операций платформы, `AppSecClient` покрывает 27 операций API безопасности. Методы и модели сгенерированы из [сохранённых спецификаций](api/README.ru.md) от 4 ноября 2026 года.

## Установка

Добавьте SDK в Go-проект:

```sh
go get github.com/skarm/sourcecraft
```

## Первый запрос

Задайте персональный токен доступа (PAT) с правами на нужные ресурсы:

```sh
export SOURCECRAFT_TOKEN='your_access_token_here'
```

Создайте клиент и получите репозиторий по slug организации и репозитория:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/skarm/sourcecraft"
)

func main() {
	client, err := sourcecraft.NewClient(os.Getenv("SOURCECRAFT_TOKEN"))
	if err != nil {
		log.Fatal(err)
	}

	repo, _, err := client.GetRepository(
		context.Background(), "my-org", "my-repo",
	)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(repo.Slug, repo.WebURL)
}
```

По умолчанию HTTP-запрос ограничен 30 секундами. Для ограничения всей последовательности запросов передайте контекст с тайм-аутом или дедлайном. Готовый клиент можно использовать из нескольких горутин.

Дальнейшие фрагменты рассчитаны на функцию, возвращающую `error`. В них `client` уже создан, а `ctx` содержит контекст операции.

## Поля запросов

Необязательные скалярные поля задаются указателями: `nil` пропускает поле, `new(false)` отправляет `false`, `new("")` отправляет пустую строку. Для списков `nil` пропускает поле, а `[]T{}` отправляет пустой массив.

Например, очистите описание и список ссылок репозитория:

```go
_, _, err := client.UpdateRepository(ctx, "my-org", "my-repo",
	&sourcecraft.UpdateRepositoryBody{
		Description: new(""),
		Links:       []sourcecraft.Link{},
	}, nil,
)
if err != nil {
	return err
}
```

Для указателей используйте встроенный `new(value)` из Go 1.26. Обёртка `sourcecraft.Ptr` сохранена для совместимости и помечена устаревшей.

## Пагинация

`Paginate` запрашивает страницы по мере чтения. Передавайте полученный токен в следующий запрос, сохраняя остальные параметры:

```go
fetch := func(
	ctx context.Context, token string,
) ([]sourcecraft.Repository, string, error) {
	page, _, err := client.ListMyRepositories(ctx,
		&sourcecraft.ListMyRepositoriesOptions{PageSize: 100, PageToken: token},
	)
	if err != nil {
		return nil, "", err
	}
	return page.Repositories, page.NextPageToken, nil
}
for repo, err := range sourcecraft.Paginate(ctx, fetch) {
	if err != nil {
		return err
	}
	fmt.Println(repo.Slug)
}
```

Итератор прекращает запросы при `break`, отмене контекста или ошибке. Повторяющийся токен страницы возвращает ошибку вместо бесконечного обхода.

## Ожидание асинхронных операций

Для слияния pull request, приглашений в организацию и изменений секретов используйте `WaitPullRequestMerge`, `WaitOrganizationInvites` и `WaitSecret`. Передайте ID, полученный при запуске операции:

```go
operation, _, err := client.WaitPullRequestMerge(ctx, operationID, time.Second)
if err != nil {
	return err
}
fmt.Println(operation.Status)
```

Задайте контексту дедлайн, чтобы ограничить общее время ожидания. Статусы `failed` и `cancel` возвращают `*sourcecraft.OperationFailedError`. После успешного `WaitOrganizationInvites` проверьте также `Response.Errors`: отдельные приглашения могут завершиться ошибкой.

## Загрузка файлов

Передайте открытый файл через `Upload`. SDK читает его потоком; закройте файл после завершения вызова:

```go
file, err := os.Open("report.txt")
if err != nil {
	return err
}
defer func() { _ = file.Close() }()

attachment, _, err := client.UploadIssueAttachment(ctx, "my-org", "my-repo", "1",
	&sourcecraft.Upload{Filename: "report.txt", Reader: file},
)
if err != nil {
	return err
}
fmt.Println(attachment.ID)
```

## Ошибки и HTTP-настройки

Методы возвращают типизированный результат, HTTP-метаданные и ошибку. Если метод не возвращает модель ответа, его результат имеет вид `(*Response, error)`. SDK сам читает и закрывает тело ответа.

Ответы с HTTP-статусом вне диапазона 200–299 возвращаются как `*sourcecraft.APIError`, если тело удалось прочитать в пределах лимита:

```go
if apiErr, ok := errors.AsType[*sourcecraft.APIError](err); ok {
	fmt.Println(apiErr.StatusCode, apiErr.ErrorCode, apiErr.RequestID)
	fmt.Println(string(apiErr.Body))
}
```

Ошибки чтения и превышения лимита возвращаются раньше разбора ошибки API. Если HTTP-ответ уже получен, его статус и заголовки доступны в `Response`. Транспортные ошибки и отмена контекста сохраняют исходную причину для `errors.Is`.

При создании клиента можно изменить тайм-аут, User-Agent и лимит тела ответа, равный по умолчанию 32 MiB:

```go
client, err := sourcecraft.NewClient(token,
	sourcecraft.WithHTTPClient(&http.Client{Timeout: time.Minute}),
	sourcecraft.WithUserAgent("my-service/1.0"),
	sourcecraft.WithMaxResponseBytes(64<<20),
)
if err != nil {
	return err
}
```

`WithBaseURL` задаёт адрес API, включая префикс пути. Клиент останавливает редиректы на другой origin и не повторяет запросы автоматически. Решение о повторе принимайте по ошибке, статусу и заголовкам ответа, например `Retry-After`.

## AppSec

Создайте отдельный клиент безопасности через `NewAppSecClient`. Он использует `DefaultAppSecBaseURL`, принимает те же HTTP-настройки и возвращает общий тип `APIError`:

```go
security, err := sourcecraft.NewAppSecClient(token)
if err != nil {
	return err
}
page, _, err := security.ListDefectGroups(ctx,
	&sourcecraft.AppSecListDefectGroupsOptions{
		GitRepo:  repositoryID,
		PageSize: 100,
		Severity: []string{"HIGH", "CRITICAL"},
	},
)
if err != nil {
	return err
}
for _, defect := range page.Data {
	fmt.Println(defect.UUID, defect.RuleName, defect.FileName)
}
```

В `GitRepo` передавайте UUID или внутренний числовой ID репозитория строкой. Для `ListRepositories` нужен `OrgID`. Модели и параметры AppSec имеют префикс `AppSec`.

Списки возвращают элементы в `Data` и токен продолжения в `NextPageToken`. Для обхода используйте `Paginate`. Нулевой `PageSize` выбирает значение 50; допустимый диапазон составляет 1–250. Необязательные query-параметры с указателями можно пропускать через `nil`.

## Запускаемые примеры

Команды запускаются из корня репозитория и читают `SOURCECRAFT_TOKEN` из окружения. В [руководстве по примерам](examples/README.ru.md) описаны параметры и предварительные условия:

- [Pull request](examples/pullrequests/main.go): создание, слияние и ожидание результата
- [Вложения](examples/attachments/main.go): загрузка файла и получение ссылки на скачивание
- [CI/CD](examples/cicd/main.go): запуск workflow, статусы, постраничные логи и ссылки на артефакты
- [Репозитории](examples/repositories/main.go): обход доступных репозиториев
- [AppSec](examples/appsec/main.go): обход групп уязвимостей

## Разработка и обновление API

Запустите линтер, тесты и проверки генерации из корня репозитория:

```sh
make lint
go test -race ./...
go vet ./...
go run ./internal/generate -check
go run ./internal/generate -spec api/appsec.openapi.json -check
go run ./internal/apicheck
```

Тесты проверяют запросы и модели по сохранённым спецификациям, ошибки HTTP, отмену, пагинацию и асинхронные операции. Они работают без токена SourceCraft и обращений к его API.

`go generate ./...` пересоздаёт методы и модели обоих клиентов из локальных снимков. Порядок обновления схем и публичного Go API описан в [руководстве по спецификациям](api/README.ru.md).

Для сравнения локальных снимков с опубликованными схемами выполните отдельную сетевую проверку:

```sh
go run ./internal/checkcontracts
```

Команда выводит путь к каталогу со сводкой и diff. Та же проверка запускается ежедневно и вручную в workflow [API contracts](.github/workflows/contracts.yml). Снимки и SDK автоматически не обновляются.
