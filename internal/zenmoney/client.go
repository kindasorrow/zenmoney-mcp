package zenmoney

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// BaseURL — единственный адрес, куда сервер ходит по сети.
const BaseURL = "https://api.zenmoney.ru"

const diffPath = "/v8/diff/"

// requestTimeout включает чтение тела: первый синк — один ответ в десятки мегабайт.
const requestTimeout = 2 * time.Minute

const errorBodyLimit = 512

var ErrUnauthorized = errors.New("zenmoney: токен не принят (401), получите новый на https://zerro.app/token")

// Client читает данные из ZenMoney API. Методов записи у него нет.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewClient создаёт клиент; baseURL отличается от BaseURL только в тестах.
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: requestTimeout,
			// Без редиректов: токен не должен уйти на другой хост, 3xx станет ошибкой статуса.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// diffRequest — только две метки. Тот же объект принимает и изменения, поэтому
// полей под сущности нет намеренно: отправить правку нельзя.
type diffRequest struct {
	CurrentClientTimestamp int64 `json:"currentClientTimestamp"`
	ServerTimestamp        int64 `json:"serverTimestamp"`
}

// Diff — изменения на сервере. Бюджеты, напоминания и пользователи не декодируются: отчётам они не нужны.
type Diff struct {
	ServerTimestamp int64         `json:"serverTimestamp"`
	Instrument      []Instrument  `json:"instrument"`
	Account         []Account     `json:"account"`
	Tag             []Tag         `json:"tag"`
	Merchant        []Merchant    `json:"merchant"`
	Transaction     []Transaction `json:"transaction"`
	Deletion        []Deletion    `json:"deletion"`
}

// Instrument — валюта. ShortTitle — трёхбуквенный код: RUB, USD.
type Instrument struct {
	ID         int    `json:"id"`
	ShortTitle string `json:"shortTitle"`
}

type Account struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Type       string  `json:"type"`
	Instrument int     `json:"instrument"`
	Balance    float64 `json:"balance"`
	Archive    bool    `json:"archive"`
}

// Tag — категория. Parent пуст у верхнего уровня; глубже одного уровня вложенности нет.
type Tag struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Parent string `json:"parent"`
}

type Merchant struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Transaction — операция. Суммы неотрицательные, в валюте своего счёта; Tag[0] — основная категория.
type Transaction struct {
	ID                string   `json:"id"`
	Date              string   `json:"date"`
	Income            float64  `json:"income"`
	Outcome           float64  `json:"outcome"`
	IncomeAccount     string   `json:"incomeAccount"`
	OutcomeAccount    string   `json:"outcomeAccount"`
	IncomeInstrument  int      `json:"incomeInstrument"`
	OutcomeInstrument int      `json:"outcomeInstrument"`
	Tag               []string `json:"tag"`
	Merchant          string   `json:"merchant"`
	Payee             string   `json:"payee"`
	Comment           string   `json:"comment"`
	Deleted           bool     `json:"deleted"`
}

// Deletion — окончательно удалённая сущность; Object — её тип: "account", "tag", "transaction".
type Deletion struct {
	ID     string `json:"id"`
	Object string `json:"object"`
}

// Diff запрашивает изменения после serverTimestamp. Ноль означает полный синк.
func (c *Client) Diff(ctx context.Context, serverTimestamp int64) (*Diff, error) {
	body, err := json.Marshal(diffRequest{
		CurrentClientTimestamp: time.Now().Unix(),
		ServerTimestamp:        serverTimestamp,
	})
	if err != nil {
		return nil, fmt.Errorf("zenmoney: encode diff request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+diffPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("zenmoney: build diff request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("zenmoney: diff request: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return nil, ErrUnauthorized
	default:
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		return nil, fmt.Errorf("zenmoney: diff: status %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}

	var diff Diff
	if err := json.NewDecoder(resp.Body).Decode(&diff); err != nil {
		return nil, fmt.Errorf("zenmoney: decode diff: %w", err)
	}

	return &diff, nil
}
