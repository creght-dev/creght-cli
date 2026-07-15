package cli

import (
	"os"
	"path/filepath"
	"testing"
)

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
