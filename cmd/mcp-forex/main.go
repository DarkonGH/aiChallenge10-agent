// Command mcp-forex — MCP-сервер валютных котировок и накопления замеров.
//
// Сервер отдельный процесс: агент запускает его по mcp.json и общается по
// stdio. Котировки берутся из публичного Coinbase spot, ключ не нужен.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/DarkonGH/aiChallenge10-agent/internal/forex"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fetchFXArgs struct {
	Base  string `json:"base,omitempty" jsonschema:"базовая валюта, по умолчанию USD"`
	Quote string `json:"quote,omitempty" jsonschema:"котируемая валюта, по умолчанию RUB"`
}

type saveTickArgs struct {
	Base      string  `json:"base" jsonschema:"базовая валюта, например USD"`
	Quote     string  `json:"quote" jsonschema:"котируемая валюта, например RUB"`
	Rate      float64 `json:"rate" jsonschema:"курс пары"`
	FetchedAt string  `json:"fetched_at,omitempty" jsonschema:"время замера RFC3339; пусто — сейчас"`
	Source    string  `json:"source,omitempty" jsonschema:"источник котировки"`
}

type saveTickResult struct {
	Saved bool `json:"saved"`
	Total int  `json:"total"`
}

type readPeriodArgs struct {
	From  string `json:"from,omitempty" jsonschema:"начало периода RFC3339; пусто — без ограничения"`
	To    string `json:"to,omitempty" jsonschema:"конец периода RFC3339; пусто — без ограничения"`
	Quote string `json:"quote,omitempty" jsonschema:"котируемая валюта, например RUB"`
}

type readPeriodResult struct {
	Count int          `json:"count"`
	Ticks []forex.Tick `json:"ticks"`
}

func main() {
	store, err := forex.NewStore(forex.DefaultStorePath())
	if err != nil {
		log.Fatalf("mcp-forex: %v", err)
	}
	client := forex.NewClient()

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "goagent-forex",
		Version: "1.0.0",
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "fetch_fx",
		Description: "Получить текущий spot-курс валютной пары. " +
			"Аргументы base и quote; по умолчанию USD/RUB. Не придумывай курс сам.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args fetchFXArgs) (*mcp.CallToolResult, forex.Tick, error) {
		base := defaultString(args.Base, "USD")
		quote := defaultString(args.Quote, "RUB")
		tick, err := client.Spot(ctx, base, quote)
		if err != nil {
			return nil, forex.Tick{}, err
		}
		content, err := jsonContent(tick)
		if err != nil {
			return nil, forex.Tick{}, err
		}
		return content, tick, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "save_tick",
		Description: "Сохранить один замер курса в append-only JSONL-журнал. " +
			"Возвращает общее число накопленных записей.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, args saveTickArgs) (*mcp.CallToolResult, saveTickResult, error) {
		total, err := store.Append(forex.Tick{
			Base:      args.Base,
			Quote:     args.Quote,
			Rate:      args.Rate,
			FetchedAt: args.FetchedAt,
			Source:    args.Source,
		})
		if err != nil {
			return nil, saveTickResult{}, err
		}
		result := saveTickResult{Saved: true, Total: total}
		content, err := jsonContent(result)
		if err != nil {
			return nil, saveTickResult{}, err
		}
		return content, result, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "read_period",
		Description: "Прочитать накопленные замеры за период [from, to]. " +
			"Пустые границы означают всё накопленное; quote фильтрует пару.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, args readPeriodArgs) (*mcp.CallToolResult, readPeriodResult, error) {
		from, err := optionalTime(args.From)
		if err != nil {
			return nil, readPeriodResult{}, fmt.Errorf("from: %w", err)
		}
		to, err := optionalTime(args.To)
		if err != nil {
			return nil, readPeriodResult{}, fmt.Errorf("to: %w", err)
		}
		ticks, err := store.ReadPeriod(from, to, args.Quote)
		if err != nil {
			return nil, readPeriodResult{}, err
		}
		result := readPeriodResult{Count: len(ticks), Ticks: ticks}
		content, err := jsonContent(result)
		if err != nil {
			return nil, readPeriodResult{}, err
		}
		return content, result, nil
	})

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("mcp-forex: %v", err)
	}
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func jsonContent(v any) (*mcp.CallToolResult, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
	}, nil
}

func optionalTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
		if at, err := time.Parse(layout, value); err == nil {
			return at, nil
		}
	}
	return time.Time{}, fmt.Errorf("ожидается RFC3339, получено %q", value)
}
