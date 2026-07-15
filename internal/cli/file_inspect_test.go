package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func numberedLines(n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("line%d", i+1)
	}
	return lines
}

func countHunkHeaders(out string) int {
	count := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "@@ -") {
			count++
		}
	}
	return count
}

func TestUnifiedLineDiffOneLineChangeInLongFilePrintsOneHunk(t *testing.T) {
	aLines := numberedLines(200)
	a := strings.Join(aLines, "\n") + "\n"
	bLines := append([]string{}, aLines...)
	bLines[99] = "CHANGED"
	b := strings.Join(bLines, "\n") + "\n"

	out := unifiedLineDiff("remote:/x", "local:/x", a, b)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 11 {
		t.Fatalf("got %d output lines, want 11 (2 headers + hunk header + 8 lines):\n%s", len(lines), out)
	}
	if lines[2] != "@@ -97,7 +97,7 @@" {
		t.Fatalf("hunk header = %q, want @@ -97,7 +97,7 @@", lines[2])
	}
	if !strings.Contains(out, "-line100\n") || !strings.Contains(out, "+CHANGED\n") {
		t.Fatalf("diff missing changed lines:\n%s", out)
	}
}

func TestUnifiedLineDiffMergesNearbyChangesIntoOneHunk(t *testing.T) {
	aLines := numberedLines(30)
	a := strings.Join(aLines, "\n") + "\n"
	bLines := append([]string{}, aLines...)
	bLines[9] = "X"
	bLines[13] = "Y"
	b := strings.Join(bLines, "\n") + "\n"

	out := unifiedLineDiff("a", "b", a, b)
	if got := countHunkHeaders(out); got != 1 {
		t.Fatalf("got %d hunks, want 1 (changes 3 unchanged lines apart merge):\n%s", got, out)
	}
}

func TestUnifiedLineDiffSeparatesFarChangesIntoTwoHunks(t *testing.T) {
	aLines := numberedLines(60)
	a := strings.Join(aLines, "\n") + "\n"
	bLines := append([]string{}, aLines...)
	bLines[9] = "X"
	bLines[49] = "Y"
	b := strings.Join(bLines, "\n") + "\n"

	out := unifiedLineDiff("a", "b", a, b)
	if got := countHunkHeaders(out); got != 2 {
		t.Fatalf("got %d hunks, want 2:\n%s", got, out)
	}
}

func TestUnifiedLineDiffChangeAtFileStart(t *testing.T) {
	aLines := numberedLines(10)
	a := strings.Join(aLines, "\n") + "\n"
	bLines := append([]string{}, aLines...)
	bLines[0] = "X"
	b := strings.Join(bLines, "\n") + "\n"

	out := unifiedLineDiff("a", "b", a, b)
	if !strings.Contains(out, "\n@@ -1,") {
		t.Fatalf("hunk should start at line 1:\n%s", out)
	}
	if countHunkHeaders(out) != 1 {
		t.Fatalf("want a single hunk:\n%s", out)
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

func TestResolveWorkspacePathFromSubdirectory(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "page")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, sub)

	for input, want := range map[string]string{
		"Index.tsx":            "/page/Index.tsx",
		"deep/Nested.tsx":      "/page/deep/Nested.tsx",
		"../talizen.config.ts": "/talizen.config.ts",
		"/Index.tsx":           "/Index.tsx",
		"/page/Index.tsx":      "/page/Index.tsx",
	} {
		got, err := resolveWorkspacePath(root, input)
		if err != nil {
			t.Fatalf("resolveWorkspacePath(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("resolveWorkspacePath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestResolveWorkspacePathOutsideRootFallsBackToRootRelative(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()
	chdir(t, elsewhere)

	got, err := resolveWorkspacePath(root, "page/Index.tsx")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/page/Index.tsx" {
		t.Fatalf("resolveWorkspacePath = %q, want root-relative fallback /page/Index.tsx", got)
	}
}

func TestResolveWorkspacePathRejectsEscapingRoot(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "page")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, sub)

	if _, err := resolveWorkspacePath(root, "../../outside.txt"); err == nil {
		t.Fatal("expected error for path escaping the workspace root")
	}
}
