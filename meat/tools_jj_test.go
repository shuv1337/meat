package meat

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireJJ(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skipf("jj not found: %v", err)
	}
}

func jjRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	requireJJ(t)
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("jj", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"JJ_CONFIG=/dev/null",
			"JJ_EMAIL=t@t",
			"JJ_USER=t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("jj %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("git", "init", "--no-colocate")
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("commit", "-m", "init")
	return dir
}

func TestToolboxGrepJJ(t *testing.T) {
	repo := jjRepo(t, map[string]string{
		"src/a.go": "package a\nfunc Foo() {}\n",
		"src/b.go": "package b\nfunc Bar() {}\n",
		"other.md": "no match here\n",
	})
	tb := &toolbox{root: repo, backend: "jj"}

	in, _ := json.Marshal(grepInput{Pattern: `func Foo`})
	out, isErr := tb.grep(context.Background(), in)
	if isErr {
		t.Fatalf("grep error: %s", out)
	}
	if !strings.Contains(out, "src/a.go") || !strings.Contains(out, "func Foo") {
		t.Errorf("grep result = %q, want src/a.go match", out)
	}
	if strings.Contains(out, "Bar") {
		t.Errorf("grep leaked unrelated match: %q", out)
	}

	// Path restriction.
	in, _ = json.Marshal(grepInput{Pattern: `func`, Path: "src"})
	out, isErr = tb.grep(context.Background(), in)
	if isErr {
		t.Fatalf("path-restricted grep error: %s", out)
	}
	if !strings.Contains(out, "src/a.go") || !strings.Contains(out, "src/b.go") {
		t.Errorf("path-restricted grep = %q, want both src files", out)
	}
	if strings.Contains(out, "other.md") {
		t.Errorf("path restriction leaked other.md: %q", out)
	}

	// No match.
	in, _ = json.Marshal(grepInput{Pattern: `this_symbol_does_not_exist_zzz`})
	out, isErr = tb.grep(context.Background(), in)
	if isErr {
		t.Fatalf("no-match grep error: %s", out)
	}
	if out != "(no matches)" {
		t.Errorf("no-match = %q, want (no matches)", out)
	}

	// Path confinement.
	bad, _ := json.Marshal(grepInput{Pattern: "x", Path: "../../../etc"})
	if _, isErr := tb.grep(context.Background(), bad); !isErr {
		t.Error("want path traversal rejected")
	}
}

func TestToolboxGrepJJSkipsBinaryAndIgnored(t *testing.T) {
	repo := jjRepo(t, map[string]string{
		"tracked.txt": "hello tracked\n",
		".gitignore":  "ignored.txt\n",
	})
	// Ignored file must not appear in the @ tree listing.
	if err := os.WriteFile(filepath.Join(repo, "ignored.txt"), []byte("hello ignored\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Binary file present in @.
	if err := os.WriteFile(filepath.Join(repo, "bin.dat"), []byte("a\x00b"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("jj", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "JJ_CONFIG=/dev/null", "JJ_EMAIL=t@t", "JJ_USER=t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("jj %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("commit", "-m", "bin and ignore")

	tb := &toolbox{root: repo, backend: "jj"}
	in, _ := json.Marshal(grepInput{Pattern: `hello`})
	out, isErr := tb.grep(context.Background(), in)
	if isErr {
		t.Fatalf("grep error: %s", out)
	}
	if !strings.Contains(out, "tracked.txt") {
		t.Errorf("missing tracked match: %q", out)
	}
	if strings.Contains(out, "ignored") {
		t.Errorf("ignored file was searched: %q", out)
	}

	in, _ = json.Marshal(grepInput{Pattern: `.`})
	out, isErr = tb.grep(context.Background(), in)
	if isErr {
		t.Fatalf("binary grep error: %s", out)
	}
	if strings.Contains(out, "bin.dat") {
		t.Errorf("binary file was searched: %q", out)
	}
}

func TestToolboxGrepDescriptionNamesFlavor(t *testing.T) {
	gitTB := &toolbox{root: "/repo", backend: "git"}
	jjTB := &toolbox{root: "/repo", backend: "jj"}
	var gitDesc, jjDesc string
	for _, tool := range gitTB.tools() {
		if tool.Name == "grep" {
			gitDesc = tool.Description
		}
	}
	for _, tool := range jjTB.tools() {
		if tool.Name == "grep" {
			jjDesc = tool.Description
		}
	}
	if !strings.Contains(gitDesc, "git grep") || !strings.Contains(gitDesc, "POSIX basic") {
		t.Errorf("git grep description = %q", gitDesc)
	}
	if !strings.Contains(jjDesc, "RE2") || strings.Contains(jjDesc, "git grep") {
		t.Errorf("jj grep description = %q", jjDesc)
	}
}
