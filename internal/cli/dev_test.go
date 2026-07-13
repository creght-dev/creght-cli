package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDevSyncerWsURL(t *testing.T) {
	d := &DevSyncer{apiHost: "https://creght.cn"}
	got, err := d.wsURL()
	if err != nil {
		t.Fatalf("wsURL: %v", err)
	}
	if got != "wss://creght.cn/api/u/ws" {
		t.Fatalf("wsURL = %q", got)
	}

	d.apiHost = "http://localhost:8433"
	got, err = d.wsURL()
	if err != nil {
		t.Fatalf("wsURL localhost: %v", err)
	}
	if got != "ws://localhost:8433/api/u/ws" {
		t.Fatalf("wsURL localhost = %q", got)
	}
}

func TestHasSyncableLocalFiles(t *testing.T) {
	dir := t.TempDir()
	ok, err := hasSyncableLocalFiles(dir)
	if err != nil {
		t.Fatalf("empty dir: %v", err)
	}
	if ok {
		t.Fatalf("empty dir should not have syncable files")
	}

	if err := os.MkdirAll(filepath.Join(dir, "page"), 0o755); err != nil {
		t.Fatalf("mkdir page: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "page", "Index.tsx"), []byte("export default function Index() { return null }\n"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}

	ok, err = hasSyncableLocalFiles(dir)
	if err != nil {
		t.Fatalf("non-empty dir: %v", err)
	}
	if !ok {
		t.Fatalf("dir should have syncable files")
	}
}

func TestVitePreviewURL(t *testing.T) {
	if got := vitePreviewURL("localhost", 5173); got != "http://localhost:5173" {
		t.Fatalf("vitePreviewURL localhost = %q", got)
	}
	if got := vitePreviewURL("0.0.0.0", 5174); got != "http://localhost:5174" {
		t.Fatalf("vitePreviewURL wildcard = %q", got)
	}
}

func TestLocalViteBin(t *testing.T) {
	dir := t.TempDir()
	if got := localViteBin(dir); got != "" {
		t.Fatalf("localViteBin empty = %q", got)
	}

	name := "vite"
	if runtime.GOOS == "windows" {
		name = "vite.cmd"
	}
	bin := filepath.Join(dir, "node_modules", ".bin", name)
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write bin: %v", err)
	}
	if got := localViteBin(dir); got != bin {
		t.Fatalf("localViteBin = %q, want %q", got, bin)
	}
}

func TestStageVitePluginCopiesRelativeImports(t *testing.T) {
	sourceDir := t.TempDir()
	pluginSource := filepath.Join(sourceDir, "index.js")
	if err := os.WriteFile(pluginSource, []byte("import './import-map.js'\nexport default function creght() {}\n"), 0o644); err != nil {
		t.Fatalf("write plugin source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "import-map.js"), []byte("export const ok = true\n"), 0o644); err != nil {
		t.Fatalf("write import map helper: %v", err)
	}

	configDir := t.TempDir()
	pluginPath, err := stageVitePlugin(pluginSource, configDir)
	if err != nil {
		t.Fatalf("stage vite plugin: %v", err)
	}
	if pluginPath != filepath.Join(configDir, "creght-vite-plugin.mjs") {
		t.Fatalf("pluginPath = %q", pluginPath)
	}
	if _, err := os.Stat(pluginPath); err != nil {
		t.Fatalf("stat staged plugin: %v", err)
	}
	if _, err := os.Stat(filepath.Join(configDir, "import-map.js")); err != nil {
		t.Fatalf("stat staged import-map helper: %v", err)
	}
}

func TestWriteRemoteFileToLocalSkipsSameContent(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "page", "About.tsx")
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatalf("mkdir page: %v", err)
	}
	if err := os.WriteFile(localPath, []byte("same\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	before, err := os.Stat(localPath)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}

	d := &DevSyncer{Syncer: &Syncer{dir: dir}, remoteApply: map[string]time.Time{}}
	if err := d.writeRemoteFileToLocal("/page/About.tsx", "same\n"); err != nil {
		t.Fatalf("write same remote file: %v", err)
	}
	after, err := os.Stat(localPath)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("same content should not rewrite file")
	}
	if len(d.remoteApply) != 0 {
		t.Fatalf("same content should not mark remote apply")
	}
}
