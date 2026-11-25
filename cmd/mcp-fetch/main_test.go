package main

import (
	"context"
	"strings"
	"testing"
)

func TestValidateHostRejectsLocalAddresses(t *testing.T) {
	blocked := []string{
		"localhost",
		"LOCALHOST.",
		"api.localhost",
		"printer.local",
		"metadata.google.internal",
		"127.0.0.1",
		"127.1.2.3",
		"10.0.0.1",
		"172.16.5.4",
		"192.168.1.1",
		"169.254.169.254",
		"0.0.0.0",
		"100.64.0.1",
		"198.18.0.1",
		"224.0.0.1",
		"::1",
		"fd00::1",
		"fe80::1",
		"::",
		"::ffff:127.0.0.1",
	}
	for _, host := range blocked {
		if err := validateHost(context.Background(), host); err == nil {
			t.Errorf("validateHost(%q) вернул nil, ожидалась ошибка", host)
		}
	}
}

func TestValidateHostAllowsPublicIP(t *testing.T) {
	for _, host := range []string{"93.184.216.34", "8.8.8.8", "2606:2800:220:1:248:1893:25c8:1946"} {
		if err := validateHost(context.Background(), host); err != nil {
			t.Errorf("validateHost(%q) = %v, ожидался nil", host, err)
		}
	}
}

func TestBlockedIP(t *testing.T) {
	if reason := blockedIP([]byte{8, 8, 8, 8}); reason != "" {
		t.Errorf("8.8.8.8 заблокирован: %s", reason)
	}
	if reason := blockedIP([]byte{192, 168, 0, 1}); reason == "" {
		t.Error("192.168.0.1 не заблокирован")
	}
}

func TestValidateURL(t *testing.T) {
	bad := []string{
		"",
		"file:///etc/passwd",
		"ftp://example.com/file",
		"http://user:pass@93.184.216.34/",
		"http://localhost/",
		"http://127.0.0.1:8080/",
		"http://[::1]/",
	}
	for _, raw := range bad {
		if _, err := validateURL(context.Background(), raw); err == nil {
			t.Errorf("validateURL(%q) вернул nil, ожидалась ошибка", raw)
		}
	}

	u, err := validateURL(context.Background(), "https://93.184.216.34/page?q=1")
	if err != nil {
		t.Fatalf("validateURL(публичный IP) = %v", err)
	}
	if u.Hostname() != "93.184.216.34" {
		t.Errorf("Hostname() = %q", u.Hostname())
	}
}

func TestHTMLToText(t *testing.T) {
	in := `<html><head><title>Заголовок</title><style>body{color:red}</style>` +
		`<script>alert("x")</script></head><body><h1>Привет &amp; мир</h1>` +
		`<p>Первая&nbsp;строка</p><p>Вторая &lt;строка&gt;</p><!-- comment --></body></html>`
	got := htmlToText(in)

	for _, unwanted := range []string{"alert", "color:red", "<p>", "&amp;", "<!--"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("htmlToText оставил %q в %q", unwanted, got)
		}
	}
	for _, want := range []string{"Привет & мир", "Первая строка", "Вторая <строка>", "Заголовок"} {
		if !strings.Contains(got, want) {
			t.Errorf("htmlToText потерял %q в %q", want, got)
		}
	}
}

func TestTruncateRunes(t *testing.T) {
	got, truncated := truncateRunes("привет", 3)
	if got != "при" || !truncated {
		t.Errorf("truncateRunes = %q, %v; ожидалось %q, true", got, truncated, "при")
	}
	got, truncated = truncateRunes("ok", 10)
	if got != "ok" || truncated {
		t.Errorf("truncateRunes = %q, %v; ожидалось %q, false", got, truncated, "ok")
	}
}

func TestDecodeCharsetWindows1251(t *testing.T) {
	// "Привет" в windows-1251.
	data := []byte{0xCF, 0xF0, 0xE8, 0xE2, 0xE5, 0xF2}
	if got := string(decodeCharset(data, "text/html; charset=windows-1251", false)); got != "Привет" {
		t.Errorf("decodeCharset = %q, ожидалось %q", got, "Привет")
	}
}
