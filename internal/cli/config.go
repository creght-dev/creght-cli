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

func saveConfig(cfg Config) error {
	cfg.APIHost = canonicalAPIHost(cfg.APIHost)
	if cfg.APIHost == "" {
		cfg.APIHost = defaultAPIHost()
	}
	cfg.APIHost = canonicalAPIHost(cfg.APIHost)

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

	existing.APIHost = cfg.APIHost
	existing.Token = existing.Tokens[cfg.APIHost]
	cfg = existing

	path, err := configPath()
	if err != nil {
		return err
	}

	return writeConfig(path, cfg)
}

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
			cfg.APIHost = apiHost
			cfg.Token = cfg.Tokens[apiHost]
			if cfg.Token == "" {
				for host, token := range cfg.Tokens {
					cfg.APIHost = host
					cfg.Token = token
					break
				}
			}
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
