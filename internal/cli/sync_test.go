package cli

import (
	"os"
	"path/filepath"
	"testing"

	"bysir/creght-cli/internal/creght"
)

func TestCollectLocalSnapshotActions(t *testing.T) {
	dir := t.TempDir()
	unchangedBody := []byte("same\n")
	changedBody := []byte("new\n")

	if err := os.WriteFile(filepath.Join(dir, "same.txt"), unchangedBody, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "changed.txt"), changedBody, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "created.txt"), []byte("created\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	unchangedHash, err := qetagHash(unchangedBody)
	if err != nil {
		t.Fatal(err)
	}

	s := &Syncer{
		dir: dir,
		remoteByPath: map[string]creght.File{
			"/same.txt": {
				ID:   "same-id",
				Path: "/same.txt",
				Hash: unchangedHash,
			},
			"/changed.txt": {
				ID:   "changed-id",
				Path: "/changed.txt",
				Hash: "old-hash",
			},
			"/deleted.txt": {
				ID:   "deleted-id",
				Path: "/deleted.txt",
				Hash: "deleted-hash",
			},
			"/readonly.txt": {
				ID:       "readonly-id",
				Path:     "/readonly.txt",
				Hash:     "readonly-hash",
				Readonly: true,
			},
		},
	}

	actions, err := s.collectLocalSnapshotActions()
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}
	for _, action := range actions {
		got[action.remotePath] = action.action.Action
	}

	want := map[string]string{
		"/changed.txt": "file_update",
		"/created.txt": "file_create",
		"/deleted.txt": "file_delete",
	}
	if len(got) != len(want) {
		t.Fatalf("got actions %v, want %v", got, want)
	}
	for path, action := range want {
		if got[path] != action {
			t.Fatalf("got action for %s = %q, want %q; all actions: %v", path, got[path], action, got)
		}
	}
}
