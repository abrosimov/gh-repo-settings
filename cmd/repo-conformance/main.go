package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/abrosimov/gh-repo-settings/internal/conformance"
)

const (
	exitConforms   = 0
	exitViolations = 1
	exitError      = 2

	programName  = "repo-conformance"
	defaultOwner = "abrosimov"

	// Longer than GitHub's own GraphQL execution limit, so a slow page fails with GitHub's error rather than ours.
	requestTimeout = 20 * time.Second
)

var errTokenUnset = errors.New("GITHUB_TOKEN is not set: export a token that can read the owner's repositories")

type environment struct {
	stdout     io.Writer
	stderr     io.Writer
	getenv     func(string) string
	httpClient *http.Client
	endpoint   string
}

func main() {
	env := &environment{
		stdout:     os.Stdout,
		stderr:     os.Stderr,
		getenv:     os.Getenv,
		httpClient: &http.Client{Timeout: requestTimeout},
		endpoint:   conformance.GitHubGraphQLEndpoint,
	}
	os.Exit(run(context.Background(), os.Args[1:], env))
}

func run(ctx context.Context, args []string, env *environment) int {
	flags := flag.NewFlagSet(programName, flag.ContinueOnError)
	flags.SetOutput(env.stderr)
	flags.Usage = func() { usage(flags) }
	owner := flags.String("owner", defaultOwner, "GitHub user whose repositories are checked")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitConforms
		}
		return exitError
	}
	if flags.NArg() != 0 {
		usage(flags)
		return exitError
	}

	token := env.getenv("GITHUB_TOKEN")
	if token == "" {
		return fail(env.stderr, errTokenUnset)
	}
	client := conformance.NewGitHubClient(env.httpClient, env.endpoint, token)
	repositories, err := client.Repositories(ctx, *owner)
	if err != nil {
		return fail(env.stderr, err)
	}

	result := conformance.Check(repositories)
	for i := range result.Violations {
		if _, err := fmt.Fprintln(env.stdout, result.Violations[i].String()); err != nil {
			return fail(env.stderr, fmt.Errorf("writing violations: %w", err))
		}
	}
	code := exitConforms
	if len(result.Violations) > 0 {
		code = exitViolations
	}
	return report(env.stderr, code, fmt.Sprintf("%s: %d repositories checked, %d violation(s)\n",
		*owner, result.Checked, len(result.Violations)))
}

func usage(flags *flag.FlagSet) {
	text := "Usage: " + programName + " [--owner LOGIN]\n\n" +
		"Fails when a non-fork, non-archived repository of the owner is not licensed under " + conformance.RequiredLicence + "\n" +
		"or carries community health files of its own instead of the defaults from its " + conformance.DefaultsRepository +
		" repository.\n" +
		"Reads GitHub with the token in GITHUB_TOKEN. Exits 1 on a violation, 2 on an error.\n\nFlags:\n"
	if _, err := io.WriteString(flags.Output(), text); err != nil {
		return
	}
	flags.PrintDefaults()
}

func fail(stderr io.Writer, err error) int {
	return report(stderr, exitError, fmt.Sprintf("%s: %v\n", programName, err))
}

func report(w io.Writer, code int, text string) int {
	if _, err := io.WriteString(w, text); err != nil {
		return exitError
	}
	return code
}
