package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/DarkonGH/aiChallenge10-agent/internal/compact"
	"github.com/DarkonGH/aiChallenge10-agent/internal/config"
	"github.com/DarkonGH/aiChallenge10-agent/internal/memory"
	"github.com/DarkonGH/aiChallenge10-agent/internal/tokens"
)

// profileCommand handles /profile: what the current persona looks like, which
// profiles exist, switching and manual edits. The profile is a file of its own
// (not a layer of the session), so one agent can serve several people, and the
// same person keeps the same preferences across tasks.
func (a *app) profileCommand(arg string) error {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		a.printProfile()
		return nil
	}
	switch strings.ToLower(fields[0]) {
	case "list":
		a.printProfiles()
	case "use":
		if len(fields) != 2 {
			return fmt.Errorf("нужно: /profile use <имя>")
		}
		return a.useProfile(fields[1])
	case "set":
		rest := strings.TrimSpace(strings.TrimPrefix(arg, fields[0]))
		key, value, ok := strings.Cut(rest, "=")
		if !ok || strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			return fmt.Errorf("нужно: /profile set ключ=значение")
		}
		if err := a.mem.Add(memory.LayerProfile, strings.TrimSpace(key), strings.TrimSpace(value)); err != nil {
			return err
		}
		if err := a.mem.Save(); err != nil {
			return err
		}
		fmt.Printf("профиль «%s» += %s\n", a.profileLabel(), strings.TrimSpace(key))
		a.printProfile()
	case "clear":
		if err := a.mem.Clear(memory.LayerProfile); err != nil {
			return err
		}
		if err := a.mem.Save(); err != nil {
			return err
		}
		fmt.Printf("профиль «%s» очищен\n", a.profileLabel())
		a.printProfile()
	default:
		return fmt.Errorf("неизвестная подкоманда %q; /profile | /profile list | /profile use <имя> | /profile set ключ=значение | /profile clear", fields[0])
	}
	return nil
}

// promptReader is what onboarding needs from the readline editor; the editor
// type itself is unexported, so the dialogue depends on the method, not the type.
type promptReader interface {
	Read(prompt string) (string, error)
}

// onboardProfile is the day-12 dialogue at an interactive start: the agent
// asks about the user and folds the answers into the profile with the same
// extractor that routes the dialogue. A profile left from a previous task is
// shown first and can be replaced — that is how the day-11 «Дмитрий» stops
// shaping answers nobody asked for.
//
// Scripts and non-interactive runs skip it: -skip-profile, -script and a
// non-TTY stdin all go straight to the usual startup line.
func (a *app) onboardProfile(ed promptReader) {
	a.printProfileLine()
	if a.skipProfile || !isTerminal(os.Stdin) {
		return
	}
	cleared := false
	if a.mem.Len(memory.LayerProfile) > 0 {
		ans, err := ed.Read("использовать этот профиль? [Y/n]: ")
		if err != nil {
			return
		}
		if !isNo(ans) {
			return
		}
		// The user rejected the old profile: clear it on disk right away, so a
		// failed model call cannot quietly bring the old persona back.
		if err := a.mem.Clear(memory.LayerProfile); err != nil {
			fmt.Println("ошибка:", err)
			return
		}
		if err := a.mem.Save(); err != nil {
			fmt.Println("ошибка:", err)
			return
		}
		fmt.Println("старый профиль очищен")
		cleared = true
	}
	a.interviewProfile(ed, cleared)
}

// interviewProfile asks three short questions and lets the extractor decide
// what is a profile record. The answers are not written by code: the same
// model that routes the dialogue chooses the keys and the layer.
func (a *app) interviewProfile(ed promptReader, cleared bool) {
	fmt.Println("Заполним профиль из диалога: три вопроса, Enter — пропустить.")
	questions := []struct{ label, text string }{
		{"как обращаться", "Как к вам обращаться и кем вы работаете? "},
		{"стиль и формат", "Как отвечать: кратко или подробно, с кодом или без? "},
		{"ограничения", "Чего не делать в ответах? "},
	}
	var b strings.Builder
	for _, q := range questions {
		ans, err := ed.Read(q.text)
		if err != nil {
			return
		}
		if ans = strings.TrimSpace(ans); ans == "" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", q.label, ans)
	}
	if b.Len() == 0 {
		if cleared {
			fmt.Println("старый профиль очищен, новый не задан")
		} else {
			fmt.Println("профиль не изменён")
		}
		return
	}

	fmt.Println("разбираю ответы…")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out, err := a.agent.Comp.ExtractMemory(ctx, a.mem, b.String())
	if err != nil {
		fmt.Printf("профиль: модель не ответила (%s) — профиль остался как был\n", config.OneLine(err.Error()))
		return
	}
	upd, perr := compact.ParseMemoryJSON(out)
	if perr != nil {
		fmt.Printf("профиль: %s — профиль остался как был\n", perr)
		return
	}
	a.mem.Apply(upd)
	if err := a.mem.Save(); err != nil {
		fmt.Println("профиль: сохранить не удалось:", err)
		return
	}
	if err := a.hist.AppendMemory(upd); err != nil {
		fmt.Println("профиль: журнал:", err)
	}
	if n := len(upd.Profile); n > 0 {
		keys := make([]string, 0, n)
		for _, e := range upd.Profile {
			keys = append(keys, e.Key)
		}
		fmt.Printf("профиль пополнен из диалога: %s\n", strings.Join(keys, ", "))
	} else {
		fmt.Println("модель не нашла устойчивых предпочтений — профиль прежний")
	}
	a.printProfile()
}

// printProfileLine is the startup reminder: which persona is applied to every
// request. Without it the profile is invisible until /profile, and a shared
// profile left from a previous task silently shapes the answers.
func (a *app) printProfileLine() {
	entries := a.mem.Section(memory.LayerProfile)
	if len(entries) == 0 {
		fmt.Printf("профиль «%s» пуст: скажите, как отвечать («пиши кратко», «без кода», «на ты»), или /profile set стиль=кратко\n", a.profileLabel())
		return
	}
	hint := ""
	if e := entries[0]; e.Key != "" {
		hint = fmt.Sprintf(" · %s: %s", e.Key, config.Truncate(e.Value, 40))
	}
	fmt.Printf("профиль «%s»: %d %s%s\n", a.profileLabel(), len(entries), recordsWord(len(entries)), hint)
	if a.profile == "" {
		fmt.Println("  это общий профиль из прошлых задач: /profile — посмотреть, /profile clear — очистить, /profile use <имя> — другой")
	}
}

// printProfile is the teaching view of day 12: the preferences that go into
// every request, what they cost and where the file lives.
func (a *app) printProfile() {
	entries := a.mem.Section(memory.LayerProfile)
	block := a.mem.Block(memory.LayerProfile, "")
	fmt.Printf("профиль «%s»: %d/%d записей, ~%d ток., всегда в промпте (последним блоком)\n",
		a.profileLabel(), len(entries), memory.Caps[memory.LayerProfile], tokens.EstimateTokens(block))
	if len(entries) == 0 {
		fmt.Println("  пусто — скажите агенту, как отвечать («пиши кратко», «без кода», «на ты»), или задайте вручную: /profile set стиль=кратко")
	}
	for _, e := range entries {
		fmt.Printf("  - %s: %s\n", e.Key, e.Value)
	}
	if dir := a.mem.ProfileDir(); dir != "" {
		fmt.Printf("  файл: %s\n", filepath.Join(dir, "profile.json"))
	}
}

// profileInfo is one profile that exists on disk.
type profileInfo struct {
	name  string // "" is the day-11 shared default
	label string
	dir   string
}

// printProfiles lists the profiles the agent can switch to. The shared default
// is shown only when it already has a file, so an empty data dir does not look
// like it has a profile.
func (a *app) printProfiles() {
	infos := a.profileInfos()
	if len(infos) == 0 {
		fmt.Println("профилей пока нет: /profile set стиль=кратко создаст общий профиль")
		return
	}
	fmt.Println("профили:")
	for _, inf := range infos {
		entries, err := memory.ReadProfile(filepath.Join(inf.dir, "profile.json"))
		mark := " "
		if inf.name == a.profile {
			mark = "*"
		}
		if err != nil {
			fmt.Printf(" %s %-12s ошибка чтения: %v\n", mark, inf.label, err)
			continue
		}
		fmt.Printf(" %s %-12s %d/%d записей\n", mark, inf.label, len(entries), memory.Caps[memory.LayerProfile])
	}
	fmt.Println("* — активный; переключить: /profile use <имя>")
}

// profileInfos scans the data dir for profiles: the shared default plus every
// directory under profiles/. A directory counts even when profile.json is not
// written yet — /profile use creates it on the first saved preference.
func (a *app) profileInfos() []profileInfo {
	var out []profileInfo
	shared := filepath.Join(a.dataDir, "shared")
	if _, err := os.Stat(filepath.Join(shared, "profile.json")); err == nil {
		out = append(out, profileInfo{name: "", label: "общий", dir: shared})
	}
	dirs, err := os.ReadDir(filepath.Join(a.dataDir, "profiles"))
	if err == nil {
		for _, d := range dirs {
			if !d.IsDir() {
				continue
			}
			dir := filepath.Join(a.dataDir, "profiles", d.Name())
			out = append(out, profileInfo{name: d.Name(), label: d.Name(), dir: dir})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// useProfile switches the active profile without touching either file: the
// previous persona stays on disk exactly as it was.
func (a *app) useProfile(name string) error {
	name = strings.TrimSpace(name)
	if name == "общий" || name == "shared" || name == "default" {
		name = ""
	}
	if name != "" {
		if err := validProfileName(name); err != nil {
			return err
		}
	}
	if err := a.mem.LoadProfile(profileDirFor(a.dataDir, name)); err != nil {
		return err
	}
	a.profile = name
	fmt.Printf("профиль: «%s»\n", a.profileLabel())
	a.printProfile()
	return nil
}

// profileLabel is the human name of the active profile.
func (a *app) profileLabel() string {
	if a.profile == "" {
		return "общий"
	}
	return a.profile
}

// isNo reads a negative answer in both languages: the confirmation defaults to
// "yes", so only an explicit "no" throws the old profile away.
func isNo(ans string) bool {
	switch strings.ToLower(strings.TrimSpace(ans)) {
	case "n", "no", "н", "нет", "не":
		return true
	}
	return false
}

// recordsWord picks the Russian plural for "запись" after a count: 1 запись,
// 2 записи, 5 записей.
func recordsWord(n int) string {
	if n%100 >= 11 && n%100 <= 14 {
		return "записей"
	}
	switch n % 10 {
	case 1:
		return "запись"
	case 2, 3, 4:
		return "записи"
	}
	return "записей"
}

// validProfileName keeps a profile inside <data>/profiles: no separators and no
// dots, only letters, digits, dash and underscore.
func validProfileName(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("пустое имя профиля")
	}
	for _, r := range name {
		ok := r == '-' || r == '_' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			(r >= 'а' && r <= 'я') || (r >= 'А' && r <= 'Я') || r == 'ё' || r == 'Ё'
		if !ok {
			return fmt.Errorf("в имени профиля %q допустимы буквы, цифры, дефис и подчёркивание", name)
		}
	}
	return nil
}
