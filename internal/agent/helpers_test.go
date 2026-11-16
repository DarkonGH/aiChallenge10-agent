package agent

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/DarkonGH/aiChallenge10-agent/internal/history"
	"github.com/DarkonGH/aiChallenge10-agent/internal/llm"
)

// newTestClient points a chat client at the stub server.
func newTestClient(srv *httptest.Server) *llm.ChatClient {
	return llm.NewChatClientAt(srv.URL, "test")
}

// seedTurns appends n user/assistant turns.
func seedTurns(t *testing.T, h *history.History, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		h.AppendUser(fmt.Sprintf("вопрос номер %d", i))
		h.AppendAssistant(fmt.Sprintf("ответ номер %d", i), nil, "m")
	}
}
