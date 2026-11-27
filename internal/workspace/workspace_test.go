package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRootDefaultAndOverride(t *testing.T) {
	t.Setenv("AGENT_WORK_DIR", "")
	root, err := ResolveRoot("")
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}
	if filepath.Base(root) != DefaultName {
		t.Fatalf("ожидался %s, получено %s", DefaultName, root)
	}

	t.Setenv("AGENT_WORK_DIR", "~/custom-work")
	root, err = ResolveRoot("")
	if err != nil {
		t.Fatalf("ResolveRoot env: %v", err)
	}
	if !filepath.IsAbs(root) || filepath.Base(root) != "custom-work" {
		t.Fatalf("некорректный env-root: %s", root)
	}

	flagRoot, err := ResolveRoot("/tmp/explicit-work")
	if err != nil {
		t.Fatalf("ResolveRoot flag: %v", err)
	}
	if flagRoot != "/tmp/explicit-work" {
		t.Fatalf("флаг не победил env: %s", flagRoot)
	}
}

func TestEnsureLayoutCreatesNotesAndReports(t *testing.T) {
	root := t.TempDir()
	if err := EnsureLayout(root); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}
	for _, sub := range []string{"notes", "reports"} {
		if st, err := os.Stat(filepath.Join(root, sub)); err != nil || !st.IsDir() {
			t.Fatalf("нет каталога %s: %v", sub, err)
		}
	}
}

func TestResolveRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	if _, err := Resolve(root, "notes/a.md"); err != nil {
		t.Fatalf("обычный путь: %v", err)
	}
	for _, rel := range []string{"../escape", "/etc/passwd", "a/../../escape"} {
		if _, err := Resolve(root, rel); err == nil {
			t.Fatalf("ожидалась ошибка для %q", rel)
		}
	}
}

func TestResolveRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if _, err := Resolve(root, "link/secret.md"); err == nil {
		t.Fatal("symlink за пределы workdir не отклонён")
	}
}

func TestSecretAndHiddenNames(t *testing.T) {
	for _, name := range []string{".env", ".env.local", "key.pem", "server.key", "id_rsa", "id_ed25519.pub", "credentials.json", "secrets.yaml"} {
		if !IsSecretName(name) {
			t.Fatalf("%q должен считаться секретом", name)
		}
	}
	if IsSecretName("notes.md") {
		t.Fatal("notes.md не секрет")
	}
	if !IsHiddenName(".git") || IsHiddenName("notes") {
		t.Fatal("неверная проверка скрытых имён")
	}
	if !HasSecretComponent("reports/.env") || HasSecretComponent("reports/summary.md") {
		t.Fatal("неверная проверка компонентов пути")
	}
}

func TestTextNamesAndBinaryDetection(t *testing.T) {
	for _, name := range []string{"a.md", "main.go", "config.yaml", "data.json", "run.sh", "Makefile"} {
		if !IsTextName(name) {
			t.Fatalf("%q должен считаться текстом", name)
		}
	}
	for _, name := range []string{"photo.png", "archive.zip", "binary"} {
		if IsTextName(name) {
			t.Fatalf("%q не должен считаться текстом", name)
		}
	}
	dir := t.TempDir()
	text := filepath.Join(dir, "a.md")
	if err := os.WriteFile(text, []byte("привет"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !IsTextFile(text) {
		t.Fatal("текстовый файл отклонён")
	}
	bin := filepath.Join(dir, "a.bin")
	if err := os.WriteFile(bin, []byte{0, 1, 2}, 0o644); err != nil {
		t.Fatal(err)
	}
	if IsTextFile(bin) {
		t.Fatal("бинарный файл принят")
	}
}
