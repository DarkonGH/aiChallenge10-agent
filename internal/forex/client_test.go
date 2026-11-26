package forex

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSpot(t *testing.T) {
	var gotPath, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"amount":"82.5163","base":"USD","currency":"RUB"}}`)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL + "/v2/prices", HTTP: srv.Client()}
	tick, err := c.Spot(context.Background(), "usd", "rub")
	if err != nil {
		t.Fatalf("Spot: %v", err)
	}
	if gotPath != "/v2/prices/USD-RUB/spot" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotUA != "goagent-forex/1.0" {
		t.Fatalf("user-agent = %q", gotUA)
	}
	if tick.Base != "USD" || tick.Quote != "RUB" || tick.Rate != 82.5163 {
		t.Fatalf("tick = %+v", tick)
	}
	if tick.Source != "Coinbase spot" || tick.FetchedAt == "" {
		t.Fatalf("tick metadata = %+v", tick)
	}
}

func TestSpotHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"rate limited"}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	_, err := c.Spot(context.Background(), "USD", "RUB")
	if err == nil || !strings.Contains(err.Error(), "HTTP 429") {
		t.Fatalf("err = %v", err)
	}
}

func TestSpotRejectsBadCurrency(t *testing.T) {
	c := NewClient()
	for _, pair := range [][2]string{{"", "RUB"}, {"US", "RUB"}, {"USD", "R$"}} {
		if _, err := c.Spot(context.Background(), pair[0], pair[1]); err == nil {
			t.Fatalf("пара %q/%q прошла без ошибки", pair[0], pair[1])
		}
	}
}

func TestSpotRejectsBadAmount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"amount":"not-a-number"}}`)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	if _, err := c.Spot(context.Background(), "USD", "RUB"); err == nil {
		t.Fatal("ожидалась ошибка разбора курса")
	}
}
