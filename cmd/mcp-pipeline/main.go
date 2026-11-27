// Command mcp-pipeline — MCP-сервер из трёх инструментов одного пайплайна.
//
// search находит файлы, summarize делает по ним экстрактивное резюме,
// save_to_file записывает резюме на диск. Оркестратора в агенте нет:
// модель сама передаёт items из search в summarize, а summary — в
// save_to_file, и останавливается на пустом результате или ошибке.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/DarkonGH/aiChallenge10-agent/internal/pipeline"
	"github.com/DarkonGH/aiChallenge10-agent/internal/workspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type searchArgs struct {
	Query string `json:"query" jsonschema:"поисковый запрос; документ должен содержать все слова"`
	Path  string `json:"path,omitempty" jsonschema:"необязательный подкаталог внутри рабочего каталога"`
	Limit int    `json:"limit,omitempty" jsonschema:"сколько документов вернуть, по умолчанию 5"`
	Empty bool   `json:"empty,omitempty" jsonschema:"тестовый режим: вернуть пустой результат, ничего не читая"`
}

type summarizeArgs struct {
	Items        []pipeline.Item `json:"items" jsonschema:"документы, полученные от search"`
	MaxSentences int             `json:"max_sentences,omitempty" jsonschema:"сколько предложений оставить, по умолчанию 5"`
	Fail         bool            `json:"fail,omitempty" jsonschema:"тестовый режим: вернуть ошибку шага 2"`
}

type saveArgs struct {
	Path    string `json:"path" jsonschema:"относительный путь внутри каталога вывода"`
	Content string `json:"content" jsonschema:"текст, который нужно сохранить"`
	Format  string `json:"format,omitempty" jsonschema:"формат, по умолчанию markdown"`
	Fail    bool   `json:"fail,omitempty" jsonschema:"тестовый режим: вернуть ошибку шага 3"`
}

func main() {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "goagent-pipeline",
		Version: "1.0.0",
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "search",
		Description: "Найти текст в рабочем каталоге модели по запросу: рекурсивно по notes, " +
			"reports и другим текстовым файлам. Возвращает count, scanned, skipped, truncated и " +
			"items с path, line, snippet и text вокруг первого совпадения. Для цепочки передай " +
			"items целиком в summarize. Пустой count означает, что данных нет: не выдумывай " +
			"документы и не вызывай summarize.",
	}, search)

	mcp.AddTool(server, &mcp.Tool{
		Name: "summarize",
		Description: "Сделать экстрактивное резюме по items от search. Передавай items из " +
			"результата search, а не придумывай их. Пустой items — ошибка: не вызывай " +
			"save_to_file на пустых данных. В ответе есть summary для следующего шага.",
	}, summarize)

	mcp.AddTool(server, &mcp.Tool{
		Name: "save_to_file",
		Description: "Сохранить готовый текст в файл внутри каталога reports рабочего каталога. " +
			"Передай в content поле summary из результата summarize. Пустой content — ошибка: " +
			"сохранять нечего.",
	}, saveToFile)

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("mcp-pipeline: %v", err)
	}
}

const (
	maxSearchFiles     = 2000
	maxSearchFileBytes = 1 << 20
	maxSearchTotal     = 20 << 20
)

func search(_ context.Context, _ *mcp.CallToolRequest, args searchArgs) (*mcp.CallToolResult, pipeline.SearchResult, error) {
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return nil, pipeline.SearchResult{}, fmt.Errorf("search: пустой query")
	}
	root := searchRoot()
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, pipeline.SearchResult{}, fmt.Errorf("search: рабочий каталог %s: %w", root, err)
	}
	result := pipeline.SearchResult{Query: query, Root: rootAbs, Items: make([]pipeline.Item, 0)}
	if args.Empty {
		body, err := jsonContent(result)
		if err != nil {
			return nil, pipeline.SearchResult{}, err
		}
		return body, result, nil
	}

	terms := tokenize(query)
	if len(terms) == 0 {
		return nil, pipeline.SearchResult{}, fmt.Errorf("search: в запросе %q нет слов для поиска", query)
	}

	base := rootAbs
	if sub := strings.TrimSpace(args.Path); sub != "" {
		base, err = workspace.Resolve(rootAbs, sub)
		if err != nil {
			return nil, pipeline.SearchResult{}, fmt.Errorf("search: path: %w", err)
		}
		st, err := os.Stat(base)
		if err != nil {
			return nil, pipeline.SearchResult{}, fmt.Errorf("search: каталог %s: %w", sub, err)
		}
		if !st.IsDir() {
			return nil, pipeline.SearchResult{}, fmt.Errorf("search: %q — не каталог", sub)
		}
	} else if st, err := os.Stat(rootAbs); err != nil {
		if os.IsNotExist(err) {
			body, jsonErr := jsonContent(result)
			if jsonErr != nil {
				return nil, pipeline.SearchResult{}, jsonErr
			}
			return body, result, nil
		}
		return nil, pipeline.SearchResult{}, fmt.Errorf("search: рабочий каталог %s: %w", rootAbs, err)
	} else if !st.IsDir() {
		return nil, pipeline.SearchResult{}, fmt.Errorf("search: %s — не каталог", rootAbs)
	}

	type scored struct {
		item  pipeline.Item
		score int
	}
	var found []scored
	var total int64

	err = filepath.WalkDir(base, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == base {
				return walkErr
			}
			result.Skipped++
			return nil
		}
		if path == base {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			result.Skipped++
			return nil
		}
		if d.IsDir() {
			if workspace.IsHiddenName(d.Name()) || workspace.SkipDir(d.Name()) {
				result.Skipped++
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			result.Skipped++
			return nil
		}
		if result.Scanned >= maxSearchFiles {
			result.Truncated = true
			return filepath.SkipAll
		}
		name := d.Name()
		if workspace.IsHiddenName(name) || workspace.IsSecretName(name) || !workspace.IsTextName(name) {
			result.Skipped++
			return nil
		}
		info, err := d.Info()
		if err != nil {
			result.Skipped++
			return nil
		}
		if info.Size() > maxSearchFileBytes {
			result.Skipped++
			return nil
		}
		if total+info.Size() > maxSearchTotal {
			result.Truncated = true
			return filepath.SkipAll
		}
		body, err := os.ReadFile(path)
		if err != nil {
			result.Skipped++
			return nil
		}
		if bytes.IndexByte(body, 0) >= 0 {
			result.Skipped++
			return nil
		}
		total += info.Size()
		result.Scanned++

		text := string(body)
		title := noteTitle(name, text)
		lower := strings.ToLower(title + "\n" + text)
		bodyLower := strings.ToLower(text)
		score := 0
		matchedAll := true
		for _, term := range terms {
			n := strings.Count(lower, term)
			if n == 0 {
				matchedAll = false
				break
			}
			score += n
		}
		if !matchedAll {
			return nil
		}
		rel, err := filepath.Rel(rootAbs, path)
		if err != nil {
			result.Skipped++
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		idx := firstMatchIndex(bodyLower, terms)
		line := 1
		if idx >= 0 {
			line += strings.Count(text[:idx], "\n")
		}
		id := strings.TrimSuffix(relSlash, filepath.Ext(relSlash))
		if id == "" {
			id = relSlash
		}
		found = append(found, scored{
			item: pipeline.Item{
				ID:      id,
				Title:   title,
				Path:    relSlash,
				Line:    line,
				Snippet: snippetAround(text, idx, 80, 220),
				Text:    snippetAround(text, idx, 800, 3200),
			},
			score: score,
		})
		return nil
	})
	if err != nil {
		return nil, pipeline.SearchResult{}, fmt.Errorf("search: обход %s: %w", base, err)
	}

	sort.SliceStable(found, func(i, j int) bool {
		if found[i].score != found[j].score {
			return found[i].score > found[j].score
		}
		return found[i].item.Path < found[j].item.Path
	})

	limit := args.Limit
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}
	if len(found) > limit {
		found = found[:limit]
	}
	for _, f := range found {
		result.Items = append(result.Items, f.item)
	}
	result.Count = len(result.Items)
	body, err := jsonContent(result)
	if err != nil {
		return nil, pipeline.SearchResult{}, err
	}
	return body, result, nil
}

func firstMatchIndex(lower string, terms []string) int {
	best := -1
	for _, term := range terms {
		if term == "" {
			continue
		}
		if i := strings.Index(lower, term); i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	return best
}

func snippetAround(text string, byteIdx, before, after int) string {
	if byteIdx < 0 {
		return truncateRunes(strings.TrimSpace(text), before+after)
	}
	runes := []rune(text)
	runeIdx := utf8.RuneCountInString(text[:byteIdx])
	start := runeIdx - before
	if start < 0 {
		start = 0
	}
	end := runeIdx + after
	if end > len(runes) {
		end = len(runes)
	}
	s := strings.TrimSpace(string(runes[start:end]))
	if start > 0 {
		s = "…" + s
	}
	if end < len(runes) {
		s += "…"
	}
	return s
}

func summarize(_ context.Context, _ *mcp.CallToolRequest, args summarizeArgs) (*mcp.CallToolResult, pipeline.SummaryResult, error) {
	if args.Fail {
		return nil, pipeline.SummaryResult{}, fmt.Errorf(
			"summarize: тестовый сбой на шаге 2 — обработка остановлена; save_to_file не вызывай")
	}
	if len(args.Items) == 0 {
		return nil, pipeline.SummaryResult{}, fmt.Errorf(
			"summarize: пустой список items — search ничего не вернул; следующий шаг не выполняй")
	}

	maxSentences := args.MaxSentences
	if maxSentences <= 0 {
		maxSentences = 5
	}
	if maxSentences > 20 {
		maxSentences = 20
	}

	seen := make(map[string]bool)
	sentences := make([]string, 0, maxSentences)
	sourceIDs := make([]string, 0, len(args.Items))
	for _, item := range args.Items {
		sourceIDs = append(sourceIDs, item.ID)
		if isCodePath(item.Path) {
			for _, line := range codeLines(item) {
				key := strings.ToLower(line)
				if seen[key] {
					continue
				}
				seen[key] = true
				sentences = append(sentences, line)
				if len(sentences) >= maxSentences {
					break
				}
			}
			if len(sentences) >= maxSentences {
				break
			}
			continue
		}
		text := item.Text
		if strings.TrimSpace(text) == "" {
			text = item.Snippet
		}
		for _, sentence := range splitSentences(text) {
			key := strings.ToLower(sentence)
			if seen[key] {
				continue
			}
			seen[key] = true
			sentences = append(sentences, sentence)
			if len(sentences) >= maxSentences {
				break
			}
		}
		if len(sentences) >= maxSentences {
			break
		}
	}
	if len(sentences) == 0 {
		return nil, pipeline.SummaryResult{}, fmt.Errorf(
			"summarize: в items нет текста для резюме — save_to_file не вызывай")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Резюме по %s:\n", documentsDative(len(args.Items)))
	for _, sentence := range sentences {
		fmt.Fprintf(&b, "- %s\n", sentence)
	}

	result := pipeline.SummaryResult{
		Count:     len(args.Items),
		Summary:   strings.TrimRight(b.String(), "\n"),
		Sentences: sentences,
		SourceIDs: sourceIDs,
	}
	body, err := jsonContent(result)
	if err != nil {
		return nil, pipeline.SummaryResult{}, err
	}
	return body, result, nil
}

func saveToFile(_ context.Context, _ *mcp.CallToolRequest, args saveArgs) (*mcp.CallToolResult, pipeline.SaveResult, error) {
	if args.Fail {
		return nil, pipeline.SaveResult{}, fmt.Errorf(
			"save_to_file: тестовый сбой на шаге 3 — файл не создан")
	}
	contentText := strings.TrimSpace(args.Content)
	if contentText == "" {
		return nil, pipeline.SaveResult{}, fmt.Errorf(
			"save_to_file: пустой content — сохранять нечего; цепочка остановлена")
	}

	out := outDir()
	if strings.TrimSpace(args.Path) == "" {
		return nil, pipeline.SaveResult{}, fmt.Errorf("save_to_file: path обязателен")
	}
	full, err := workspace.Resolve(out, args.Path)
	if err != nil {
		return nil, pipeline.SaveResult{}, fmt.Errorf("save_to_file: %w", err)
	}
	rel, err := workspace.Rel(out, full)
	if err != nil {
		return nil, pipeline.SaveResult{}, fmt.Errorf("save_to_file: %w", err)
	}
	if workspace.HasSecretComponent(rel) {
		return nil, pipeline.SaveResult{}, fmt.Errorf("save_to_file: путь %q похож на секрет; запись запрещена", rel)
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return nil, pipeline.SaveResult{}, fmt.Errorf("save_to_file: каталог: %w", err)
	}

	data := []byte(args.Content)
	if !strings.HasSuffix(args.Content, "\n") {
		data = append(data, '\n')
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return nil, pipeline.SaveResult{}, fmt.Errorf("save_to_file: запись %s: %w", full, err)
	}
	sum := sha256.Sum256(data)
	format := strings.TrimSpace(args.Format)
	if format == "" {
		format = "markdown"
	}

	result := pipeline.SaveResult{
		Saved:  true,
		Path:   rel,
		Bytes:  len(data),
		SHA256: hex.EncodeToString(sum[:]),
		Format: format,
	}
	body, err := jsonContent(result)
	if err != nil {
		return nil, pipeline.SaveResult{}, err
	}
	return body, result, nil
}

func searchRoot() string {
	if dir := strings.TrimSpace(os.Getenv("PIPELINE_SEARCH_ROOT")); dir != "" {
		return dir
	}
	if dir := strings.TrimSpace(os.Getenv("PIPELINE_NOTES_DIR")); dir != "" {
		return dir
	}
	if st, err := os.Stat("testdata/day19-notes"); err == nil && st.IsDir() {
		return "testdata/day19-notes"
	}
	return filepath.Join(homeDir(), ".local", "share", "goagent", "pipeline", "notes")
}

func outDir() string {
	if dir := strings.TrimSpace(os.Getenv("PIPELINE_OUT_DIR")); dir != "" {
		return dir
	}
	return filepath.Join(homeDir(), ".local", "share", "goagent", "pipeline", "reports")
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}

func isCodePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".py", ".js", ".ts", ".tsx", ".jsx", ".rb", ".php", ".pl", ".lua", ".r",
		".java", ".kt", ".kts", ".swift", ".c", ".h", ".cc", ".cpp", ".hpp", ".rs",
		".sh", ".bash", ".zsh", ".fish", ".ps1", ".sql":
		return true
	default:
		return false
	}
}

// codeLines turns the snippet around the match into a few short file:line
// bullets. Code is not prose: sentence splitting on dots would cut it badly.
func codeLines(item pipeline.Item) []string {
	text := item.Snippet
	if strings.TrimSpace(text) == "" {
		text = item.Text
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "…" {
			continue
		}
		out = append(out, fmt.Sprintf("%s:%d: %s", item.Path, item.Line, line))
		if len(out) >= 3 {
			break
		}
	}
	return out
}

func noteTitle(name, text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			if title := strings.TrimSpace(strings.TrimPrefix(line, "# ")); title != "" {
				return title
			}
		}
	}
	return strings.TrimSuffix(name, filepath.Ext(name))
}

func tokenize(query string) []string {
	fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	seen := make(map[string]bool)
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if field == "" || seen[field] {
			continue
		}
		seen[field] = true
		out = append(out, field)
	}
	return out
}

func splitSentences(text string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		s := strings.TrimSpace(b.String())
		s = strings.TrimSpace(strings.TrimLeft(s, "-*•"))
		b.Reset()
		if len([]rune(s)) < 20 {
			return
		}
		if strings.HasPrefix(s, "#") {
			return
		}
		out = append(out, s)
	}
	for _, r := range text {
		switch r {
		case '.', '!', '?', '\n', ';':
			flush()
		default:
			b.WriteRune(r)
		}
	}
	flush()
	return out
}

func documentsDative(n int) string {
	switch {
	case n%10 == 1 && n%100 != 11:
		return fmt.Sprintf("%d документу", n)
	default:
		return fmt.Sprintf("%d документам", n)
	}
}

func truncateRunes(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
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
