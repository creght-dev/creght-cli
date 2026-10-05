package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/creght-dev/creght-cli/pkg/sitesync"
)

// syncClientFromConfig is clientFromConfig for the commands that run through
// pkg/sitesync (pull, push, resolve, version list): the same host and token,
// with the plan and progress lines going to stdout as before.
func syncClientFromConfig() (*sitesync.Client, Config, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, Config{}, err
	}
	token := cfg.Token
	client, err := sitesync.New(sitesync.Options{
		Host:  cfg.APIHost,
		Token: func(context.Context) (string, error) { return token, nil },
		Log:   os.Stdout,
	})
	if err != nil {
		return nil, Config{}, err
	}
	return client, cfg, nil
}

// withAuthHint adds the line saying where the rejected token came from, which
// the package cannot know; see authHint.
func withAuthHint(err error, cfg Config) error {
	if err != nil && errors.Is(err, sitesync.ErrUnauthorized) {
		return fmt.Errorf("%w\n%s", err, authHint(cfg))
	}
	return err
}
