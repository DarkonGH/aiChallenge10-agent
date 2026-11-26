package main

import (
	"testing"
	"time"
)

func TestSecondsDuration(t *testing.T) {
	got, err := secondsDuration(3600)
	if err != nil {
		t.Fatalf("3600: %v", err)
	}
	if got != time.Hour {
		t.Fatalf("3600 = %s, ожидался час", got)
	}
	if _, err := secondsDuration(0); err == nil {
		t.Fatal("0 должен быть ошибкой")
	}
	const maxSeconds = int64(^uint64(0)>>1) / int64(time.Second)
	if _, err := secondsDuration(int(maxSeconds + 1)); err == nil {
		t.Fatal("переполнение duration должно быть ошибкой")
	}
}
