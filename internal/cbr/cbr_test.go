package cbr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testXML = `<?xml version="1.0" encoding="windows-1251"?>
<ValCurs Date="20.11.2026" name="Foreign Currency Market">
  <Valute ID="R01235">
    <NumCode>840</NumCode>
    <CharCode>USD</CharCode>
    <Nominal>1</Nominal>
    <Name>US Dollar</Name>
    <Value>81,9290</Value>
    <VunitRate>81,929</VunitRate>
  </Valute>
  <Valute ID="R01335">
    <NumCode>398</NumCode>
    <CharCode>KZT</CharCode>
    <Nominal>100</Nominal>
    <Name>Kazakhstani Tenge</Name>
    <Value>15,8424</Value>
    <VunitRate>0,158424</VunitRate>
  </Valute>
</ValCurs>`

func testClient(t *testing.T) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("date_req"); got != "20/11/2026" {
			t.Errorf("date_req = %q, want 20/11/2026", got)
		}
		if got := r.Header.Get("User-Agent"); got != "goagent-cbr/1.0" {
			t.Errorf("User-Agent = %q", got)
		}
		w.Header().Set("Content-Type", "application/xml; charset=windows-1251")
		_, _ = w.Write([]byte(testXML))
	}))
	return &Client{BaseURL: srv.URL, HTTP: srv.Client()}, srv
}

func TestGetUSD(t *testing.T) {
	c, srv := testClient(t)
	defer srv.Close()

	got, err := c.Get(context.Background(), "usd", "20.11.2026")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Currency != "USD" || got.Nominal != 1 {
		t.Errorf("currency/nominal = %q/%d", got.Currency, got.Nominal)
	}
	if got.Date != "20.11.2026" {
		t.Errorf("date = %q", got.Date)
	}
	if got.Rate != 81.929 || got.UnitRate != 81.929 {
		t.Errorf("rate = %v, unit = %v", got.Rate, got.UnitRate)
	}
	if got.Source != "ЦБ РФ" {
		t.Errorf("source = %q", got.Source)
	}
}

func TestGetNominal(t *testing.T) {
	c, srv := testClient(t)
	defer srv.Close()

	got, err := c.Get(context.Background(), "KZT", "20.11.2026")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Nominal != 100 {
		t.Errorf("nominal = %d", got.Nominal)
	}
	if got.UnitRate != 0.158424 {
		t.Errorf("unit rate = %v", got.UnitRate)
	}
}

func TestGetUnknownCurrency(t *testing.T) {
	c, srv := testClient(t)
	defer srv.Close()

	_, err := c.Get(context.Background(), "XXX", "20.11.2026")
	if err == nil || !strings.Contains(err.Error(), "USD") {
		t.Fatalf("ожидалась ошибка со списком валют, получено %v", err)
	}
}

func TestGetRejectsBadCurrency(t *testing.T) {
	c := &Client{}
	for _, code := range []string{"", "US", "USDD", "12$"} {
		if _, err := c.Get(context.Background(), code, "20.11.2026"); err == nil {
			t.Errorf("код %q принят", code)
		}
	}
}

func TestGetRejectsBadDate(t *testing.T) {
	c := &Client{}
	for _, date := range []string{"2026-02-30", "20/11/2026", "вчера"} {
		if _, err := c.Get(context.Background(), "USD", date); err == nil {
			t.Errorf("дата %q принята", date)
		}
	}
}

func TestGetHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "blocked", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	if _, err := c.Get(context.Background(), "USD", "20.11.2026"); err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("ожидалась HTTP 429, получено %v", err)
	}
}

func TestParseDate(t *testing.T) {
	for _, in := range []string{"20.11.2026", "2026-11-20"} {
		got, err := parseDate(in)
		if err != nil {
			t.Errorf("parseDate(%q): %v", in, err)
			continue
		}
		if got.Format("02.01.2006") != "20.11.2026" {
			t.Errorf("parseDate(%q) = %s", in, got)
		}
	}
}

func TestParseDecimal(t *testing.T) {
	got, err := parseDecimal("1 234,50")
	if err != nil {
		t.Fatal(err)
	}
	if got != 1234.5 {
		t.Errorf("parseDecimal = %v", got)
	}
	if _, err := parseDecimal("abc"); err == nil {
		t.Error("нечисловое значение принято")
	}
}
