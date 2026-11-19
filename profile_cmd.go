package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
