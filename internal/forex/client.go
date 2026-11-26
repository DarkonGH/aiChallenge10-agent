// Package forex получает валютные котировки и хранит накопленные замеры.
package forex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL — публичный spot-эндпоинт Coinbase. Ключ не нужен, ответ
// обычный JSON; это валютная пара, а не биржевой Forex-терминал.
const DefaultBaseURL = "https://api.coinbase.com/v2/prices"

// Tick — один замер валютной пары.
type Tick struct {
	Base      string  `json:"base"`
	Quote     string  `json:"quote"`
	Rate      float64 `json:"rate"`
	FetchedAt string  `json:"fetched_at"`
	Source    string  `json:"source,omitempty"`
}

// Client ходит за котировкой пары.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// NewClient возвращает клиент с таймаутом: планировщик не должен зависать
// на одном запросе.
func NewClient() *Client {
	return &Client{
		BaseURL: DefaultBaseURL,
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
}

// Spot запрашивает курс base/quote. Например, USD/RUB — сколько рублей за
// один доллар.
func (c *Client) Spot(ctx context.Context, base, quote string) (Tick, error) {
	base = strings.ToUpper(strings.TrimSpace(base))
	quote = strings.ToUpper(strings.TrimSpace(quote))
	if !validCurrency(base) || !validCurrency(quote) {
		return Tick{}, fmt.Errorf("некорректная валютная пара %q/%q", base, quote)
	}

	baseURL := c.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	url := strings.TrimRight(baseURL, "/") + "/" + base + "-" + quote + "/spot"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Tick{}, err
	}
	req.Header.Set("User-Agent", "goagent-forex/1.0")
	req.Header.Set("Accept", "application/json")

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return Tick{}, fmt.Errorf("Coinbase: %w", err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return Tick{}, fmt.Errorf("Coinbase: чтение ответа: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return Tick{}, fmt.Errorf("Coinbase: HTTP %d: %s", res.StatusCode, shortBody(body))
	}

	var payload struct {
		Data struct {
			Amount   string `json:"amount"`
			Base     string `json:"base"`
			Currency string `json:"currency"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Tick{}, fmt.Errorf("Coinbase: разбор JSON: %w", err)
	}
	rate, err := strconv.ParseFloat(strings.TrimSpace(payload.Data.Amount), 64)
	if err != nil || rate <= 0 {
		return Tick{}, fmt.Errorf("Coinbase: некорректный курс %q", payload.Data.Amount)
	}

	return Tick{
		Base:      base,
		Quote:     quote,
		Rate:      rate,
		FetchedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Source:    "Coinbase spot",
	}, nil
}

func validCurrency(code string) bool {
	if len(code) < 2 || len(code) > 10 {
		return false
	}
	for _, r := range code {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func shortBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	return text
}
