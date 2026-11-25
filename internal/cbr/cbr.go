// Package cbr is the client for the Bank of Russia daily currency rates.
//
// The public endpoint returns windows-1251 XML, so the decoder needs an
// explicit charset reader. Only the ASCII/numeric fields (CharCode, Nominal,
// Value) are used: the Russian currency name is irrelevant for the tool.
package cbr

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"
)

// DefaultURL is the public daily rates endpoint.
const DefaultURL = "https://www.cbr.ru/scripts/XML_daily.asp"

// Rate is one currency quotation. Rate is the value for Nominal units;
// UnitRate is the value for exactly one unit (important for JPY and similar
// currencies quoted per 100).
type Rate struct {
	Date     string  `json:"date"`
	Currency string  `json:"currency"`
	Nominal  int     `json:"nominal"`
	Rate     float64 `json:"rate"`
	UnitRate float64 `json:"unit_rate"`
	Source   string  `json:"source"`
}

// Client talks to the CBR endpoint. BaseURL is replaceable in tests.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// NewClient returns a client with a bounded timeout. A dedicated timeout is
// important: the MCP server must not hang forever on a slow government site.
func NewClient() *Client {
	return &Client{
		BaseURL: DefaultURL,
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
}

// Get returns the quotation of currency for date. Date accepts DD.MM.YYYY or
// YYYY-MM-DD; empty means today. Currency is a three-letter code such as USD.
func (c *Client) Get(ctx context.Context, currency, date string) (Rate, error) {
	code := strings.ToUpper(strings.TrimSpace(currency))
	if len(code) != 3 {
		return Rate{}, fmt.Errorf("валюта: нужен трёхбуквенный код, получено %q", currency)
	}
	for _, r := range code {
		if r < 'A' || r > 'Z' {
			return Rate{}, fmt.Errorf("валюта: нужен латинский код, получено %q", currency)
		}
	}

	day, err := parseDate(date)
	if err != nil {
		return Rate{}, err
	}

	base := strings.TrimRight(c.baseURL(), "?")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s?date_req=%s", base, day.Format("02/01/2006")), nil)
	if err != nil {
		return Rate{}, fmt.Errorf("ЦБ РФ: запрос: %w", err)
	}
	req.Header.Set("User-Agent", "goagent-cbr/1.0")
	req.Header.Set("Accept", "application/xml,text/xml")

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return Rate{}, fmt.Errorf("ЦБ РФ: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Rate{}, fmt.Errorf("ЦБ РФ: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Rate{}, fmt.Errorf("ЦБ РФ: чтение ответа: %w", err)
	}

	var doc valCurs
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		if strings.EqualFold(charset, "windows-1251") {
			return transform.NewReader(input, charmap.Windows1251.NewDecoder()), nil
		}
		return input, nil
	}
	if err := dec.Decode(&doc); err != nil {
		return Rate{}, fmt.Errorf("ЦБ РФ: разбор XML: %w", err)
	}

	var available []string
	for _, v := range doc.Valutes {
		if strings.EqualFold(strings.TrimSpace(v.CharCode), code) {
			value, err := parseDecimal(v.Value)
			if err != nil {
				return Rate{}, fmt.Errorf("ЦБ РФ: курс %s: %w", code, err)
			}
			nominal := v.Nominal
			if nominal <= 0 {
				nominal = 1
			}
			resultDate := doc.Date
			if resultDate == "" {
				resultDate = day.Format("02.01.2006")
			}
			return Rate{
				Date:     resultDate,
				Currency: code,
				Nominal:  nominal,
				Rate:     value,
				UnitRate: value / float64(nominal),
				Source:   "ЦБ РФ",
			}, nil
		}
		if c := strings.TrimSpace(v.CharCode); c != "" {
			available = append(available, c)
		}
	}
	if len(available) == 0 {
		return Rate{}, fmt.Errorf("ЦБ РФ: на %s нет валют", day.Format("02.01.2006"))
	}
	return Rate{}, fmt.Errorf("ЦБ РФ: валюта %s не найдена; доступны: %s",
		code, strings.Join(available, ", "))
}

func (c *Client) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return DefaultURL
}

func parseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Now(), nil
	}
	for _, layout := range []string{"02.01.2006", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("дата: ожидается ДД.ММ.ГГГГ, получено %q", s)
}

func parseDecimal(s string) (float64, error) {
	// ЦБ РФ sometimes returns thousands groups with spaces; strip them too.
	s = strings.TrimSpace(strings.NewReplacer(" ", "", "\u00a0", "", ",", ".").Replace(s))
	if s == "" {
		return 0, fmt.Errorf("пустое значение")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("не число %q", s)
	}
	return v, nil
}

type valCurs struct {
	Date    string   `xml:"Date,attr"`
	Valutes []valute `xml:"Valute"`
}

type valute struct {
	CharCode string `xml:"CharCode"`
	Nominal  int    `xml:"Nominal"`
	Value    string `xml:"Value"`
}
