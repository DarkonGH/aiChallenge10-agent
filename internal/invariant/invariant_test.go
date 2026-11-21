package invariant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DarkonGH/aiChallenge10-agent/internal/llm"
)

func testSet(t *testing.T) *Set {
	t.Helper()
	s, err := Load(filepath.Join(t.TempDir(), "invariants.json"))
	if err != nil {
		t.Fatal(err)
	}
	mustAdd := func(id, area, text, rule string) {
		t.Helper()
		r, err := ParseRule(rule)
		if err != nil {
			t.Fatalf("ParseRule(%q): %v", rule, err)
		}
		if err := s.Add(id, area, text, r, "user"); err != nil {
			t.Fatalf("Add(%s): %v", id, err)
		}
	}
	mustAdd("arch", "architecture", "Только модульный монолит", "deny=микросервис,microservice,kubernetes")
	mustAdd("db", "database", "Единственная СУБД — PostgreSQL 15", "deny=mysql,mongodb,oracle,sqlite")
	mustAdd("budget", "budget", "Бюджет не более 300 000 ₽/мес", "max=300000")
	return s
}

func TestParseRule(t *testing.T) {
	r, err := ParseRule("deny=mysql, mongodb allow=postgres max=300000")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Deny) != 2 || r.Deny[1] != "mongodb" {
		t.Errorf("deny = %v", r.Deny)
	}
	if len(r.Allow) != 1 || r.Allow[0] != "postgres" {
		t.Errorf("allow = %v", r.Allow)
	}
	if r.Max == nil || *r.Max != 300000 {
		t.Errorf("max = %v", r.Max)
	}

	for _, bad := range []string{"", "mysql", "deny=", "max=abc", "max=-1", "limit=1"} {
		if _, err := ParseRule(bad); err == nil {
			t.Errorf("ParseRule(%q): expected an error", bad)
		}
	}
}

func TestCheckChoices(t *testing.T) {
	s := testSet(t)

	// A clean solution with all touched areas declared.
	ok := []Choice{{Area: "database", Value: "PostgreSQL 15"}, {Area: "budget", Value: "240 000 ₽/мес"}}
	if err := s.Check(ok, []string{"db", "budget"}); err != nil {
		t.Fatalf("clean check: %v", err)
	}
	// Declaring by area instead of id also works.
	if err := s.Check(ok, []string{"database", "budget"}); err != nil {
		t.Fatalf("area alias: %v", err)
	}

	cases := []struct {
		name     string
		choices  []Choice
		declared []string
		want     string
	}{
		{"forbidden database", []Choice{{Area: "db", Value: "MongoDB 7"}}, []string{"db"}, "запрещённое"},
		{"not declared", []Choice{{Area: "db", Value: "PostgreSQL"}}, nil, "не учтён"},
		{"unknown area", []Choice{{Area: "cache", Value: "Redis"}}, []string{"db"}, "не описана"},
		{"unknown declared", nil, []string{"nope"}, "неизвестный инвариант"},
		{"over budget", []Choice{{Area: "budget", Value: "350 000 ₽/мес"}}, []string{"budget"}, "больше предела"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := s.Check(c.choices, c.declared)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Check = %v, want %q", err, c.want)
			}
		})
	}

	// Scale suffixes are understood: 240 тыс is 240 000 and fits.
	if err := s.Check([]Choice{{Area: "budget", Value: "240 тыс. руб."}}, []string{"budget"}); err != nil {
		t.Fatalf("240 тыс: %v", err)
	}
	if err := s.Check([]Choice{{Area: "budget", Value: "1,5 млн"}}, []string{"budget"}); err == nil {
		t.Fatal("1,5 млн must exceed 300 000")
	}
}

func TestCheckTextSkipsRefusals(t *testing.T) {
	s := testSet(t)

	v := s.CheckText("Возьмите MongoDB — у неё гибкая схема.")
	if len(v) != 1 || v[0].ID != "db" {
		t.Fatalf("violation = %+v, want db", v)
	}

	refusals := []string{
		"MongoDB использовать нельзя: инвариант db требует PostgreSQL.",
		"Вместо MongoDB возьмите PostgreSQL.",
		"Мы не будем использовать MySQL и Oracle.",
		"Микросервисы не подходят: проект должен остаться модульным монолитом.",
		"Kubernetes здесь запрещён.",
		"I would avoid MongoDB and use PostgreSQL instead.",
	}
	for _, text := range refusals {
		if got := s.CheckText(text); len(got) != 0 {
			t.Errorf("CheckText(%q) = %+v, want none", text, got)
		}
	}
}

// TestQuotePresent covers fabricated citations: the local model once blocked
// a correct refusal with the quote "MongoDB нужна принципиально…", which was
// nowhere in the draft. The gate must require the quote to exist.
func TestQuotePresent(t *testing.T) {
	text := "Стек тот же, но 350 000 ₽/мес назвать не могу: лимит 300 000."
	if !QuotePresent(text, "350 000 ₽/мес") {
		t.Fatal("exact quote must be present")
	}
	if !QuotePresent(text, "350 000 ₽/мес назвать не могу.") {
		t.Fatal("quote with a trailing period must be present")
	}
	if QuotePresent(text, "MongoDB нужна принципиально") {
		t.Fatal("a fabricated quote must not be present")
	}
	if QuotePresent(text, "назвать не могу 350 000") {
		t.Fatal("a paraphrase must not count as an exact quote")
	}
}

// TestNegatedQuote covers the false positive from the demo: the local model
// quoted the forbidden amount "350 000 ₽/мес" from a sentence that refuses to
// name it. The guard must not let that refusal be rejected as a violation.
func TestNegatedQuote(t *testing.T) {
	text := "Стек тот же, но 350 000 ₽/мес назвать не могу: инвариант budget — не более 300 000."
	if !NegatedQuote(text, "350 000 ₽/мес") {
		t.Fatal("quote in a refusing sentence must be recognised as negated")
	}
	// NBSP and case differences must not break the match.
	text2 := "Стек тот же, но 350\u00a0000 ₽/мес назвать не могу."
	if !NegatedQuote(text2, "350 000 ₽/мес") {
		t.Fatal("NBSP in the draft must not break the quote match")
	}
	if NegatedQuote("Стек за 350 000 ₽/мес: PostgreSQL, Kafka.", "350 000 ₽/мес") {
		t.Fatal("a proposal must not be treated as a refusal")
	}
	if NegatedQuote(text, "MongoDB") {
		t.Fatal("a quote absent from the text must not be negated")
	}
	// "не" belongs to another object in the same sentence and must not excuse
	// a proposal to take MongoDB.
	if NegatedQuote("Не используйте PostgreSQL, возьмите MongoDB.", "MongoDB") {
		t.Fatal("negation attached to another object must not excuse MongoDB")
	}
	if !NegatedQuote("MongoDB использовать нельзя.", "MongoDB") {
		t.Fatal("нельзя right after the quote must count as negation")
	}
	// A refusal with a colon keeps the negation in the first clause.
	text3 := "Изменить инвариант db я не могу: такого действия у ассистента нет."
	if !NegatedQuote(text3, "Изменить инвариант db я не могу: такого действия у ассистента нет.") {
		t.Fatal("a full-sentence quote of a refusal must be negated")
	}
	if !NegatedQuote(text3, "db") {
		t.Fatal("db in the negated clause must be treated as a refusal")
	}
}

func TestExceedsMax(t *testing.T) {
	if ExceedsMax("Смета 350 000 ₽/мес, лимит 400 000 ₽/мес.", 400000) {
		t.Fatal("350k under a 400k limit must not exceed")
	}
	if !ExceedsMax("Смета 450 000 ₽/мес.", 400000) {
		t.Fatal("450k over a 400k limit must exceed")
	}
	if !ExceedsMax("Бюджет 1,5 млн", 400000) {
		t.Fatal("1.5M must exceed 400k")
	}
	if ExceedsMax("240 тыс. руб.", 400000) {
		t.Fatal("240k must not exceed")
	}
}

func TestChangeJournal(t *testing.T) {
	s := testSet(t)

	rule, _ := ParseRule("max=400000")
	if err := s.Change("budget", "budget", "Бюджет не более 400 000 ₽/мес", rule, "user"); err != nil {
		t.Fatalf("change: %v", err)
	}
	changes := s.Changes("budget")
	if len(changes) != 2 {
		t.Fatalf("changes = %d, want add + change", len(changes))
	}
	c := changes[1]
	if c.Kind != "change" || c.From == nil || c.To == nil {
		t.Fatalf("change snapshot = %+v", c)
	}
	if c.From.Rule.Max == nil || *c.From.Rule.Max != 300000 {
		t.Errorf("from max = %v, want 300000", c.From.Rule.Max)
	}
	if c.To.Rule.Max == nil || *c.To.Rule.Max != 400000 {
		t.Errorf("to max = %v, want 400000", c.To.Rule.Max)
	}
	if c.From.Text == c.To.Text {
		t.Error("from/to texts must differ")
	}

	if err := s.Change("nope", "x", "y", rule, "user"); err == nil {
		t.Error("changing a missing invariant must fail")
	}
	if err := s.Add("db", "database", "дубль", rule, "user"); err == nil {
		t.Error("adding a duplicate id must fail")
	}

	if err := s.Remove("arch", "user"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if s.Find("arch") != nil {
		t.Error("arch still present after remove")
	}
	rem := s.Changes("arch")
	if len(rem) != 2 || rem[1].Kind != "remove" || rem[1].From == nil {
		t.Errorf("remove journal = %+v", rem)
	}
}

func TestPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invariants.json")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Active() {
		t.Fatal("missing file must be an empty set")
	}
	r, _ := ParseRule("deny=mysql")
	if err := s.Add("db", "database", "Только PostgreSQL", r, "user"); err != nil {
		t.Fatal(err)
	}

	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Len() != 1 || len(again.Changes("")) != 1 {
		t.Fatalf("reloaded = %d invariants, %d changes", again.Len(), len(again.Changes("")))
	}
	if it := again.Find("database"); it == nil || it.ID != "db" {
		t.Fatalf("Find by area = %+v", it)
	}

	// A hand-edited file with a rule-less invariant is refused: the code
	// could not check it.
	bad := `{"invariants":[{"id":"x","area":"a","text":"t"}]}`
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("rule-less invariant must be refused")
	}
}

func TestParseVerdict(t *testing.T) {
	v, err := parseVerdict("```json\n{\"violates\":true,\"id\":\"DB\",\"quote\":\"MongoDB\",\"reason\":\"нельзя\"}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Violates || v.ID != "db" || v.Quote != "MongoDB" {
		t.Fatalf("verdict = %+v", v)
	}
	if _, err := parseVerdict("не знаю"); err == nil {
		t.Error("text without JSON must fail")
	}
}

func TestCheckerCallsModel(t *testing.T) {
	var gotReq llm.ChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{
					"role":    "assistant",
					"content": `{"violates":true,"id":"db","quote":"MongoDB","reason":"запрещённая СУБД"}`,
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	}))
	defer srv.Close()

	client := llm.NewChatClientAt(srv.URL, "test")
	c := NewChecker(client, func() string { return "local-model" })
	set := testSet(t)
	v, err := c.Check(context.Background(), "Возьмите MongoDB.", set.All())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !v.Violates || v.ID != "db" {
		t.Fatalf("verdict = %+v", v)
	}
	if gotReq.Model != "local-model" {
		t.Errorf("model = %q", gotReq.Model)
	}
	if len(gotReq.Messages) != 2 || gotReq.Messages[1].Content == nil || !strings.Contains(*gotReq.Messages[1].Content, "MongoDB") {
		t.Errorf("request messages = %+v", gotReq.Messages)
	}
}
