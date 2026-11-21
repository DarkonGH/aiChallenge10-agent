package config

import (
	"math"
	"testing"
)

func TestDefaultModelFromEnv(t *testing.T) {
	t.Setenv("LLM_MODEL", "glm-5.3-flash")
	t.Setenv("LLM_SUMMARY_MODEL", "")
	if got := DefaultModel(); got != "glm-5.3-flash" {
		t.Fatalf("DefaultModel() = %q, want LLM_MODEL", got)
	}
	if got := DefaultSummaryModel(); got != "opencode-go/deepseek-v4.1-flash" {
		t.Fatalf("DefaultSummaryModel() = %q, want cheapest in roster (opencode-go/deepseek-v4.1-flash)", got)
	}

	t.Setenv("LLM_SUMMARY_MODEL", "qwen3.8-27b")
	if got := DefaultSummaryModel(); got != "qwen3.8-27b" {
		t.Fatalf("DefaultSummaryModel() = %q, want LLM_SUMMARY_MODEL", got)
	}

	t.Setenv("LLM_MODEL", "")
	t.Setenv("LLM_SUMMARY_MODEL", "")
	if got := DefaultModel(); got != ModelSet[0].ID {
		t.Fatalf("DefaultModel() = %q, want roster fallback %q", got, ModelSet[0].ID)
	}
}

// TestCostUsdWithCache covers the split bill: fresh prompt tokens, cached
// prompt tokens and completion tokens have three different rates. The cached
// ones are part of prompt_tokens, so counting the whole prompt at RateInPerM
// overstates the cost by ~50x when the provider serves it from its cache.
func TestCostUsdWithCache(t *testing.T) {
	m := Model{ID: "test", RateInPerM: 1, RateOutPerM: 2, RateCachePerM: 0.01}

	if got, want := m.CostUsd(1_000_000, 0, 1_000_000), 1.0+2.0; got != want {
		t.Fatalf("no cache: CostUsd = %v, want %v", got, want)
	}
	if got, want := m.CostUsd(1_000_000, 1_000_000, 0), 0.01; got != want {
		t.Fatalf("all cached: CostUsd = %v, want %v", got, want)
	}
	// Half the prompt cached: 0.5 fresh at 1.0 + 0.5 cached at 0.01.
	if got, want := m.CostUsd(1_000_000, 500_000, 0), 0.5+0.005; got != want {
		t.Fatalf("half cached: CostUsd = %v, want %v", got, want)
	}
	// A provider that reports no cache rate bills cached tokens as input.
	flat := Model{ID: "flat", RateInPerM: 1}
	if got, want := flat.CostUsd(1_000_000, 1_000_000, 0), 1.0; got != want {
		t.Fatalf("no cache rate: CostUsd = %v, want %v", got, want)
	}
	// Cached can never exceed the prompt: a bad report must not create a
	// discount larger than the request. Floating point makes the exact compare
	// noisy at 1e-7, so allow a tiny epsilon.
	if got, want := m.CostUsd(10, 100, 0), 10*0.01/1e6; math.Abs(got-want) > 1e-12 {
		t.Fatalf("cached > prompt: CostUsd = %v, want %v", got, want)
	}
}
