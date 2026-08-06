package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

// The site's version history is also reachable as a read-only git remote, so
// `git log`, `git show <version>:<path>`, `git diff` and `git blame` work on a
// site without the CLI reimplementing any of them.
//
// The only friction is credentials: git speaks HTTP Basic and would otherwise
// prompt for a password nobody has — the account has a token, not a git
// password. `creght git credential` is a git credential helper that answers with
// the token the CLI already stores, so nothing is typed and no token ends up in
// a remote URL or in shell history.

// gitCredentialUsername is arbitrary; the token in the password field is what
// authenticates. Basic auth requires *some* username, so this keeps it stable
// and recognizable in logs.
const gitCredentialUsername = "creght"

func runGit(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("git requires a subcommand: credential, url, setup")
	}

	switch args[0] {
	case "credential":
		return runGitCredential(ctx, args[1:])
	case "url":
		return runGitURL(ctx, args[1:])
	case "setup":
		return runGitSetup(ctx, args[1:])
	default:
		return fmt.Errorf("unknown git subcommand: %s", args[0])
	}
}

// runGitCredential implements git's credential helper protocol.
//
// git invokes the helper with an operation argument and feeds key=value lines on
// stdin, terminated by a blank line. Only `get` produces output; `store` and
// `erase` must consume stdin and succeed quietly, otherwise git reports the
// helper as broken.
func runGitCredential(ctx context.Context, args []string) error {
	operation := "get"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		operation = args[0]
		args = args[1:]
	}

	fs := flag.NewFlagSet("git credential", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	request, err := readGitCredentialRequest(os.Stdin)
	if err != nil {
		return err
	}

	switch operation {
	case "get":
	case "store", "erase":
		// The token lives in the CLI config; git must not cache or delete it.
		return nil
	default:
		return fmt.Errorf("unsupported credential operation: %s", operation)
	}

	host := request["host"]
	if host == "" {
		return fmt.Errorf("git credential get: no host in request")
	}
	protocol := request["protocol"]
	if protocol == "" {
		protocol = "https"
	}

	token, err := tokenForGitHost(protocol, host)
	if err != nil {
		return err
	}

	// Answering nothing lets git fall back to prompting, which is the right
	// behavior for a host we have no token for.
	if token == "" {
		return nil
	}

	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	fmt.Fprintf(out, "username=%s\n", gitCredentialUsername)
	fmt.Fprintf(out, "password=%s\n", token)
	return nil
}

func readGitCredentialRequest(r io.Reader) (map[string]string, error) {
	request := map[string]string{}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			break
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		request[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read credential request: %w", err)
	}
	return request, nil
}

// tokenForGitHost resolves the token for a git host from the same config the
// rest of the CLI uses, so `creght login` is the only step needed.
func tokenForGitHost(protocol string, host string) (string, error) {
	cfg, err := loadConfig()
	if err != nil {
		return "", err
	}

	candidate := protocol + "://" + host
	if token := cfg.Tokens[candidate]; token != "" {
		return token, nil
	}
	// Fall back across schemes, then to the default token, so a config written
	// with a different scheme still works.
	for stored, token := range cfg.Tokens {
		if strings.EqualFold(stripScheme(stored), host) && token != "" {
			return token, nil
		}
	}
	if strings.EqualFold(stripScheme(cfg.APIHost), host) {
		return cfg.Token, nil
	}
	return "", nil
}

func stripScheme(host string) string {
	host = strings.TrimSpace(host)
	if idx := strings.Index(host, "://"); idx >= 0 {
		host = host[idx+3:]
	}
	return strings.TrimSuffix(host, "/")
}

// runGitURL prints the clone URL for a site. Inside a pulled workspace the site
// is discovered the same way pull/push discover it.
func runGitURL(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("git url", flag.ContinueOnError)
	siteID, dir := versionSiteFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	projectID, realSiteID, _, err := resolveVersionSite(fs, *siteID, *dir, true)
	if err != nil {
		return err
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	cloneURL, err := gitCloneURL(cfg.APIHost, projectID, realSiteID)
	if err != nil {
		return err
	}

	fmt.Println(cloneURL)
	return nil
}

// gitCloneURL builds the remote URL. The /api prefix is required: only /api/* is
// routed to the API service, so a bare /git path reaches the frontend instead.
func gitCloneURL(apiHost string, projectID string, siteID string) (string, error) {
	base, err := url.Parse(canonicalAPIHost(apiHost))
	if err != nil {
		return "", fmt.Errorf("parse api host: %w", err)
	}
	base.Path = strings.TrimRight(base.Path, "/") +
		"/api/git/project/" + url.PathEscape(projectID) + "/site/" + url.PathEscape(siteID)
	return base.String(), nil
}

// runGitSetup registers the credential helper for the configured host, then
// prints the clone command. Scoped per host rather than globally so it cannot
// affect credentials for github.com or anything else.
func runGitSetup(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("git setup", flag.ContinueOnError)
	siteID, dir := versionSiteFlags(fs)
	global := fs.Bool("global", true, "write to the global git config; --global=false writes to the repository config")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	host := canonicalAPIHost(cfg.APIHost)

	self, err := os.Executable()
	if err != nil {
		// Fall back to the command name; a helper resolved from PATH still works.
		self = "creght"
	}

	// The "!" prefix tells git the value is a command line, not a
	// git-credential-<name> binary.
	key := "credential." + strings.TrimRight(host, "/") + ".helper"
	value := "!" + quoteForGitConfig(self) + " git credential"

	// credential.helper is multi-valued and inherits from the system config, so
	// on macOS osxkeychain is usually already in the list. Two problems with
	// leaving it there: it answers first, so a token refreshed by `creght login`
	// loses to a stale keychain entry; and its store step fails noisily
	// ("failed to store: -60006"). An empty value resets the inherited list, and
	// because the key is URL-scoped this only affects this host — credentials
	// for github.com and everything else are untouched.
	for _, args := range [][]string{
		{"--unset-all", key},
		{"--add", key, ""},
		{"--add", key, value},
	} {
		configArgs := []string{"config"}
		if *global {
			configArgs = append(configArgs, "--global")
		}
		configArgs = append(configArgs, args...)

		cmd := exec.CommandContext(ctx, "git", configArgs...)
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil && args[0] != "--unset-all" {
			// --unset-all exits non-zero when the key was never set, which is
			// the normal first-run case.
			return fmt.Errorf("git config %s: %w", key, err)
		}
	}

	fmt.Printf("Configured git credential helper for %s\n", host)

	projectID, realSiteID, _, err := resolveVersionSite(fs, *siteID, *dir, true)
	if err != nil {
		// Setup succeeded; not being in a workspace is not a failure.
		fmt.Println("Run `creght git url` inside a pulled workspace to get the clone URL.")
		return nil
	}
	cloneURL, err := gitCloneURL(cfg.APIHost, projectID, realSiteID)
	if err != nil {
		return err
	}
	fmt.Printf("\n  git clone %s\n", cloneURL)
	return nil
}

func quoteForGitConfig(path string) string {
	if !strings.ContainsAny(path, " \t\"") {
		return path
	}
	return `"` + strings.ReplaceAll(path, `"`, `\"`) + `"`
}
