package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bysir/creght-cli/internal/creght"
)

func TestEnsurePulledAgentsFileCreatesWhenRemoteMissing(t *testing.T) {
	dir := t.TempDir()

	created, err := ensurePulledAgentsFile(dir, []creght.File{
		{Path: "/page/index.tsx", Body: "export default function Page() { return null }\n"},
	}, "project-1", "site-1", "https://creght.cn/editor/project/project-1/site/site-1")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("created = false, want true")
	}

	body, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"This is a Cregh project pulled by the Cregh CLI.",
		"https://github.com/creght/skills/blob/main/readme.md",
		"Project ID: project-1",
		"Site ID: site-1",
		"Editor URL: https://creght.cn/editor/project/project-1/site/site-1",
		"creght push --site_id=project-1/site-1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("AGENTS.md missing %q in:\n%s", want, text)
		}
	}
}

func TestEnsurePulledAgentsFileDoesNothingWhenRemoteExists(t *testing.T) {
	dir := t.TempDir()

	created, err := ensurePulledAgentsFile(dir, []creght.File{
		{Path: "/AGENTS.md", Body: "remote body\n"},
	}, "project-1", "site-1", "https://creght.cn/editor/project/project-1/site/site-1")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("created = true, want false")
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("AGENTS.md local stat err = %v, want not exist", err)
	}
}

func TestEnsurePulledAgentsFileDoesNotOverwriteLocalFile(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(localPath, []byte("local body\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	created, err := ensurePulledAgentsFile(dir, nil, "project-1", "site-1", "https://creght.cn/editor/project/project-1/site/site-1")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("created = true, want false")
	}

	body, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "local body\n" {
		t.Fatalf("local AGENTS.md was overwritten: %q", string(body))
	}
}

func TestShouldSkipLocalPath(t *testing.T) {
	root := filepath.Clean("/tmp/site")

	tests := []struct {
		name string
		path string
		want bool
	}{
		{
			name: "root is not skipped",
			path: root,
			want: false,
		},
		{
			name: "hidden file is skipped",
			path: filepath.Join(root, ".DS_Store"),
			want: true,
		},
		{
			name: "hidden nested file is skipped",
			path: filepath.Join(root, "app", ".env"),
			want: true,
		},
		{
			name: "hidden directory child is skipped",
			path: filepath.Join(root, ".git", "config"),
			want: true,
		},
		{
			name: "node modules child is skipped",
			path: filepath.Join(root, "node_modules", "pkg", "index.js"),
			want: true,
		},
		{
			name: "build output child is skipped",
			path: filepath.Join(root, "dist", "bundle.js"),
			want: true,
		},
		{
			name: "regular source file is not skipped",
			path: filepath.Join(root, "src", "index.ts"),
			want: false,
		},
		{
			name: "similar visible name is not skipped",
			path: filepath.Join(root, "src", "node_modules_backup", "index.ts"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldSkipLocalPath(root, tt.path)
			if got != tt.want {
				t.Fatalf("shouldSkipLocalPath(%q, %q) = %v, want %v", root, tt.path, got, tt.want)
			}
		})
	}
}

func TestIsUTF8FileBody(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		want bool
	}{
		{
			name: "plain text is accepted",
			body: []byte("hello\n"),
			want: true,
		},
		{
			name: "utf8 text is accepted",
			body: []byte("你好\n"),
			want: true,
		},
		{
			name: "empty file is accepted",
			body: nil,
			want: true,
		},
		{
			name: "png-like binary is skipped",
			body: []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
			want: false,
		},
		{
			name: "invalid utf8 is skipped",
			body: []byte{0xff, 0xfe, 0xfd},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isUTF8FileBody(tt.body)
			if got != tt.want {
				t.Fatalf("isUTF8FileBody(%v) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}
