package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	APIHost string            `json:"api_host"`
	Token   string            `json:"token,omitempty"`
	Tokens  map[string]string `json:"tokens,omitempty"`
	// TokenSource says where Token came from. Set by loadConfig, never saved.
	TokenSource tokenSource `json:"-"`
}

func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("get user config dir: %w", err)
	}

	return filepath.Join(dir, "creght", "config.json"), nil
}

func loadConfig() (Config, error) {
	path, err := configPath()
	if err != nil {
		return Config{}, err
	}

	bs, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// No saved default yet, but the environment or the surrounding
		// workspace may still name a host.
		return applyEnvToken(Config{
			APIHost: resolveAPIHost("").Host,
		}), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	err = json.Unmarshal(bs, &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	legacyAPIHost := strings.TrimSpace(cfg.APIHost)
	if legacyAPIHost == "" {
		legacyAPIHost = defaultAPIHost()
	}

	cfg.APIHost = resolveAPIHost(cfg.APIHost).Host

	token := tokenForAPIHost(cfg, cfg.APIHost, legacyAPIHost)
	cfg.Token = token
	if token != "" {
		cfg.TokenSource = tokenSourceSaved
	}

	return applyEnvToken(cfg), nil
}

// saveConfig stores cfg's token under its API host, leaving every other host's
// token intact.
//
// The saved default (api_host) is deliberately not moved when the host was
// discovered rather than chosen — see apiHostSource.implicit. Both
// CREGHT_API_HOST and a workspace's recorded host are scoped, so
// `CREGHT_API_HOST=https://creght.com creght login`, or a login run inside a
// workspace pulled from another deployment, should add that host's token and
// nothing more: a later bare `creght project list` elsewhere must still talk to
// whatever default the user chose. Use `creght config set api_host=...` to move
// the default on purpose.
func saveConfig(cfg Config) error {
	cfg.APIHost = canonicalAPIHost(cfg.APIHost)
	if cfg.APIHost == "" {
		cfg.APIHost = canonicalAPIHost(defaultAPIHost())
	}

	existing, err := loadRawConfig()
	if err != nil {
		return err
	}
	if existing.Tokens == nil {
		existing.Tokens = map[string]string{}
	}
	for apiHost, token := range cfg.Tokens {
		apiHost = canonicalAPIHost(apiHost)
		token = strings.TrimSpace(token)
		if apiHost == "" || token == "" {
			continue
		}
		existing.Tokens[apiHost] = token
	}
	if token := strings.TrimSpace(cfg.Token); token != "" {
		existing.Tokens[cfg.APIHost] = token
	}

	if !resolveAPIHost(existing.APIHost).Source.implicit() {
		existing.APIHost = cfg.APIHost
	}
	if existing.APIHost == "" {
		// First write on this machine while an implicit host is in play: record
		// the built-in default rather than the discovered host, so the file
		// never picks up a host meant for one command or one directory.
		existing.APIHost = canonicalAPIHost(defaultAPIHostValue)
	}
	existing.Token = existing.Tokens[existing.APIHost]
	cfg = existing

	path, err := configPath()
	if err != nil {
		return err
	}

	return writeConfig(path, cfg)
}

// deleteConfig forgets the token saved for apiHost — the host logout just
// revoked on, so the two always agree — keeping every other host's token.
//
// The saved default never moves. It used to slide to the lowest-sorted host
// still logged in when the default itself was logged out of, which silently
// pointed every later bare command at another deployment (logging out of
// creght.cn made creght.com the default). A default with no token left just
// says "run creght login" on the next command, which is the honest outcome.
// The file is removed only once it holds nothing worth keeping: no tokens and
// no default other than the built-in.
func deleteConfig(apiHost string) error {
	cfg, err := loadRawConfig()
	if err != nil {
		return err
	}

	path, err := configPath()
	if err != nil {
		return err
	}

	delete(cfg.Tokens, canonicalAPIHost(apiHost))
	cfg.Token = cfg.Tokens[cfg.APIHost]

	if len(cfg.Tokens) == 0 && (cfg.APIHost == "" || cfg.APIHost == canonicalAPIHost(defaultAPIHostValue)) {
		err = os.Remove(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("delete config: %w", err)
		}
		return nil
	}

	return writeConfig(path, cfg)
}

func loadRawConfig() (Config, error) {
	path, err := configPath()
	if err != nil {
		return Config{}, err
	}

	bs, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(bs, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	cfg.APIHost = canonicalAPIHost(cfg.APIHost)
	if cfg.Tokens == nil {
		cfg.Tokens = map[string]string{}
	}
	tokens := map[string]string{}
	for apiHost, token := range cfg.Tokens {
		canonical := canonicalAPIHost(apiHost)
		token = strings.TrimSpace(token)
		if canonical != "" && token != "" {
			tokens[canonical] = token
		}
	}
	cfg.Tokens = tokens
	if cfg.APIHost != "" && strings.TrimSpace(cfg.Token) != "" {
		cfg.Tokens[cfg.APIHost] = strings.TrimSpace(cfg.Token)
	}

	return cfg, nil
}

func writeConfig(path string, cfg Config) error {
	err := os.MkdirAll(filepath.Dir(path), 0o755)
	if err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	bs, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	err = os.WriteFile(path, bs, 0o600)
	if err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	return nil
}

func tokenForAPIHost(cfg Config, apiHost string, legacyAPIHost string) string {
	apiHost = canonicalAPIHost(apiHost)
	for host, token := range cfg.Tokens {
		if canonicalAPIHost(host) == apiHost {
			return strings.TrimSpace(token)
		}
	}
	if canonicalAPIHost(legacyAPIHost) == apiHost {
		return strings.TrimSpace(cfg.Token)
	}

	return ""
}

func canonicalAPIHost(apiHost string) string {
	apiHost = strings.TrimRight(strings.TrimSpace(apiHost), "/")
	if apiHost == "" {
		return ""
	}

	u, err := url.Parse(apiHost)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return apiHost
	}

	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""

	return strings.TrimRight(u.String(), "/")
}
