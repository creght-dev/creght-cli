package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"bysir/creght-cli/internal/creght"

	"github.com/fsnotify/fsnotify"
	"github.com/gorilla/websocket"
)

type DevSyncer struct {
	*Syncer

	apiHost string
	webHost string
	token   string

	remoteApplyMu sync.Mutex
	remoteApply   map[string]time.Time
}

type wsMessage struct {
	Type     string          `json:"type"`
	Topics   []string        `json:"topics,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
	ClientID string          `json:"client_id,omitempty"`
}

type wsFileUpdateData struct {
	File struct {
		ID      string `json:"id"`
		Path    string `json:"path"`
		Content string `json:"content"`
	} `json:"file"`
}

func runDev(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("dev", flag.ContinueOnError)
	webHost := fs.String("web", "", "Cregh web host")
	siteID := fs.String("site_id", "", "project_id/site_id")
	dir := fs.String("dir", ".", "local Cregh project directory")
	noPreview := fs.Bool("no-preview", false, "do not start a local Vite preview")
	previewHost := fs.String("preview-host", "localhost", "local Vite preview host")
	previewPort := fs.Int("preview-port", 5173, "local Vite preview preferred port")
	err := fs.Parse(args)
	if err != nil {
		return err
	}

	projectID, realSiteID, err := parseSiteRef(*siteID)
	if err != nil {
		return err
	}

	client, cfg, err := clientFromConfig()
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return fmt.Errorf("creght dev requires login; run creght login first")
	}

	syncer, err := NewSyncer(client, projectID, realSiteID, *dir)
	if err != nil {
		return err
	}

	resolvedWebHost := strings.TrimSpace(*webHost)
	if resolvedWebHost == "" {
		resolvedWebHost = defaultWebHost(cfg.APIHost)
	}

	dev := &DevSyncer{
		Syncer:      syncer,
		apiHost:     strings.TrimRight(cfg.APIHost, "/"),
		webHost:     strings.TrimRight(resolvedWebHost, "/"),
		token:       strings.TrimSpace(cfg.Token),
		remoteApply: map[string]time.Time{},
	}

	editorURL := fmt.Sprintf("%s/editor/project/%s/site/%s", dev.webHost, url.PathEscape(projectID), url.PathEscape(realSiteID))
	fmt.Printf("Web editor:  %s\n", editorURL)
	fmt.Printf("Local files: %s\n", dev.dir)
	fmt.Printf("Remote API:  %s\n", dev.apiHost)
	fmt.Println("Sync mode:   bidirectional, last write wins")

	if !*noPreview {
		preview, err := startVitePreview(ctx, vitePreviewOptions{
			Dir:       dev.dir,
			APIHost:   dev.apiHost,
			ProjectID: projectID,
			Token:     dev.token,
			Host:      *previewHost,
			Port:      *previewPort,
			ImportMap: systemDevImportMap(ctx, client),
		})
		if err != nil {
			return err
		}
		defer preview.Stop()
		fmt.Printf("Local Vite:  started (preferred %s; use the Vite Local URL above)\n", preview.URL)
	}

	return dev.Run(ctx)
}

type vitePreviewOptions struct {
	Dir       string
	APIHost   string
	ProjectID string
	Token     string
	Host      string
	Port      int
	ImportMap map[string]string
}

type vitePreviewProcess struct {
	cmd       *exec.Cmd
	configDir string
	URL       string
	done      chan error
}

func startVitePreview(ctx context.Context, opts vitePreviewOptions) (*vitePreviewProcess, error) {
	if opts.Port == 0 {
		opts.Port = 5173
	}
	if strings.TrimSpace(opts.Host) == "" {
		opts.Host = "localhost"
	}

	pluginSource, err := findVitePluginSource(opts.Dir)
	if err != nil {
		return nil, err
	}

	stateRoot := filepath.Join(opts.Dir, ".creght")
	if err := os.MkdirAll(stateRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create creght state dir: %w", err)
	}
	configDir, err := os.MkdirTemp(stateRoot, "vite-*")
	if err != nil {
		return nil, fmt.Errorf("create vite config dir: %w", err)
	}
	pluginPath, err := stageVitePlugin(pluginSource, configDir)
	if err != nil {
		_ = os.RemoveAll(configDir)
		return nil, err
	}

	configPath := filepath.Join(configDir, "vite.config.mjs")
	config := fmt.Sprintf(`import { defineConfig } from 'vite'
import creght from %q

export default defineConfig({
  root: %q,
  cacheDir: %q,
  server: {
    host: %q,
    port: %d,
  },
  plugins: [
    creght({
      root: %q,
      apiHost: %q,
      projectId: %q,
      token: %q,
      importMap: %s,
    }),
  ],
})
`, pathToFileURL(pluginPath), filepath.ToSlash(opts.Dir), filepath.ToSlash(filepath.Join(configDir, ".vite")), opts.Host, opts.Port, filepath.ToSlash(opts.Dir), opts.APIHost, opts.ProjectID, opts.Token, jsonLiteral(opts.ImportMap))

	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		_ = os.RemoveAll(configDir)
		return nil, fmt.Errorf("write vite config: %w", err)
	}
	packageJSON := []byte(`{"private":true,"type":"module"}` + "\n")
	if err := os.WriteFile(filepath.Join(configDir, "package.json"), packageJSON, 0o600); err != nil {
		_ = os.RemoveAll(configDir)
		return nil, fmt.Errorf("write vite package.json: %w", err)
	}

	cmdName, args, err := viteCommand(opts.Dir, configDir, configPath, opts.Host, opts.Port)
	if err != nil {
		_ = os.RemoveAll(configDir)
		return nil, err
	}

	cmd := exec.CommandContext(ctx, cmdName, args...)
	cmd.Dir = opts.Dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(configDir)
		return nil, fmt.Errorf("start vite preview: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		_ = os.RemoveAll(configDir)
		if err != nil {
			return nil, fmt.Errorf("vite preview exited: %w", err)
		}
		return nil, fmt.Errorf("vite preview exited")
	case <-time.After(2 * time.Second):
	}

	return &vitePreviewProcess{
		cmd:       cmd,
		configDir: configDir,
		URL:       vitePreviewURL(opts.Host, opts.Port),
		done:      done,
	}, nil
}

func (p *vitePreviewProcess) Stop() {
	if p == nil {
		return
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		if p.done != nil {
			<-p.done
		}
	}
	if p.configDir != "" {
		_ = os.RemoveAll(p.configDir)
	}
}

func systemDevImportMap(ctx context.Context, client *creght.Client) map[string]string {
	info, err := client.GetSystemInfo(ctx)
	if err != nil {
		fmt.Printf("get system import map: %v\n", err)
		return nil
	}

	imports := map[string]string{}
	for key, value := range info.RenderConfig.ImportMap {
		imports[key] = value
	}
	for key, value := range info.RenderConfig.DevImportMap {
		imports[key] = value
	}
	if len(imports) == 0 {
		return nil
	}
	return imports
}

func jsonLiteral(value any) string {
	body, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(body)
}

func viteCommand(projectDir string, configDir string, configPath string, host string, port int) (string, []string, error) {
	if bin := localViteBin(projectDir); bin != "" {
		return bin, []string{"--host", host, "--port", fmt.Sprintf("%d", port), "--config", configPath}, nil
	}

	if bin := localViteBin(configDir); bin != "" {
		return bin, []string{"--host", host, "--port", fmt.Sprintf("%d", port), "--config", configPath}, nil
	}

	install := exec.Command("npm", "install", "--silent", "--no-audit", "--no-fund", "--prefix", configDir, "vite@latest", "esbuild@latest")
	install.Dir = configDir
	install.Stdout = os.Stdout
	install.Stderr = os.Stderr
	if err := install.Run(); err != nil {
		return "", nil, fmt.Errorf("install vite preview dependency: %w", err)
	}
	if bin := localViteBin(configDir); bin != "" {
		return bin, []string{"--host", host, "--port", fmt.Sprintf("%d", port), "--config", configPath}, nil
	}

	return "", nil, fmt.Errorf("vite binary not found after install")
}

func localViteBin(projectDir string) string {
	name := "vite"
	if runtime.GOOS == "windows" {
		name = "vite.cmd"
	}
	bin := filepath.Join(projectDir, "node_modules", ".bin", name)
	if _, err := os.Stat(bin); err == nil {
		return bin
	}
	return ""
}

func vitePreviewURL(host string, port int) string {
	if host == "0.0.0.0" || host == "::" || strings.TrimSpace(host) == "" {
		host = "localhost"
	}
	return fmt.Sprintf("http://%s:%d", host, port)
}

func findVitePluginSource(projectDir string) (string, error) {
	candidates := []string{}
	candidates = append(candidates, filepath.Join(projectDir, "node_modules", "creght-cli", "vite", "index.js"))
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, "vite", "index.js"),
			filepath.Join(exeDir, "..", "vite", "index.js"),
			filepath.Join(exeDir, "..", "..", "vite", "index.js"),
		)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "vite", "index.js"))
	}

	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("creght vite plugin not found; reinstall creght-cli and try again")
}

func stageVitePlugin(pluginSource string, configDir string) (string, error) {
	pluginPath := filepath.Join(configDir, "creght-vite-plugin.mjs")
	if err := copyFile(pluginSource, pluginPath, 0o600); err != nil {
		return "", err
	}

	importMapSource := filepath.Join(filepath.Dir(pluginSource), "import-map.js")
	importMapPath := filepath.Join(configDir, "import-map.js")
	if err := copyFile(importMapSource, importMapPath, 0o600); err != nil {
		return "", err
	}

	return pluginPath, nil
}

func pathToFileURL(file string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(file)}
	return u.String()
}

func copyFile(src string, dst string, mode os.FileMode) error {
	body, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read vite plugin: %w", err)
	}
	if err := os.WriteFile(dst, body, mode); err != nil {
		return fmt.Errorf("write vite plugin: %w", err)
	}
	return nil
}

func (d *DevSyncer) Run(ctx context.Context) error {
	err := os.MkdirAll(d.dir, 0o755)
	if err != nil {
		return fmt.Errorf("create local dir: %w", err)
	}

	hasLocalFiles, err := hasSyncableLocalFiles(d.dir)
	if err != nil {
		return err
	}
	if hasLocalFiles {
		if err := d.Push(ctx); err != nil {
			return err
		}
	} else {
		if err := d.refreshAndMirrorRemote(ctx); err != nil {
			return err
		}
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create watcher: %w", err)
	}
	defer watcher.Close()

	if err := d.watchDirs(watcher); err != nil {
		return err
	}

	go d.runRemoteWatcher(ctx)
	fmt.Printf("Watching local changes and remote editor changes for %s/%s\n", d.projectID, d.siteID)

	debounce := map[string]*time.Timer{}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-watcher.Errors:
			if err != nil {
				fmt.Printf("watch error: %v\n", err)
			}
		case event := <-watcher.Events:
			if shouldSkipLocalPath(d.dir, event.Name) {
				continue
			}
			if d.consumeRemoteApply(event.Name) {
				continue
			}
			if event.Op&fsnotify.Create != 0 {
				info, statErr := os.Stat(event.Name)
				if statErr == nil && info.IsDir() {
					_ = filepath.WalkDir(event.Name, func(path string, entry os.DirEntry, err error) error {
						if err != nil || !entry.IsDir() {
							return nil
						}
						if shouldSkipLocalPath(d.dir, path) {
							return filepath.SkipDir
						}
						return watcher.Add(path)
					})
				}
			}

			key := event.Name
			if timer, ok := debounce[key]; ok {
				timer.Stop()
			}
			debounce[key] = time.AfterFunc(400*time.Millisecond, func() {
				delete(debounce, key)
				if err := d.handleEvent(context.Background(), event); err != nil {
					fmt.Printf("sync %s: %v\n", event.Name, err)
				}
			})
		}
	}
}

func (d *DevSyncer) runRemoteWatcher(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		err := d.connectRemoteWatcher(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			fmt.Printf("remote watcher: %v\n", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

func (d *DevSyncer) connectRemoteWatcher(ctx context.Context) error {
	wsURL, err := d.wsURL()
	if err != nil {
		return err
	}

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+d.token)
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		return fmt.Errorf("connect %s: %w", wsURL, err)
	}
	defer conn.Close()

	subscribe := map[string]any{
		"type": "subscribe",
		"data": map[string]any{
			"topics": []string{
				fmt.Sprintf("site/%s/file_list", d.siteID),
				fmt.Sprintf("site/%s/file", d.siteID),
			},
		},
	}
	if err := conn.WriteJSON(subscribe); err != nil {
		return fmt.Errorf("subscribe remote changes: %w", err)
	}

	for {
		var msg wsMessage
		if err := conn.ReadJSON(&msg); err != nil {
			return err
		}
		if msg.ClientID != "" && msg.ClientID == d.clientID {
			continue
		}

		switch msg.Type {
		case "file_update":
			if err := d.applyRemoteFileUpdate(ctx, msg.Data); err != nil {
				fmt.Printf("apply remote file update: %v\n", err)
			}
		case "file_list_update":
			if err := d.refreshAndMirrorRemote(ctx); err != nil {
				fmt.Printf("refresh remote files: %v\n", err)
			}
		case "ping":
			_ = conn.WriteJSON(map[string]string{"type": "pong"})
		}
	}
}

func (d *DevSyncer) wsURL() (string, error) {
	u, err := url.Parse(d.apiHost)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("unsupported api scheme: %s", u.Scheme)
	}
	u.Path = "/api/u/ws"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func hasSyncableLocalFiles(root string) (bool, error) {
	found := false
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if shouldSkipLocalPath(root, path) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read local file: %w", err)
		}
		if isUTF8FileBody(body) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found, err
}

func (d *DevSyncer) applyRemoteFileUpdate(ctx context.Context, raw json.RawMessage) error {
	var data wsFileUpdateData
	if err := json.Unmarshal(raw, &data); err != nil {
		return fmt.Errorf("parse file_update: %w", err)
	}
	if data.File.Path == "" {
		return fmt.Errorf("file_update path is empty")
	}

	hash, err := qetagHash([]byte(data.File.Content))
	if err != nil {
		return err
	}

	d.mu.Lock()
	d.remoteByPath[data.File.Path] = creght.File{
		ID:   data.File.ID,
		Path: data.File.Path,
		Body: data.File.Content,
		Hash: hash,
	}
	d.mu.Unlock()

	return d.writeRemoteFileToLocal(data.File.Path, data.File.Content)
}

func (d *DevSyncer) refreshAndMirrorRemote(ctx context.Context) error {
	if err := d.refreshRemote(ctx); err != nil {
		return err
	}
	return d.mirrorRemoteToLocal(ctx)
}

func (d *DevSyncer) mirrorRemoteToLocal(ctx context.Context) error {
	files, err := d.client.GetFileList(ctx, d.projectID, d.siteID)
	if err != nil {
		return err
	}

	remotePaths := map[string]struct{}{}
	for _, file := range files.List {
		if file.IsDir {
			continue
		}
		remotePaths[file.Path] = struct{}{}
		if err := d.writeRemoteFileToLocal(file.Path, file.Body); err != nil {
			return err
		}
	}

	return filepath.WalkDir(d.dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if shouldSkipLocalPath(d.dir, path) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		remotePath, err := localPathToRemote(d.dir, path)
		if err != nil {
			return err
		}
		if _, ok := remotePaths[remotePath]; ok {
			return nil
		}
		d.markRemoteApply(path)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove local deleted remote file %s: %w", remotePath, err)
		}
		return nil
	})
}

func (d *DevSyncer) writeRemoteFileToLocal(remotePath string, body string) error {
	localPath, err := remotePathToLocal(d.dir, remotePath)
	if err != nil {
		return err
	}
	current, err := os.ReadFile(localPath)
	if err == nil && string(current) == body {
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read local file before remote write %s: %w", remotePath, err)
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("create local parent: %w", err)
	}

	d.markRemoteApply(localPath)
	if err := os.WriteFile(localPath, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write remote file locally %s: %w", remotePath, err)
	}
	return nil
}

func (d *DevSyncer) markRemoteApply(localPath string) {
	d.remoteApplyMu.Lock()
	defer d.remoteApplyMu.Unlock()
	d.remoteApply[localPath] = time.Now()
}

func (d *DevSyncer) consumeRemoteApply(localPath string) bool {
	d.remoteApplyMu.Lock()
	defer d.remoteApplyMu.Unlock()
	t, ok := d.remoteApply[localPath]
	if !ok {
		return false
	}
	delete(d.remoteApply, localPath)
	return time.Since(t) < 5*time.Second
}
