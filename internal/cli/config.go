package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Config struct {
	APIHost string            `json:"api_host"`
	Token   string            `json:"token,omitempty"`
	Tokens  map[string]string `json:"tokens,omitempty"`
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
		return Config{
			APIHost: canonicalAPIHost(defaultAPIHost()),
		}, nil
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

	if apiHost, ok := envAPIHost(); ok {
		cfg.APIHost = apiHost
	}
	if cfg.APIHost == "" {
		cfg.APIHost = defaultAPIHost()
	}
	cfg.APIHost = canonicalAPIHost(cfg.APIHost)

	token := tokenForAPIHost(cfg, cfg.APIHost, legacyAPIHost)
	cfg.Token = token

	return cfg, nil
}

// saveConfig stores cfg's token under its API host, leaving every other host's
// token intact.
//
// The saved default (api_host) is deliberately not moved when the host came from
// CREGHT_API_HOST. That variable is a per-invocation override, so
// `CREGHT_API_HOST=https://creght.com creght login` should add a token for
// creght.com and nothing more — a later bare `creght project list` must still
// talk to whatever default the user chose. Use `creght config set api_host=...`
// to move the default on purpose.
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

	if _, fromEnv := envAPIHost(); !fromEnv {
		existing.APIHost = cfg.APIHost
	}
	if existing.APIHost == "" {
		// First write on this machine while the env var is set: record the
		// built-in default rather than the override, so the file never picks up
		// a host the user only meant for one command.
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

// deleteConfig forgets the token for the API host in play, keeping every other
// host's token. The file itself is removed only once no token is left.
//
// The saved default stays where it is: logging out of a host named by
// CREGHT_API_HOST must not repoint the default at that host — and, once its
// token is gone, must not repoint it at some arbitrary other host either. The
// default moves only when it is itself the host being logged out of, and then to
// the lowest-sorted remaining host so the result does not depend on Go's
// randomized map iteration order.
func deleteConfig() error {
	cfg, err := loadRawConfig()
	if err != nil {
		return err
	}

	path, err := configPath()
	if err != nil {
		return err
	}

	if len(cfg.Tokens) > 0 {
		apiHost := strings.TrimSpace(cfg.APIHost)
		if envHost, ok := envAPIHost(); ok {
			apiHost = envHost
		}
		apiHost = canonicalAPIHost(apiHost)
		delete(cfg.Tokens, apiHost)

		if len(cfg.Tokens) > 0 {
			if canonicalAPIHost(cfg.APIHost) == apiHost {
				cfg.APIHost = lowestAPIHost(cfg.Tokens)
			}
			cfg.APIHost = canonicalAPIHost(cfg.APIHost)
			cfg.Token = cfg.Tokens[cfg.APIHost]
			return writeConfig(path, cfg)
		}
	}

	err = os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete config: %w", err)
	}

	return nil
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

// lowestAPIHost picks a saved host deterministically, so which login becomes the
// new default never depends on map iteration order.
func lowestAPIHost(tokens map[string]string) string {
	hosts := make([]string, 0, len(tokens))
	for host := range tokens {
		hosts = append(hosts, host)
	}
	if len(hosts) == 0 {
		return ""
	}
	sort.Strings(hosts)

	return hosts[0]
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
