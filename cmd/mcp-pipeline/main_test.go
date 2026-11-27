package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DarkonGH/aiChallenge10-agent/internal/pipeline"
)

func writeNote(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("запись заметки %s: %v", name, err)
	}
}

func TestSearchFindsAndRanksNotes(t *testing.T) {
	dir := t.TempDir()
	writeNote(t, dir, "a.md", "# MCP basics\nMCP — это протокол для инструментов.")
	writeNote(t, dir, "b.md", "# MCP pipeline\nMCP-инструменты можно собирать в цепочки.")
	writeNote(t, dir, "c.txt", "Заметки про планировщик и отчёты.")
	t.Setenv("PIPELINE_NOTES_DIR", dir)

	_, res, err := search(context.Background(), nil, searchArgs{Query: "MCP"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Count != 2 || len(res.Items) != 2 {
		t.Fatalf("ожидалось 2 документа, получено %+v", res)
	}
	for _, item := range res.Items {
		if item.ID == "" || item.Title == "" || item.Path == "" || item.Text == "" {
			t.Fatalf("неполный item: %+v", item)
		}
	}

	_, one, err := search(context.Background(), nil, searchArgs{Query: "MCP pipeline"})
	if err != nil {
		t.Fatalf("search по двум словам: %v", err)
	}
	if one.Count != 1 || one.Items[0].ID != "b" {
		t.Fatalf("ожидался только b, получено %+v", one)
	}

	_, none, err := search(context.Background(), nil, searchArgs{Query: "нетакого"})
	if err != nil {
		t.Fatalf("search без совпадений: %v", err)
	}
	if none.Count != 0 || none.Items == nil || len(none.Items) != 0 {
		t.Fatalf("ожидался пустой не-nil items, получено %+v", none)
	}
}

func TestSearchEmptyFlagSkipsDisk(t *testing.T) {
	t.Setenv("PIPELINE_NOTES_DIR", filepath.Join(t.TempDir(), "missing"))
	_, res, err := search(context.Background(), nil, searchArgs{Query: "MCP", Empty: true})
	if err != nil {
		t.Fatalf("search empty: %v", err)
	}
	if res.Count != 0 || len(res.Items) != 0 {
		t.Fatalf("ожидался пустой результат, получено %+v", res)
	}
}

func TestSearchRecursiveAndPathFilter(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "code"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeNote(t, filepath.Join(root, "notes"), "a.md", "# MCP\nMCP — протокол.")
	writeNote(t, filepath.Join(root, "code"), "main.go", "// MCP client\nfunc main() {}\n")
	t.Setenv("PIPELINE_SEARCH_ROOT", root)

	_, all, err := search(context.Background(), nil, searchArgs{Query: "MCP"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if all.Count != 2 || all.Scanned < 2 {
		t.Fatalf("рекурсивный поиск не нашёл оба файла: %+v", all)
	}
	if all.Root != root {
		t.Fatalf("root=%q, ожидался %q", all.Root, root)
	}

	_, only, err := search(context.Background(), nil, searchArgs{Query: "MCP", Path: "notes"})
	if err != nil {
		t.Fatalf("search path: %v", err)
	}
	if only.Count != 1 || only.Items[0].Path != "notes/a.md" {
		t.Fatalf("фильтр path не сработал: %+v", only)
	}
	if _, _, err := search(context.Background(), nil, searchArgs{Query: "MCP", Path: "../"}); err == nil {
		t.Fatal("path вне workdir не отклонён")
	}
}

func TestSearchSkipsSecretsHiddenAndBinary(t *testing.T) {
	root := t.TempDir()
	writeNote(t, root, "notes.md", "# MCP\nMCP полезен.")
	writeNote(t, root, ".env", "MCP_TOKEN=secret")
	writeNote(t, root, "server.pem", "MCP certificate")
	writeNote(t, root, ".hidden.md", "# MCP hidden")
	writeNote(t, root, "image.png", "MCP binary")
	if err := os.WriteFile(filepath.Join(root, "binary.md"), []byte{0, 1, 2, 'M', 'C', 'P'}, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PIPELINE_SEARCH_ROOT", root)

	_, res, err := search(context.Background(), nil, searchArgs{Query: "MCP"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Count != 1 || res.Items[0].Path != "notes.md" {
		t.Fatalf("секреты/скрытые/бинарные файлы не отфильтрованы: %+v", res)
	}
	if res.Skipped == 0 {
		t.Fatal("skipped не посчитан")
	}
}

func TestSearchSnippetAroundMatchAndLine(t *testing.T) {
	root := t.TempDir()
	body := "строка 1\nстрока 2\nстрока 3\nстрока 4\nздесь needle и ещё текст\nстрока 6\n"
	writeNote(t, root, "long.md", body)
	t.Setenv("PIPELINE_SEARCH_ROOT", root)

	_, res, err := search(context.Background(), nil, searchArgs{Query: "needle"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if res.Count != 1 {
		t.Fatalf("ожидался один документ: %+v", res)
	}
	item := res.Items[0]
	if item.Line != 5 {
		t.Fatalf("line=%d, ожидалась 5", item.Line)
	}
	if !strings.Contains(item.Snippet, "needle") {
		t.Fatalf("snippet не вокруг совпадения: %q", item.Snippet)
	}
	if !strings.Contains(item.Text, "needle") {
		t.Fatalf("text не содержит совпадение: %q", item.Text)
	}
}

func TestSummarizeCodeUsesFileLineBullets(t *testing.T) {
	items := []pipeline.Item{{
		ID:      "main",
		Path:    "main.go",
		Line:    12,
		Snippet: "func main() {\n\t// MCP client\n}",
	}}
	_, res, err := summarize(context.Background(), nil, summarizeArgs{Items: items, MaxSentences: 3})
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if !strings.Contains(res.Summary, "main.go:12:") || !strings.Contains(res.Summary, "func main") {
		t.Fatalf("код не попал в резюме: %q", res.Summary)
	}
	if strings.Contains(res.Summary, "MCP client") == false {
		t.Fatalf("строка вокруг совпадения потерялась: %q", res.Summary)
	}
}

func TestSearchRejectsEmptyQuery(t *testing.T) {
	if _, _, err := search(context.Background(), nil, searchArgs{Query: "  "}); err == nil {
		t.Fatal("ожидалась ошибка про пустой query")
	}
}

func TestSummarizeBuildsSummary(t *testing.T) {
	items := []pipeline.Item{
		{ID: "a", Text: "MCP — это протокол для инструментов. Он позволяет собирать цепочки."},
		{ID: "b", Text: "Пайплайн останавливается на ошибке. Пустой результат тоже останавливает цепочку."},
	}
	_, res, err := summarize(context.Background(), nil, summarizeArgs{Items: items, MaxSentences: 3})
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if res.Count != 2 {
		t.Fatalf("count: %d", res.Count)
	}
	if !strings.Contains(res.Summary, "Резюме по 2 документам") {
		t.Fatalf("нет заголовка резюме: %q", res.Summary)
	}
	if len(res.Sentences) == 0 || len(res.Sentences) > 3 {
		t.Fatalf("неверное число предложений: %d", len(res.Sentences))
	}
	if len(res.SourceIDs) != 2 || res.SourceIDs[0] != "a" {
		t.Fatalf("неверные source_ids: %v", res.SourceIDs)
	}
}

func TestSummarizeRejectsEmptyAndFail(t *testing.T) {
	if _, _, err := summarize(context.Background(), nil, summarizeArgs{}); err == nil ||
		!strings.Contains(err.Error(), "пустой список items") {
		t.Fatalf("ожидалась ошибка про пустой items, получено %v", err)
	}
	if _, _, err := summarize(context.Background(), nil, summarizeArgs{
		Items: []pipeline.Item{{ID: "a", Text: "текст"}},
		Fail:  true,
	}); err == nil || !strings.Contains(err.Error(), "тестовый сбой") {
		t.Fatalf("ожидалась тестовая ошибка, получено %v", err)
	}
	if _, _, err := summarize(context.Background(), nil, summarizeArgs{
		Items: []pipeline.Item{{ID: "a"}},
	}); err == nil || !strings.Contains(err.Error(), "нет текста") {
		t.Fatalf("ожидалась ошибка про отсутствие текста, получено %v", err)
	}
}

func TestSaveToFileWritesInsideOutDir(t *testing.T) {
	out := t.TempDir()
	t.Setenv("PIPELINE_OUT_DIR", out)

	_, res, err := saveToFile(context.Background(), nil, saveArgs{
		Path:    "nested/summary.md",
		Content: "# Итог\nТекст резюме.",
	})
	if err != nil {
		t.Fatalf("save_to_file: %v", err)
	}
	if !res.Saved || res.Path != "nested/summary.md" {
		t.Fatalf("неверный результат: %+v", res)
	}
	if len(res.SHA256) != 64 {
		t.Fatalf("неверный sha256: %q", res.SHA256)
	}
	body, err := os.ReadFile(filepath.Join(out, "nested", "summary.md"))
	if err != nil {
		t.Fatalf("файл не создан: %v", err)
	}
	if string(body) != "# Итог\nТекст резюме.\n" {
		t.Fatalf("неверное содержимое: %q", string(body))
	}
	if res.Bytes != len(body) {
		t.Fatalf("bytes=%d, файл=%d", res.Bytes, len(body))
	}
}

func TestSaveToFileRejectsBadPathsAndContent(t *testing.T) {
	t.Setenv("PIPELINE_OUT_DIR", t.TempDir())

	cases := []saveArgs{
		{Path: "x.md", Content: "  "},
		{Path: "/tmp/x.md", Content: "текст"},
		{Path: "../escape.md", Content: "текст"},
		{Path: "a/../../escape.md", Content: "текст"},
	}
	for _, args := range cases {
		if _, _, err := saveToFile(context.Background(), nil, args); err == nil {
			t.Fatalf("ожидалась ошибка для %+v", args)
		}
	}
	if _, _, err := saveToFile(context.Background(), nil, saveArgs{
		Path: "x.md", Content: "текст", Fail: true,
	}); err == nil || !strings.Contains(err.Error(), "тестовый сбой") {
		t.Fatalf("ожидалась тестовая ошибка, получено %v", err)
	}
}
