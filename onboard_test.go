package main

import (
	"io"
	"strings"
	"testing"

	"github.com/DarkonGH/aiChallenge10-agent/internal/memory"
)

// fakeReader replays answers and counts questions, so the onboarding logic is
// tested without a terminal.
type fakeReader struct {
	answers []string
	asked   []string
}

func (f *fakeReader) Read(prompt string) (string, error) {
	f.asked = append(f.asked, prompt)
	if len(f.answers) == 0 {
		return "", io.EOF
	}
	ans := f.answers[0]
	f.answers = f.answers[1:]
	return ans, nil
}

func newTestApp(t *testing.T, profile []memory.Entry) *app {
	t.Helper()
	st, err := memory.Open(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(profile) > 0 {
		st.Apply(memory.Update{Profile: profile})
		if err := st.Save(); err != nil {
			t.Fatal(err)
		}
	}
	return &app{mem: st}
}

// TestOnboardProfileSkip keeps -skip-profile silent: scripts and runs must not
// see a question, and the profile on disk must stay untouched.
func TestOnboardProfileSkip(t *testing.T) {
	a := newTestApp(t, []memory.Entry{{Key: "имя", Value: "Дмитрий"}})
	a.skipProfile = true
	ed := &fakeReader{answers: []string{"n"}}
	a.onboardProfile(ed)
	if len(ed.asked) != 0 {
		t.Fatalf("задано %d вопросов при -skip-profile", len(ed.asked))
	}
	if got := a.mem.Section(memory.LayerProfile); len(got) != 1 || got[0].Value != "Дмитрий" {
		t.Fatalf("профиль изменён при -skip-profile: %v", got)
	}
}

// TestOnboardProfileReplacesOld checks the day-11 leftover path: a rejected
// profile is cleared before the questions, so an interrupted interview cannot
// leave the old persona in force.
func TestOnboardProfileReplacesOld(t *testing.T) {
	a := newTestApp(t, []memory.Entry{{Key: "имя", Value: "Дмитрий"}})
	ed := &fakeReader{answers: []string{"n", "", "", ""}}
	a.onboardProfile(ed)
	if len(a.mem.Section(memory.LayerProfile)) != 0 {
		t.Fatalf("старый профиль не очищен: %v", a.mem.Section(memory.LayerProfile))
	}
	if len(ed.asked) != 4 {
		t.Fatalf("спрошено %d раз, ждали подтверждение и три вопроса", len(ed.asked))
	}
	if !strings.Contains(ed.asked[0], "использовать этот профиль") {
		t.Fatalf("первый вопрос не про старый профиль: %q", ed.asked[0])
	}
}

// TestOnboardProfileKeepsApproved leaves an approved profile alone and asks no
// further questions: the interview is for a new user, not a daily quiz.
func TestOnboardProfileKeepsApproved(t *testing.T) {
	a := newTestApp(t, []memory.Entry{{Key: "имя", Value: "Дмитрий"}})
	ed := &fakeReader{answers: []string{"y"}}
	a.onboardProfile(ed)
	if len(ed.asked) != 1 {
		t.Fatalf("спрошено %d раз после согласия", len(ed.asked))
	}
	if got := a.mem.Section(memory.LayerProfile); len(got) != 1 {
		t.Fatalf("профиль изменился после согласия: %v", got)
	}
}

// TestOnboardProfileEmptyProfileInterviewed checks a fresh data dir: no
// confirmation question, straight to the three interview questions.
func TestOnboardProfileEmptyProfileInterviewed(t *testing.T) {
	a := newTestApp(t, nil)
	ed := &fakeReader{answers: []string{"", "", ""}}
	a.onboardProfile(ed)
	if len(ed.asked) != 3 {
		t.Fatalf("спрошено %d раз, ждали три вопроса", len(ed.asked))
	}
	if !strings.Contains(ed.asked[0], "Как к вам обращаться") {
		t.Fatalf("первый вопрос не про обращение: %q", ed.asked[0])
	}
}

// TestOnboardProfileEOF stops quietly when the input ends, leaving the profile
// as it was.
func TestOnboardProfileEOF(t *testing.T) {
	a := newTestApp(t, []memory.Entry{{Key: "имя", Value: "Дмитрий"}})
	ed := &fakeReader{}
	a.onboardProfile(ed)
	if got := a.mem.Section(memory.LayerProfile); len(got) != 1 {
		t.Fatalf("профиль изменился при EOF: %v", got)
	}
}

// TestIsNo accepts the negative answers a person actually types and treats
// everything else, including an empty Enter, as "yes".
func TestIsNo(t *testing.T) {
	no := []string{"n", "N", "no", " нет ", "не", "н"}
	for _, s := range no {
		if !isNo(s) {
			t.Errorf("%q не распознано как «нет»", s)
		}
	}
	yes := []string{"", "y", "yes", "да", "ну", "конечно"}
	for _, s := range yes {
		if isNo(s) {
			t.Errorf("%q распознано как «нет»", s)
		}
	}
}

// TestOnboardProfileRussianNo clears the old profile on «нет» too.
func TestOnboardProfileRussianNo(t *testing.T) {
	a := newTestApp(t, []memory.Entry{{Key: "имя", Value: "Дмитрий"}})
	ed := &fakeReader{answers: []string{"нет", "", "", ""}}
	a.onboardProfile(ed)
	if len(a.mem.Section(memory.LayerProfile)) != 0 {
		t.Fatalf("старый профиль не очищен по «нет»: %v", a.mem.Section(memory.LayerProfile))
	}
}
