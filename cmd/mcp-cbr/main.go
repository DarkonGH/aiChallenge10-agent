package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/DarkonGH/aiChallenge10-agent/internal/cbr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// currencyRateArgs is the tool input schema. The SDK derives JSON Schema from
// the struct and validates arguments before the handler runs.
type currencyRateArgs struct {
	Currency string `json:"currency" jsonschema:"код валюты ЦБ РФ: USD, EUR, CNY, GBP, JPY"`
	Date     string `json:"date,omitempty" jsonschema:"дата в формате ДД.ММ.ГГГГ; пусто — сегодня"`
}

func main() {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "goagent-cbr",
		Version: "1.0.0",
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "currency_rate",
		Description: "Возвращает официальный курс валюты ЦБ РФ на указанную дату. " +
			"Используй для курсов USD, EUR, CNY, GBP, JPY; не придумывай курс сам.",
	}, currencyRate)

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("mcp-cbr: %v", err)
	}
}

func currencyRate(ctx context.Context, _ *mcp.CallToolRequest, args currencyRateArgs) (*mcp.CallToolResult, cbr.Rate, error) {
	currency := strings.ToUpper(strings.TrimSpace(args.Currency))
	if currency == "" {
		return nil, cbr.Rate{}, fmt.Errorf("currency обязателен: USD, EUR, CNY, GBP или JPY")
	}
	date := strings.TrimSpace(args.Date)
	if date == "" {
		date = time.Now().Format("02.01.2006")
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	rate, err := cbr.NewClient().Get(ctx, currency, date)
	if err != nil {
		return nil, cbr.Rate{}, err
	}
	body, err := json.Marshal(rate)
	if err != nil {
		return nil, cbr.Rate{}, fmt.Errorf("курс: %w", err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
	}, rate, nil
}
