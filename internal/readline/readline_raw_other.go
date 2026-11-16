//go:build !darwin

package readline

// Заглушка сырого режима для платформ, отличных от macOS: lineEditor
// деградирует до обычного построчного ввода без стрелок.

import "fmt"

// TerminalWidth — заглушка для платформ без сырого режима: редактор там
// деградирует до построчного ввода и ширину терминала не использует.
func TerminalWidth() int { return 80 }

type rawState struct{}

func makeRaw(fd uintptr) (*rawState, error) {
	return nil, fmt.Errorf("raw mode не поддерживается на этой платформе")
}

func (s *rawState) restore() error { return nil }
