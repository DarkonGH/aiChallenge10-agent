package forex

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultStorePath возвращает путь к журналу замеров. Демо задаёт
// FOREX_STORE, чтобы данные не попадали в рабочий каталог репозитория.
// По умолчанию журнал лежит рядом с данными агента, а не в конкретной
// сессии: замеры — это временной ряд, который должен переживать /new,
// перезапуск и смену диалога.
func DefaultStorePath() string {
	if path := strings.TrimSpace(os.Getenv("FOREX_STORE")); path != "" {
		return path
	}
	if dir := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); dir != "" {
		return filepath.Join(dir, "goagent", "forex", "forex.jsonl")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join("data", "forex", "forex.jsonl")
	}
	return filepath.Join(home, ".local", "share", "goagent", "forex", "forex.jsonl")
}

// Store — append-only JSONL-журнал. Каждый запуск планировщика добавляет
// одну строку; старые замеры не перезаписываются.
type Store struct {
	mu    sync.Mutex
	path  string
	count int
}

// NewStore открывает (или создаёт) журнал и считает уже накопленные записи.
func NewStore(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		path = DefaultStorePath()
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("журнал forex: каталог %s: %w", dir, err)
		}
	}

	store := &Store{path: path}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("журнал forex: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		var tick Tick
		if err := json.Unmarshal([]byte(text), &tick); err != nil {
			return nil, fmt.Errorf("журнал forex: строка %d: %w", line, err)
		}
		store.count++
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("журнал forex: %w", err)
	}
	return store, nil
}

// Path возвращает путь журнала (для диагностики и тестов).
func (s *Store) Path() string { return s.path }

// Count возвращает число записей в журнале.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

// Append добавляет замер и возвращает общее число записей.
func (s *Store) Append(tick Tick) (int, error) {
	if err := normalizeTick(&tick); err != nil {
		return 0, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, fmt.Errorf("журнал forex: %w", err)
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(tick); err != nil {
		_ = f.Close()
		return 0, fmt.Errorf("журнал forex: запись: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return 0, fmt.Errorf("журнал forex: sync: %w", err)
	}
	if err := f.Close(); err != nil {
		return 0, fmt.Errorf("журнал forex: закрытие: %w", err)
	}
	s.count++
	return s.count, nil
}

// ReadPeriod читает замеры за интервал [from, to]. Нулевые границы означают
// «без ограничения». Пустой quote означает «все пары».
func (s *Store) ReadPeriod(from, to time.Time, quote string) ([]Tick, error) {
	quote = strings.ToUpper(strings.TrimSpace(quote))

	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("журнал forex: %w", err)
	}
	defer f.Close()

	var out []Tick
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		var tick Tick
		if err := json.Unmarshal([]byte(text), &tick); err != nil {
			return nil, fmt.Errorf("журнал forex: строка %d: %w", line, err)
		}
		if quote != "" && strings.ToUpper(tick.Quote) != quote {
			continue
		}
		at, err := parseTickTime(tick)
		if err != nil {
			return nil, fmt.Errorf("журнал forex: строка %d: %w", line, err)
		}
		if !from.IsZero() && at.Before(from) {
			continue
		}
		if !to.IsZero() && at.After(to) {
			continue
		}
		out = append(out, tick)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("журнал forex: %w", err)
	}
	return out, nil
}

func normalizeTick(tick *Tick) error {
	tick.Base = strings.ToUpper(strings.TrimSpace(tick.Base))
	tick.Quote = strings.ToUpper(strings.TrimSpace(tick.Quote))
	if !validCurrency(tick.Base) || !validCurrency(tick.Quote) {
		return fmt.Errorf("некорректная валютная пара %q/%q", tick.Base, tick.Quote)
	}
	if tick.Rate <= 0 {
		return fmt.Errorf("некорректный курс %v", tick.Rate)
	}
	if strings.TrimSpace(tick.FetchedAt) == "" {
		tick.FetchedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if _, err := parseTickTime(*tick); err != nil {
		return err
	}
	tick.Source = strings.TrimSpace(tick.Source)
	return nil
}

func parseTickTime(tick Tick) (time.Time, error) {
	at, err := time.Parse(time.RFC3339Nano, tick.FetchedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("fetched_at %q: %w", tick.FetchedAt, err)
	}
	return at, nil
}
