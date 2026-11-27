// Package workspace defines the one directory the model may read and write:
// ~/goagent-work by default. Every path is resolved inside that root, so a
// tool cannot escape with "..", an absolute path or a symlink.
package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// DefaultName is the directory under the user's home.
const DefaultName = "goagent-work"

// DefaultRoot returns ~/goagent-work.
func DefaultRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("домашний каталог: %w", err)
	}
	return filepath.Join(home, DefaultName), nil
}

// ResolveRoot turns the -workdir flag or AGENT_WORK_DIR into an absolute
// path. An empty value means ~/goagent-work.
func ResolveRoot(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = strings.TrimSpace(os.Getenv("AGENT_WORK_DIR"))
	}
	if value == "" {
		return DefaultRoot()
	}
	expanded, err := ExpandHome(value)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("workdir %q: %w", value, err)
	}
	return filepath.Clean(abs), nil
}

// ExpandHome expands a leading "~" or "~/".
func ExpandHome(value string) (string, error) {
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("домашний каталог: %w", err)
		}
		if value == "~" {
			return home, nil
		}
		return filepath.Join(home, value[2:]), nil
	}
	return value, nil
}

// EnsureLayout creates the root, notes/ and reports/.
func EnsureLayout(root string) error {
	for _, dir := range []string{root, filepath.Join(root, "notes"), filepath.Join(root, "reports")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("workdir %s: %w", dir, err)
		}
	}
	return nil
}

// Resolve joins rel to root and proves that the result stays inside root.
// rel must be relative; empty means the root itself. Existing symlinks are
// evaluated, so a link inside the root cannot point outside it.
func Resolve(root, rel string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("корень %q: %w", root, err)
	}
	rel = strings.TrimSpace(rel)
	if strings.HasPrefix(rel, "~") {
		return "", fmt.Errorf("путь %q начинается с ~; укажите путь относительно рабочего каталога", rel)
	}
	if rel == "" || rel == "." {
		return rootAbs, nil
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("путь %q должен быть относительным", rel)
	}
	full := filepath.Join(rootAbs, rel)
	inside, err := filepath.Rel(rootAbs, full)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("путь %q выходит за рабочий каталог", rel)
	}

	rootEval := evalPath(rootAbs)
	fullEval := evalPath(full)
	insideEval, err := filepath.Rel(rootEval, fullEval)
	if err != nil || insideEval == ".." || strings.HasPrefix(insideEval, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("путь %q выходит за рабочий каталог через symlink", rel)
	}
	return full, nil
}

// Rel returns the slash-separated path of target relative to root.
func Rel(root, target string) (string, error) {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("путь %q вне рабочего каталога", target)
	}
	return filepath.ToSlash(rel), nil
}

// evalPath resolves symlinks for the existing part of a path and appends the
// non-existing tail. This catches a symlinked parent before a new file is
// created inside it.
func evalPath(path string) string {
	current := path
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			if current == path {
				return resolved
			}
			tail, err := filepath.Rel(current, path)
			if err != nil {
				return filepath.Clean(path)
			}
			return filepath.Join(resolved, tail)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Clean(path)
		}
		current = parent
	}
}

// IsHiddenName reports dot-files and dot-directories.
func IsHiddenName(name string) bool {
	return strings.HasPrefix(name, ".") && name != "." && name != ".."
}

// IsSecretName matches files that must never be read by the model.
func IsSecretName(name string) bool {
	base := strings.ToLower(filepath.Base(name))
	patterns := []string{
		".env", ".env.*",
		"*.pem", "*.key", "*.p12", "*.pfx", "*.kdbx",
		"id_rsa*", "id_ed25519*", "id_ecdsa*",
		"credentials*", "secrets*",
	}
	for _, pattern := range patterns {
		if matched, _ := filepath.Match(pattern, base); matched {
			return true
		}
	}
	return false
}

// HasSecretComponent checks every path component, so reports/.env is denied
// as well as a bare .env.
func HasSecretComponent(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part != "" && IsSecretName(part) {
			return true
		}
	}
	return false
}

// SkipDir lists directories that are noise or dependency caches. Hidden
// directories are skipped by the caller as well.
func SkipDir(name string) bool {
	switch strings.ToLower(name) {
	case "node_modules", "vendor", "bin", "dist", "build", "target",
		"__pycache__", ".cache", ".git", ".venv", "venv":
		return true
	default:
		return false
	}
}

// IsTextName reports whether a file name is likely to contain text. The
// extension list is deliberately broad: the model's workdir may contain notes,
// code and reports.
func IsTextName(name string) bool {
	base := strings.ToLower(filepath.Base(name))
	switch base {
	case "makefile", "dockerfile", "go.mod", "go.sum", "readme", "license", "copying":
		return true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".markdown", ".txt", ".rst", ".tex",
		".go", ".py", ".js", ".ts", ".tsx", ".jsx", ".rb", ".php", ".pl", ".lua", ".r",
		".java", ".kt", ".kts", ".swift", ".c", ".h", ".cc", ".cpp", ".hpp", ".rs",
		".sh", ".bash", ".zsh", ".fish", ".ps1",
		".json", ".jsonl", ".ndjson", ".yaml", ".yml", ".toml", ".ini", ".cfg", ".conf",
		".xml", ".html", ".htm", ".css", ".scss", ".sass", ".less", ".sql",
		".csv", ".tsv", ".log", ".mod", ".sum":
		return true
	default:
		return false
	}
}

// IsTextFile rejects binary files by looking for a NUL byte in the first 8 KiB.
func IsTextFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return false
	}
	buf = buf[:n]
	for _, b := range buf {
		if b == 0 {
			return false
		}
	}
	return utf8.Valid(buf) || n == len(buf)
}
