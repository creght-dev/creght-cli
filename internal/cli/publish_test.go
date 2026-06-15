package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReleaseTag(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
		wantErr bool
	}{
		{
			name:    "plain semver",
			version: "0.1.0",
			want:    "v0.1.0",
		},
		{
			name:    "already prefixed",
			version: "v0.1.0",
			want:    "v0.1.0",
		},
		{
			name:    "trim spaces",
			version: " 0.1.0 ",
			want:    "v0.1.0",
		},
		{
			name:    "dev cannot publish",
			version: "dev",
			wantErr: true,
		},
		{
			name:    "empty cannot publish",
			version: "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := releaseTag(tt.version)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("releaseTag(%q) = %q, want %q", tt.version, got, tt.want)
			}
		})
	}
}

func TestRunLogoutDeletesConfig(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	cfgPath, err := configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}
	err = os.MkdirAll(filepath.Dir(cfgPath), 0o755)
	if err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	err = os.WriteFile(cfgPath, []byte(`{"api_host":"http://localhost:9432","token":"test"}`), 0o600)
	if err != nil {
		t.Fatalf("write config: %v", err)
	}

	err = runLogout(nil)
	if err != nil {
		t.Fatalf("runLogout: %v", err)
	}

	_, err = os.Stat(cfgPath)
	if !os.IsNotExist(err) {
		t.Fatalf("config still exists or stat failed: %v", err)
	}
}

func TestRunLogoutMissingConfigSucceeds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	err := runLogout(nil)
	if err != nil {
		t.Fatalf("runLogout: %v", err)
	}
}
