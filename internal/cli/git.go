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
	"path/filepath"
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
		return fmt.Errorf("git requires a subcommand: clone, url, setup, credential")
	}

	switch args[0] {
	case "clone":
		return runGitClone(ctx, args[1:])
	case "credential":
		return runGitCredential(ctx, args[1:])
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
	case "store":
		// The token lives in the CLI config; git must not cache a second copy.
		return nil
	case "erase":
		// git calls erase when the server rejected the credential we supplied —
		// in practice, an expired token. Without a word here the user only sees
		// "fatal: Authentication failed" with no hint about what to do, so use
		// stderr (stdout is the protocol channel) to say it. Deliberately does
		// NOT delete the stored token: a 401 can also be a transient server
		// problem or a permission issue, and destroying the login would be worse
		// than a retry.
		if request["host"] != "" {
			fmt.Fprintf(os.Stderr, "creght: credentials for %s were rejected — the token has likely expired, run `creght login`\n", request["host"])
		}
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

func gitCloneURL(apiHost string, projectID string, siteID string) (string, error) {
	base, err := url.Parse(canonicalAPIHost(apiHost))
	if err != nil {
		return "", fmt.Errorf("parse api host: %w", err)
	}
	base.Path = strings.TrimRight(base.Path, "/") +
		"/api/git/project/" + url.PathEscape(projectID) + "/site/" + url.PathEscape(siteID)
	return base.String(), nil
}

// gitCredentialConfigArgs builds the `-c` settings that make the cloned repository
// authenticate on its own.
//
// `git clone -c` persists these into the new repository's config, which is what lets
// cloning through creght be the whole setup: nothing outside that repo is touched,
// and no machine-wide step is needed.
//
// The empty value has to come FIRST. credential.<url>.helper is multi-valued and
// inherits from the system config, so on macOS osxkeychain is usually already in the
// list and answers before we do — a token refreshed by `creght login` would lose to a
// stale keychain entry, and the keychain's store step fails noisily besides. An empty
// value resets the inherited list, and because the key is URL-scoped it only affects
// this host; credentials for github.com and everything else are untouched.
func gitCredentialConfigArgs(apiHost string) []string {
	key := "credential." + strings.TrimRight(canonicalAPIHost(apiHost), "/") + ".helper"
	return []string{
		"-c", key + "=",
		"-c", key + "=" + gitCredentialHelperValue(),
	}
}

func gitCredentialHelperValue() string {
	self, err := os.Executable()
	if err != nil {
		// A helper resolved from PATH still works.
		self = "creght"
	}
	return "!" + quoteForGitConfig(filepath.ToSlash(self)) + " git credential"
}

// purgeGitCredentials asks git to drop any cached credential for a host.
//
// This is what clears an entry already sitting in the OS credential store
// (osxkeychain on macOS, Git Credential Manager on Windows). Registering our
// helper only stops future writes; it cannot remove what is already there, and a
// stale entry answers first and wins.
//
// `git credential reject` is used rather than an OS-specific command because it
// notifies every helper on the chain, so one code path covers all platforms.
// Failures are ignored: having nothing cached is the common case and must not
// turn into an error.
//
// Only logout calls this. setup and clone deliberately do not: touching the OS
// credential store as a side effect of a setup command is more surprising than
// helpful, and registering our helper already stops future writes there. Clearing
// a pre-existing entry is left to the user (`git credential reject`, or the
// keychain UI).
func purgeGitCredentials(ctx context.Context, host string) {
	stripped := stripScheme(host)
	if stripped == "" {
		return
	}
	protocol := "https"
	if strings.HasPrefix(host, "http://") {
		protocol = "http"
	}

	cmd := exec.CommandContext(ctx, "git", "credential", "reject")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("protocol=%s\nhost=%s\n\n", protocol, stripped))
	_ = cmd.Run()
}

// runGitClone clones a site and leaves the credential helper in the new
// repository's own config, so later fetch/pull keep working without touching the
// global git config or the OS keychain. `git clone -c` persists into the created
// repo, which is what makes the one-shot form possible.
func runGitClone(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("git clone", flag.ContinueOnError)
	siteID, dir := versionSiteFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("git clone accepts at most one target directory")
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

	gitArgs := append([]string{"clone"}, gitCredentialConfigArgs(cfg.APIHost)...)
	gitArgs = append(gitArgs, cloneURL)
	if fs.NArg() == 1 {
		gitArgs = append(gitArgs, fs.Arg(0))
	}

	cmd := exec.CommandContext(ctx, "git", gitArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone: %w", err)
	}
	return nil
}

func quoteForGitConfig(path string) string {
	if !strings.ContainsAny(path, " \t\"") {
		return path
	}
	return `"` + strings.ReplaceAll(path, `"`, `\"`) + `"`
}
