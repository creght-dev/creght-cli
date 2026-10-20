package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode"
)

// tokenCommandEnvVar names the environment variable holding a command that
// prints a token for a given API host, in the manner of git's credential
// helpers.
//
// CREGHT_TOKEN carries one token, so it serves one deployment for as long as
// that token lives. A host program whose user has sites on several deployments,
// or whose tokens expire during a long agent run, sets this instead: the CLI
// runs it once it knows which host the command talks to, with `--host <host>`
// appended, and sends whatever it prints. Like CREGHT_TOKEN, the token is
// borrowed — never saved, refreshed or revoked here.
const tokenCommandEnvVar = "CREGHT_TOKEN_COMMAND"

// tokenCommandTimeout bounds one run of the command, so a helper that hangs
// fails the command instead of wedging it.
const tokenCommandTimeout = 2 * time.Minute

func envTokenCommand() (string, bool) {
	v := strings.TrimSpace(os.Getenv(tokenCommandEnvVar))
	return v, v != ""
}

type tokenCommandResult struct {
	token string
	err   error
}

// tokenCommandCache holds each host's answer for the rest of the process, so a
// command that makes hundreds of requests runs the helper once per host.
var tokenCommandCache = struct {
	sync.Mutex
	byHost map[string]tokenCommandResult
}{byHost: map[string]tokenCommandResult{}}

func resetTokenCommandCache() {
	tokenCommandCache.Lock()
	defer tokenCommandCache.Unlock()
	tokenCommandCache.byHost = map[string]tokenCommandResult{}
}

// commandToken returns the token CREGHT_TOKEN_COMMAND prints for host, running
// it on the first call for that host and answering later calls from the cache.
// A failure is cached too: the command fails anyway, and asking again would
// only repeat the same message.
func commandToken(ctx context.Context, host string) (string, error) {
	host = canonicalAPIHost(host)

	tokenCommandCache.Lock()
	defer tokenCommandCache.Unlock()
	if r, ok := tokenCommandCache.byHost[host]; ok {
		return r.token, r.err
	}
	token, err := runTokenCommand(ctx, host)
	tokenCommandCache.byHost[host] = tokenCommandResult{token: token, err: err}

	return token, err
}

// runTokenCommand runs CREGHT_TOKEN_COMMAND for host. The command line is split
// into words here rather than handed to a shell, because Windows has no sh to
// hand it to.
func runTokenCommand(ctx context.Context, host string) (string, error) {
	line, ok := envTokenCommand()
	if !ok {
		return "", fmt.Errorf("%s is not set", tokenCommandEnvVar)
	}
	words, err := splitCommandLine(line)
	if err != nil {
		return "", fmt.Errorf("%s: %w", tokenCommandEnvVar, err)
	}
	words = append(words, "--host", host)

	ctx, cancel := context.WithTimeout(ctx, tokenCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, words[0], words[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// The helper's own stderr is the message: it knows why it has no token
		// for this host and what the user should do about it.
		if msg := strings.TrimRight(stderr.String(), "\r\n"); msg != "" {
			return "", errors.New(msg)
		}
		return "", fmt.Errorf("%s (%s) failed for %s: %w", tokenCommandEnvVar, line, host, err)
	}

	token := strings.TrimSpace(stdout.String())
	if token == "" {
		return "", fmt.Errorf("%s (%s) printed no token for %s", tokenCommandEnvVar, line, host)
	}

	return token, nil
}

// splitCommandLine splits a command line into words the way a POSIX shell
// would, short of expanding anything: whitespace separates words, single quotes
// keep everything literally, and double quotes keep everything but a backslash
// before ", \, $ or `. Outside quotes a backslash makes the next character
// literal.
//
// On Windows a backslash is always literal, so `"C:\Program Files\x.exe" token`
// and UNC paths come through unchanged.
func splitCommandLine(line string) ([]string, error) {
	backslashEscapes := runtime.GOOS != "windows"

	var words []string
	var word strings.Builder
	inWord := false
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case unicode.IsSpace(r):
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
		case r == '\'':
			inWord = true
			end := indexRune(runes, i+1, '\'')
			if end < 0 {
				return nil, errors.New("unterminated single quote")
			}
			word.WriteString(string(runes[i+1 : end]))
			i = end
		case r == '"':
			inWord = true
			closed := false
			for i++; i < len(runes); i++ {
				c := runes[i]
				if c == '"' {
					closed = true
					break
				}
				if c == '\\' && backslashEscapes && i+1 < len(runes) && strings.ContainsRune("\"\\$`", runes[i+1]) {
					i++
					c = runes[i]
				}
				word.WriteRune(c)
			}
			if !closed {
				return nil, errors.New("unterminated double quote")
			}
		case r == '\\' && backslashEscapes:
			inWord = true
			if i+1 < len(runes) {
				i++
				word.WriteRune(runes[i])
			}
		default:
			inWord = true
			word.WriteRune(r)
		}
	}
	if inWord {
		words = append(words, word.String())
	}
	if len(words) == 0 {
		return nil, errors.New("empty command")
	}

	return words, nil
}

func indexRune(runes []rune, from int, target rune) int {
	for i := from; i < len(runes); i++ {
		if runes[i] == target {
			return i
		}
	}

	return -1
}
