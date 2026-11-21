package main

import (
	"fmt"
	"strings"

	"github.com/DarkonGH/aiChallenge10-agent/internal/invariant"
)

// invariantCommand handles /invariant. The set is a workspace file changed
// only by this command: there is no tool for it, so a dialogue cannot rewrite
// its own constraints. Every change is journalled with a from → to snapshot.
func (a *app) invariantCommand(arg string) error {
	verb, rest, _ := strings.Cut(strings.TrimSpace(arg), " ")
	verb = strings.ToLower(verb)
	rest = strings.TrimSpace(rest)

	switch verb {
	case "", "list":
		a.printInvariants()
		return nil
	case "add":
		return a.invariantUpsert(rest, false)
	case "change":
		return a.invariantUpsert(rest, true)
	case "remove", "delete":
		return a.invariantRemove(rest)
	case "history", "log":
		a.printInvariantHistory(rest)
		return nil
	default:
		return fmt.Errorf("неизвестная подкоманда /invariant %q; допустимы list, add, change, remove, history", verb)
	}
}

func (a *app) printInvariants() {
	fmt.Println("файл инвариантов:", a.inv.Path())
	if !a.inv.Active() {
		fmt.Println("инвариантов нет.")
		printInvariantHelp()
		return
	}
	fmt.Printf("инвариантов: %d; изменений в журнале: %d\n", a.inv.Len(), len(a.inv.Changes("")))
	for _, line := range a.inv.Lines() {
		fmt.Println(" ", line)
	}
	fmt.Println("изменить: /invariant change <id> <area> <правило> :: <текст>; журнал: /invariant history")
}

func (a *app) invariantUpsert(rest string, change bool) error {
	head, text, ok := strings.Cut(rest, "::")
	if !ok || strings.TrimSpace(text) == "" {
		return fmt.Errorf("нужен текст после ::; пример: /invariant change budget budget max=400000 :: Бюджет не более 400 000 ₽/мес")
	}
	fields := strings.Fields(head)
	if len(fields) < 3 {
		return fmt.Errorf("нужны id, область и правило; пример: /invariant add db database deny=mysql,mongodb :: Только PostgreSQL 15")
	}
	id, area := fields[0], fields[1]
	rule, err := invariant.ParseRule(strings.Join(fields[2:], " "))
	if err != nil {
		return err
	}

	if change {
		if err := a.inv.Change(id, area, text, rule, "user"); err != nil {
			return err
		}
		changes := a.inv.Changes(id)
		c := changes[len(changes)-1]
		fmt.Printf("инвариант изменён: [%s]\n", c.ID)
		fmt.Printf("  было:  %s\n", invariant.Describe(*c.From))
		fmt.Printf("  стало: %s\n", invariant.Describe(*c.To))
		fmt.Println("изменение записано в журнал; со следующего запроса модель видит новое правило.")
		return nil
	}

	if err := a.inv.Add(id, area, text, rule, "user"); err != nil {
		return err
	}
	fmt.Printf("инвариант добавлен: %s\n", invariant.Describe(*a.inv.Find(id)))
	fmt.Println("со следующего запроса правило попадает в промпт; проверка — кодом до выдачи ответа.")
	return nil
}

func (a *app) invariantRemove(rest string) error {
	id := strings.TrimSpace(rest)
	if id == "" {
		return fmt.Errorf("укажите id: /invariant remove <id>")
	}
	it := a.inv.Find(id)
	if it == nil {
		return fmt.Errorf("нет инварианта %q", id)
	}
	if err := a.inv.Remove(id, "user"); err != nil {
		return err
	}
	fmt.Printf("инвариант удалён: %s\n", invariant.Describe(*it))
	fmt.Println("удаление записано в журнал; правило больше не проверяется.")
	return nil
}

func (a *app) printInvariantHistory(id string) {
	changes := a.inv.Changes(id)
	if len(changes) == 0 {
		if id == "" {
			fmt.Println("журнал изменений пуст.")
		} else {
			fmt.Printf("у инварианта %q изменений нет.\n", id)
		}
		return
	}
	if id == "" {
		fmt.Printf("журнал инвариантов (%s):\n", a.inv.Path())
	} else {
		fmt.Printf("журнал инварианта %s:\n", strings.ToLower(id))
	}
	for _, c := range changes {
		switch c.Kind {
		case "add":
			fmt.Printf("  %s  add     %s  %s\n", c.At, c.Who, invariant.Describe(*c.To))
		case "remove":
			fmt.Printf("  %s  remove  %s  %s\n", c.At, c.Who, invariant.Describe(*c.From))
		case "change":
			fmt.Printf("  %s  change  %s\n", c.At, c.Who)
			fmt.Printf("      было:  %s\n", invariant.Describe(*c.From))
			fmt.Printf("      стало: %s\n", invariant.Describe(*c.To))
		default:
			fmt.Printf("  %s  %s  %s\n", c.At, c.Kind, c.ID)
		}
	}
}

func printInvariantHelp() {
	fmt.Println("добавить: /invariant add <id> <area> <правило> :: <текст>")
	fmt.Println("изменить: /invariant change <id> <area> <правило> :: <текст>")
	fmt.Println("удалить:  /invariant remove <id>")
	fmt.Println("журнал:   /invariant history [id]")
	fmt.Println("правила:  deny=mysql,mongodb  allow=postgres  max=300000 (можно несколько через пробел)")
}
