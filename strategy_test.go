package main

import (
	"reflect"
	"testing"
)

func TestParseStrategy(t *testing.T) {
	for _, s := range []string{"summary", "window", "facts"} {
		if got, err := ParseStrategy(s); err != nil || got != Strategy(s) {
			t.Errorf("ParseStrategy(%q) = %v, %v", s, got, err)
		}
	}
	if _, err := ParseStrategy("нет"); err == nil {
		t.Error("ожидали ошибку для неизвестной стратегии")
	}
	if StrategySummary.needsWindow() {
		t.Error("summary не требует окно")
	}
	if !StrategyWindow.needsWindow() || !StrategyFacts.needsWindow() {
		t.Error("window и facts требуют окно")
	}
}

func TestParseFactsJSON(t *testing.T) {
	got, err := parseFactsJSON(`{"цель": "переезд офиса", "срок": "15 декабря", "мест": 42}`)
	if err != nil {
		t.Fatal(err)
	}
	want := []Fact{
		{Key: "цель", Value: "переезд офиса"},
		{Key: "срок", Value: "15 декабря"},
		{Key: "мест", Value: "42"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("facts = %+v, want %+v", got, want)
	}
}

func TestParseFactsJSONToleratesFencesAndProse(t *testing.T) {
	in := "Вот обновлённые факты:\n```json\n{\"ключ\": \"значение\"}\n```\n"
	got, err := parseFactsJSON(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Key != "ключ" || got[0].Value != "значение" {
		t.Errorf("facts = %+v", got)
	}
}

func TestParseFactsJSONDuplicateKeyWins(t *testing.T) {
	got, err := parseFactsJSON(`{"срок": "20 ноября", "срок": "15 декабря"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Value != "15 декабря" {
		t.Errorf("facts = %+v, хотим один ключ с последним значением", got)
	}
}

func TestParseFactsJSONRejectsNonObject(t *testing.T) {
	if _, err := parseFactsJSON("[1,2,3]"); err == nil {
		t.Error("ожидали ошибку для массива вместо объекта")
	}
	if _, err := parseFactsJSON("не JSON вовсе"); err == nil {
		t.Error("ожидали ошибку для текста без JSON")
	}
}

func TestParseFactsJSONSkipsEmptyPairs(t *testing.T) {
	got, err := parseFactsJSON(`{"": "x", "пусто": "", "ок": "да"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Key != "ок" {
		t.Errorf("facts = %+v, хотим только «ок»", got)
	}
}

func TestRenderFacts(t *testing.T) {
	got := renderFacts([]Fact{{Key: "срок", Value: "15 декабря"}, {Key: "бюджет", Value: "480 000"}})
	want := "- срок: 15 декабря\n- бюджет: 480 000"
	if got != want {
		t.Errorf("renderFacts = %q, want %q", got, want)
	}
}
