// Command mcp-fetch — локальный MCP-сервер для загрузки публичных веб-страниц.
// Сервер намеренно не умеет обходить CAPTCHA, работать с cookies и выполнять JS,
// а также блокирует локальные и приватные адреса (защита от SSRF).
package main

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/text/encoding/htmlindex"
)

const (
	defaultMaxChars = 20000
	maxMaxChars     = 100000
	maxBodyBytes    = 2 << 20 // 2 MiB
	maxRedirects    = 5
	requestTimeout  = 15 * time.Second

	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// fetchArgs — входная схема инструмента. SDK сам валидирует аргументы.
type fetchArgs struct {
	URL      string `json:"url" jsonschema:"обязательный http/https URL публичной страницы"`
	MaxChars int    `json:"max_chars,omitempty" jsonschema:"сколько символов текста вернуть; по умолчанию 20000, максимум 100000"`
}

// fetchOutput — успешный результат инструмента.
type fetchOutput struct {
	Status      int    `json:"status"`
	FinalURL    string `json:"final_url"`
	ContentType string `json:"content_type"`
	RetryAfter  string `json:"retry_after,omitempty"`
	Content     string `json:"content"`
	Truncated   bool   `json:"truncated"`
}

func main() {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "goagent-fetch",
		Version: "1.0.0",
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "fetch_url",
		Description: "Загружает публичную веб-страницу по http/https и возвращает её текст. " +
			"HTML очищается от script/style и тегов. Локальные и приватные адреса запрещены, " +
			"CAPTCHA не обходится, запросы при 403/429 не повторяются. " +
			"Аргументы: url, max_chars (по умолчанию 20000, максимум 100000).",
	}, fetchURL)

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("mcp-fetch: %v", err)
	}
}

func fetchURL(ctx context.Context, _ *mcp.CallToolRequest, args fetchArgs) (*mcp.CallToolResult, fetchOutput, error) {
	maxChars := args.MaxChars
	if maxChars < 0 {
		return nil, fetchOutput{}, errors.New("max_chars не может быть отрицательным")
	}
	if maxChars == 0 {
		maxChars = defaultMaxChars
	}
	if maxChars > maxMaxChars {
		return nil, fetchOutput{}, fmt.Errorf("max_chars не может быть больше %d", maxMaxChars)
	}

	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	target, err := validateURL(ctx, args.URL)
	if err != nil {
		return nil, fetchOutput{}, err
	}
	// validateURL резолвит хост; повторная проверка выполняется и в CheckRedirect.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fetchOutput{}, fmt.Errorf("создание запроса: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,text/plain;q=0.8,*/*;q=0.5")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")

	client := &http.Client{
		Transport: newTransport(),
		Timeout:   requestTimeout,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("слишком много редиректов (максимум %d)", maxRedirects)
			}
			if _, err := validateURL(next.Context(), next.URL.String()); err != nil {
				return fmt.Errorf("редирект на запрещённый адрес: %w", err)
			}
			return nil
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fetchOutput{}, fmt.Errorf("запрос не выполнен: %w", err)
	}
	defer resp.Body.Close()

	retryAfter := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden {
		if retryAfter != "" {
			return nil, fetchOutput{}, fmt.Errorf("HTTP %s: доступ ограничен, Retry-After: %s", resp.Status, retryAfter)
		}
		return nil, fetchOutput{}, fmt.Errorf("HTTP %s: доступ ограничен, повтор запроса не выполняется", resp.Status)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if retryAfter != "" {
			return nil, fetchOutput{}, fmt.Errorf("HTTP %s, Retry-After: %s", resp.Status, retryAfter)
		}
		return nil, fetchOutput{}, fmt.Errorf("HTTP %s", resp.Status)
	}

	body, bodyTruncated, err := readBody(resp)
	if err != nil {
		return nil, fetchOutput{}, err
	}

	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	mediaType := parseMediaType(contentType)
	text := strings.ToValidUTF8(string(decodeCharset(body, contentType, bodyTruncated)), "\uFFFD")

	switch {
	case isHTML(mediaType) || (mediaType == "" && looksLikeHTML(text)):
		text = htmlToText(text)
	case isTextual(mediaType) || mediaType == "":
		// Текст и JSON возвращаем как есть (уже приведено к UTF-8).
	default:
		return nil, fetchOutput{}, fmt.Errorf("неподдерживаемый content type %q: инструмент читает только HTML, текст и JSON", contentType)
	}

	content, contentTruncated := truncateRunes(text, maxChars)
	return nil, fetchOutput{
		Status:      resp.StatusCode,
		FinalURL:    resp.Request.URL.String(),
		ContentType: contentType,
		RetryAfter:  retryAfter,
		Content:     content,
		Truncated:   bodyTruncated || contentTruncated,
	}, nil
}

// newTransport собирает HTTP-транспорт с проверкой адреса на каждом соединении.
// Proxy отключён, чтобы переменные окружения не обходили проверки SSRF.
func newTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           safeDialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: requestTimeout,
		DisableKeepAlives:     true,
		ForceAttemptHTTP2:     false,
	}
}

// safeDialContext резолвит имя и соединяется только с проверенными IP.
// Это защищает от DNS rebinding: проверка выполняется непосредственно перед dial.
func safeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if err := validateHost(ctx, host); err != nil {
		return nil, err
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("не удалось разрешить хост %q: %w", host, err)
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var lastErr error
	for _, a := range addrs {
		if blockedIP(a.IP) != "" {
			continue
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(a.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("для хоста %q нет разрешённых адресов", host)
}

// validateURL проверяет схему, отсутствие userinfo и безопасность хоста.
func validateURL(ctx context.Context, raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("url обязателен")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("некорректный url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("разрешены только http/https, получено %q", u.Scheme)
	}
	if u.User != nil {
		return nil, errors.New("userinfo в URL запрещён")
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("в URL нет хоста")
	}
	if err := validateHost(ctx, host); err != nil {
		return nil, err
	}
	return u, nil
}

// validateHost запрещает локальные имена и хосты, которые резолвятся
// хотя бы в один приватный адрес.
func validateHost(ctx context.Context, host string) error {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if h == "" {
		return errors.New("пустой хост")
	}
	if h == "localhost" || strings.HasSuffix(h, ".localhost") ||
		strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") {
		return fmt.Errorf("хост %q локальный и запрещён", host)
	}

	if ip := net.ParseIP(h); ip != nil {
		if reason := blockedIP(ip); reason != "" {
			return fmt.Errorf("хост %q указывает на запрещённый адрес (%s)", host, reason)
		}
		return nil
	}

	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, h)
	if err != nil {
		return fmt.Errorf("не удалось разрешить хост %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("хост %q не разрешился ни в один IP", host)
	}
	for _, a := range addrs {
		if reason := blockedIP(a.IP); reason != "" {
			return fmt.Errorf("хост %q разрешается в запрещённый адрес %s (%s)", host, a.IP, reason)
		}
	}
	return nil
}

// blockedIP возвращает причину блокировки или пустую строку.
func blockedIP(ip net.IP) string {
	switch {
	case ip == nil:
		return "пустой IP"
	case ip.IsUnspecified():
		return "unspecified"
	case ip.IsLoopback():
		return "loopback"
	case ip.IsPrivate():
		return "private"
	case ip.IsLinkLocalUnicast():
		return "link-local"
	case ip.IsLinkLocalMulticast():
		return "link-local multicast"
	case ip.IsInterfaceLocalMulticast():
		return "interface-local multicast"
	case ip.IsMulticast():
		return "multicast"
	case !ip.IsGlobalUnicast():
		return "не глобальный адрес"
	}
	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 0:
			return "0.0.0.0/8"
		case ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127:
			return "CGNAT 100.64.0.0/10"
		case ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 0:
			return "192.0.0.0/24"
		case ip4[0] == 198 && (ip4[1] == 18 || ip4[1] == 19):
			return "benchmark 198.18.0.0/15"
		case ip4[0] >= 240:
			return "reserved 240.0.0.0/4"
		}
	}
	return ""
}

// readBody читает ответ с распаковкой gzip и жёстким лимитом 2 MiB.
func readBody(resp *http.Response) ([]byte, bool, error) {
	var reader io.Reader = resp.Body
	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("Content-Encoding")), "gzip") {
		zr, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, false, fmt.Errorf("распаковка gzip: %w", err)
		}
		defer zr.Close()
		reader = zr
	}

	data, err := io.ReadAll(io.LimitReader(reader, maxBodyBytes+1))
	if err != nil {
		return nil, false, fmt.Errorf("чтение ответа: %w", err)
	}
	truncated := false
	if len(data) > maxBodyBytes {
		data = data[:maxBodyBytes]
		truncated = true
	}
	return data, truncated, nil
}

func parseMediaType(contentType string) string {
	if contentType == "" {
		return ""
	}
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mt = strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])
	}
	return strings.ToLower(mt)
}

func isHTML(mediaType string) bool {
	return mediaType == "text/html" || mediaType == "application/xhtml+xml" || strings.HasSuffix(mediaType, "+html")
}

func isTextual(mediaType string) bool {
	return strings.HasPrefix(mediaType, "text/") ||
		mediaType == "application/json" ||
		strings.HasSuffix(mediaType, "+json") ||
		mediaType == "application/xml" ||
		strings.HasSuffix(mediaType, "+xml") ||
		mediaType == "application/javascript" ||
		mediaType == "application/x-ndjson"
}

func looksLikeHTML(s string) bool {
	head := strings.ToLower(strings.TrimSpace(s))
	if len(head) > 512 {
		head = head[:512]
	}
	return strings.HasPrefix(head, "<!doctype html") || strings.HasPrefix(head, "<html") ||
		strings.Contains(head, "<!doctype html") || strings.Contains(head, "<head")
}

var (
	rawBlockRe    = regexp.MustCompile(`(?is)<(script|style|noscript)\b[^>]*>.*?</(?:script|style|noscript)\s*>`)
	danglingRawRe = regexp.MustCompile(`(?is)<(script|style|noscript)\b[^>]*>.*$`)
	commentRe     = regexp.MustCompile(`(?s)<!--.*?-->`)
	danglingCmtRe = regexp.MustCompile(`(?s)<!--.*$`)
	breakTagRe    = regexp.MustCompile(`(?i)<\s*(?:br|hr|/p|/div|/li|/tr|/h[1-6]|/section|/article|/header|/footer|/blockquote)\b[^>]*>`)
	tagRe         = regexp.MustCompile(`(?s)<[^>]*>`)
	metaCharsetRe = regexp.MustCompile(`(?is)<meta[^>]+charset\s*=\s*["']?\s*([a-z0-9._-]+)`)
)

// htmlToText убирает script/style и теги, декодирует сущности,
// сохраняя переводы строк на блочных тегах.
func htmlToText(raw string) string {
	raw = rawBlockRe.ReplaceAllString(raw, " ")
	raw = danglingRawRe.ReplaceAllString(raw, " ")
	raw = commentRe.ReplaceAllString(raw, " ")
	raw = danglingCmtRe.ReplaceAllString(raw, " ")
	raw = breakTagRe.ReplaceAllString(raw, "\n")
	raw = tagRe.ReplaceAllString(raw, " ")
	raw = html.UnescapeString(raw)
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")

	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// decodeCharset приводит тело к UTF-8 по charset из Content-Type или <meta>.
func decodeCharset(data []byte, contentType string, truncated bool) []byte {
	label := ""
	if _, params, err := mime.ParseMediaType(contentType); err == nil {
		label = params["charset"]
	}
	if label == "" {
		if m := metaCharsetRe.FindSubmatch(data); len(m) > 1 {
			label = string(m[1])
		}
	}
	label = strings.ToLower(strings.TrimSpace(label))
	if label == "" || label == "utf-8" || label == "utf8" {
		return data
	}
	enc, err := htmlindex.Get(label)
	if err != nil {
		return data
	}
	decoded, err := enc.NewDecoder().Bytes(data)
	if err == nil {
		return decoded
	}
	if truncated {
		// Обрезанный на границе символа текст: пробуем отбросить хвост.
		for i := 0; i < 4 && len(data) > 0; i++ {
			data = data[:len(data)-1]
			if decoded, err := enc.NewDecoder().Bytes(data); err == nil {
				return decoded
			}
		}
	}
	return data
}

func truncateRunes(s string, max int) (string, bool) {
	if max <= 0 {
		return "", s != ""
	}
	if len(s) <= max {
		return s, false
	}
	if utf8.RuneCountInString(s) <= max {
		return s, false
	}
	r := []rune(s)
	return string(r[:max]), true
}
