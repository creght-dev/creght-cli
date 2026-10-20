package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/creght-dev/creght-cli/internal/creght"
)

// tokenEnvVar names the environment variable that hands the CLI a token.
//
// It exists for a host program that holds its own Creght OAuth access
// token and runs creght on the user's behalf: the user should not have to log
// in to the CLI a second time. The token belongs to that program, so the CLI
// only borrows it — it never saves, refreshes or revokes it.
const tokenEnvVar = "CREGHT_TOKEN"

type tokenSource int

const (
	tokenSourceNone tokenSource = iota
	tokenSourceSaved
	tokenSourceEnv
	// tokenSourceCommand: CREGHT_TOKEN_COMMAND supplies the token, fetched on
	// the first request; Config.Token stays empty.
	tokenSourceCommand
)

func envToken() (string, bool) {
	v := strings.TrimSpace(os.Getenv(tokenEnvVar))
	return v, v != ""
}

// applyEnvToken lets CREGHT_TOKEN_COMMAND, then CREGHT_TOKEN, outrank whatever
// config.json holds for the host. It replaces only the token in use: Tokens is
// left alone, so nothing that writes the config back can save the borrowed
// token.
func applyEnvToken(cfg Config) Config {
	if _, ok := envTokenCommand(); ok {
		cfg.Token = ""
		cfg.TokenSource = tokenSourceCommand
	} else if token, ok := envToken(); ok {
		cfg.Token = token
		cfg.TokenSource = tokenSourceEnv
	}

	return cfg
}

// describeToken names where the token in use came from, for `creght -h`,
// `creght whoami` and `creght config get`.
func describeToken(cfg Config) string {
	switch cfg.TokenSource {
	case tokenSourceCommand:
		return tokenCommandEnvVar + " (" + strings.TrimSpace(os.Getenv(tokenCommandEnvVar)) + " --host <host>)"
	case tokenSourceEnv:
		return tokenEnvVar + " environment variable"
	case tokenSourceSaved:
		return "saved login (creght login)"
	default:
		return "none (run creght login)"
	}
}

// authHint is the line added to a 401, telling the reader what fixes it. The
// two sources need opposite advice: a saved login is renewed with creght login,
// while a borrowed token can only be renewed by whoever set CREGHT_TOKEN —
// sending them to creght login would save a token the variable then overrides.
func authHint(cfg Config) string {
	host := canonicalAPIHost(cfg.APIHost)
	switch cfg.TokenSource {
	case tokenSourceCommand:
		return fmt.Sprintf("The token comes from %s and %s did not accept it. creght does not refresh it; "+
			"the program behind %s has to hand out a valid token for %s. Unset %s to use the saved login instead.",
			tokenCommandEnvVar, host, tokenCommandEnvVar, host, tokenCommandEnvVar)
	case tokenSourceEnv:
		return fmt.Sprintf("The token comes from %s and %s did not accept it: it has expired, was revoked, "+
			"or was issued by another Creght deployment. creght does not refresh it; the program that set "+
			"%s has to supply a new one. Unset %s to use the saved login instead.",
			tokenEnvVar, host, tokenEnvVar, tokenEnvVar)
	case tokenSourceSaved:
		return fmt.Sprintf("The saved login for %s is no longer valid; run creght login.", host)
	default:
		return fmt.Sprintf("Not logged in to %s; run creght login, or set %s.", host, tokenEnvVar)
	}
}

// refuseEnvToken stops a command that manages the saved login from running
// while CREGHT_TOKEN or CREGHT_TOKEN_COMMAND is in effect: a login would save a
// token the variable then overrides, and a logout would revoke a token the CLI
// does not own.
func refuseEnvToken(command string) error {
	name := ""
	if _, ok := envTokenCommand(); ok {
		name = tokenCommandEnvVar
	} else if _, ok := envToken(); ok {
		name = tokenEnvVar
	} else {
		return nil
	}

	return fmt.Errorf("creght %s manages the saved login, but the token in use comes from %s; "+
		"nothing was changed. Unset %s and run creght %s again",
		command, name, name, command)
}

func runWhoami(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("whoami", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "print as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("whoami does not accept positional arguments")
	}

	client, cfg, err := clientFromConfig()
	if err != nil {
		return err
	}
	host := canonicalAPIHost(cfg.APIHost)

	var profile creght.Profile
	if cfg.TokenSource != tokenSourceNone {
		profile, err = client.GetProfile(ctx)
		if err != nil {
			return err
		}
	}

	if *jsonOut {
		out := map[string]any{
			"api_host":     host,
			"token_source": tokenSourceKey(cfg.TokenSource),
			"user":         nil,
		}
		if cfg.TokenSource != tokenSourceNone {
			out["user"] = profile
		}
		if err := printJSON(out); err != nil {
			return err
		}
	} else {
		fmt.Printf("API host:  %s\n", host)
		fmt.Printf("Token:     %s\n", describeToken(cfg))
		if cfg.TokenSource != tokenSourceNone {
			fmt.Printf("User:      %s\n", describeProfile(profile))
		}
	}

	if cfg.TokenSource == tokenSourceNone {
		return fmt.Errorf("not logged in to %s; run creght login, or set %s", host, tokenEnvVar)
	}

	return nil
}

func tokenSourceKey(s tokenSource) string {
	switch s {
	case tokenSourceCommand:
		return "command"
	case tokenSourceEnv:
		return "env"
	case tokenSourceSaved:
		return "saved"
	default:
		return "none"
	}
}

func describeProfile(p creght.Profile) string {
	name := p.Username
	if p.Nickname != "" && p.Nickname != p.Username {
		name = fmt.Sprintf("%s (%s)", p.Nickname, p.Username)
	}
	parts := []string{}
	if name != "" {
		parts = append(parts, name)
	}
	if p.Email != "" {
		parts = append(parts, p.Email)
	}
	if p.ID != "" {
		parts = append(parts, "id "+string(p.ID))
	}
	if len(parts) == 0 {
		return "(unknown)"
	}

	return strings.Join(parts, ", ")
}
