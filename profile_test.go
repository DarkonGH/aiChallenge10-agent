package main

import "testing"

// TestValidProfileName keeps a profile name inside <data>/profiles: letters,
// digits, dash and underscore only — no separators and no dots.
func TestValidProfileName(t *testing.T) {
	good := []string{"кратко", "подробно", "brief", "test-1", "a_b", "Ёж"}
	for _, name := range good {
		if err := validProfileName(name); err != nil {
			t.Errorf("имя %q отвергнуто: %v", name, err)
		}
	}
	bad := []string{"", ".", "..", "a/b", `a\b`, "../x", "a b", "a.b"}
	for _, name := range bad {
		if err := validProfileName(name); err == nil {
			t.Errorf("имя %q принято", name)
		}
	}
}
