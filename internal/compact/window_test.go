package compact

import (
	"github.com/DarkonGH/aiChallenge10-agent/internal/history"
	"github.com/DarkonGH/aiChallenge10-agent/internal/llm"
	"github.com/DarkonGH/aiChallenge10-agent/internal/tokens"
	"testing"
)

func TestEnforceWindowKeepsToolGroups(t *testing.T) {
	dir := t.TempDir()
	h, err := history.OpenHistory(dir, "s", "m", "m", "sys")
	if err != nil {
		t.Fatal(err)
	}

	// 4 turns; the second one carries a tool group that must stay intact.
	seedTurns(t, h, 1)
	h.AppendUser("вопрос с инструментом")
	h.AppendAssistant("", []llm.ToolCall{{ID: "c1", Type: "function",
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "calc", Arguments: "1+1"}}}, "m")
	h.AppendTool("c1", "calc", "2")
	seedTurns(t, h, 2) // всего 8 сообщений

	comp := NewCompactor(h, nil, DefaultBudget(), tokens.NewCalibrator(), 800)
	ok, err := comp.EnforceWindow(4)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ожидали вытеснение: сообщений больше окна")
	}

	active := h.Active()
	if len(active) > 4 {
		t.Errorf("active = %d, хотим <= 4", len(active))
	}
	if active[0].Role != "user" {
		t.Errorf("окно начинается с %q, хотим user (не рвать пары assistant+tool)", active[0].Role)
	}
	assertNoOrphanTools(t, active)
	if h.HasSummary() {
		t.Error("вытеснение окна не должно создавать конспект")
	}
	archived := len(h.Archived())

	// Replay from disk must reproduce the same split.
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	re, err := history.OpenHistory(dir, "s", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer re.Close()
	if len(re.Active()) != len(active) {
		t.Errorf("после replay active = %d, было %d", len(re.Active()), len(active))
	}
	if len(re.Archived()) != archived {
		t.Errorf("после replay archived = %d, было %d", len(re.Archived()), archived)
	}

	// Окно больше, чем сообщений: вытеснять нечего.
	if ok, err := comp.EnforceWindow(100); err != nil || ok {
		t.Errorf("EnforceWindow(100) = %v, %v; хотели false", ok, err)
	}
}

func TestPromptWindowCut(t *testing.T) {
	var msgs []*llm.Message
	for i := 0; i < 3; i++ {
		msgs = append(msgs,
			&llm.Message{Role: "user", Seq: int64(i*2 + 1)},
			&llm.Message{Role: "assistant", Seq: int64(i*2 + 2)})
	}
	// Окно 4: ровно 4 последних сообщения — ходы 2 и 3.
	if cut := windowCut(msgs, 4); cut != 2 {
		t.Errorf("windowCut(4) = %d, хотим 2", cut)
	}
	if cut := windowCut(msgs, 6); cut != 0 {
		t.Errorf("windowCut(6) = %d, хотим 0 (нечего резать)", cut)
	}
	// Окно меньше хода: всё равно двигаемся, оставляя последний ход целиком.
	if cut := windowCut(msgs, 1); cut != 5 {
		t.Errorf("windowCut(1) = %d, хотим 5 (последний ход)", cut)
	}

	// Срез не должен начинать окно с tool-результата: пара assistant+tool
	// переезжает в окно целиком, сдвиг — только на один такой результат.
	withTools := []*llm.Message{
		{Role: "user", Seq: 1},
		{Role: "assistant", Seq: 2},
		{Role: "user", Seq: 3},
		{Role: "assistant", Seq: 4},
		{Role: "tool", Seq: 5},
		{Role: "tool", Seq: 6},
		{Role: "assistant", Seq: 7},
		{Role: "user", Seq: 8},
		{Role: "assistant", Seq: 9},
	}
	// Окно 5 → срез на seq 5 (tool) → сдвиг до seq 7: окно 7..9.
	if cut := windowCut(withTools, 5); cut != 6 {
		t.Errorf("windowCut(tools,5) = %d, хотим 6 (tool-результат не отрывается)", cut)
	}
	// Окно 4 → срез на seq 6 (tool) → сдвиг до seq 7: окно 7..9.
	if cut := windowCut(withTools, 4); cut != 6 {
		t.Errorf("windowCut(tools,4) = %d, хотим 6", cut)
	}
	// Окно 3 → срез на seq 7 (assistant): окно 7..9 без сдвига.
	if cut := windowCut(withTools, 3); cut != 6 {
		t.Errorf("windowCut(tools,3) = %d, хотим 6", cut)
	}
}
