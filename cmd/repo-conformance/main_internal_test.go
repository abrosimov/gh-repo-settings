//go:build unit

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testToken = "s3cr3t-token-value"
	testOwner = "abrosimov"
)

var errWriteRefused = errors.New("write refused")

type refusingWriter struct{}

func (refusingWriter) Write([]byte) (int, error) { return 0, errWriteRefused }

type request struct {
	path          string
	authorization string
}

type handler func(w http.ResponseWriter, r *http.Request)

type fakeGitHub struct {
	t        *testing.T
	routes   map[string]handler
	requests []request
	mu       sync.Mutex
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, request{path: r.URL.EscapedPath(), authorization: r.Header.Get("Authorization")})
	route := f.routes[r.URL.EscapedPath()]
	f.mu.Unlock()
	if route == nil {
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusTeapot)
		return
	}
	route(w, r)
}

func (f *fakeGitHub) Requests() []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]request(nil), f.requests...)
}

func (f *fakeGitHub) Count(path string) int {
	count := 0
	for _, r := range f.Requests() {
		if r.path == path {
			count++
		}
	}
	return count
}

type harness struct {
	env    *environment
	stdout *bytes.Buffer
	stderr *bytes.Buffer
	github *fakeGitHub
}

func newHarness(t *testing.T, token string, routes map[string]handler) *harness {
	t.Helper()
	github := &fakeGitHub{t: t, routes: routes}
	server := httptest.NewServer(github)
	t.Cleanup(server.Close)
	h := &harness{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, github: github}
	h.env = &environment{
		stdout: h.stdout,
		stderr: h.stderr,
		getenv: func(key string) string {
			if key == "GITHUB_TOKEN" {
				return token
			}
			return ""
		},
		httpClient: server.Client(),
		backoff:    func(int) time.Duration { return 0 },
		endpoint:   server.URL,
	}
	return h
}

func (h *harness) run(args ...string) int {
	return run(context.Background(), args, h.env)
}

func respondWith(status int, body string) handler {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		if _, err := io.WriteString(w, body); err != nil {
			return
		}
	}
}

func respondJSON(value any) handler {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(value); err != nil {
			return
		}
	}
}

type repoJSON struct {
	name     string
	licence  string
	root     []string
	fork     bool
	archived bool
}

func usersPath(owner string) string { return "/users/" + owner }

func orgReposPath(owner string) string { return "/orgs/" + owner + "/repos" }

func rootTreePath(owner, repo string) string {
	return "/repos/" + owner + "/" + repo + "/git/trees/main"
}

func organisationRoutes(owner string, repos ...repoJSON) map[string]handler {
	listed := make([]map[string]any, 0, len(repos))
	routes := map[string]handler{usersPath(owner): respondJSON(map[string]any{"login": owner, "type": "Organization"})}
	for _, repo := range repos {
		item := map[string]any{
			"name":           repo.name,
			"owner":          map[string]any{"login": owner, "type": "Organization"},
			"fork":           repo.fork,
			"archived":       repo.archived,
			"default_branch": "main",
			"license":        nil,
		}
		if repo.licence != "" {
			item["license"] = map[string]any{"spdx_id": repo.licence}
		}
		listed = append(listed, item)
		entries := make([]map[string]string, 0, len(repo.root))
		for _, name := range repo.root {
			entries = append(entries, map[string]string{"path": name, "type": "blob", "sha": "sha-" + name})
		}
		routes[rootTreePath(owner, repo.name)] = respondJSON(map[string]any{"tree": entries})
	}
	routes[orgReposPath(owner)] = respondJSON(listed)
	return routes
}

func TestRunConformingOwnerExitsZeroWithSummaryOnStderrOnly(t *testing.T) {
	routes := organisationRoutes(testOwner,
		repoJSON{name: "one", licence: "Apache-2.0", root: []string{"README.md"}},
		repoJSON{name: "two", licence: "Apache-2.0"},
		repoJSON{name: "forked", licence: "MIT", root: []string{"CONTRIBUTING.md"}, fork: true},
		repoJSON{name: "old", licence: "MIT", archived: true},
	)
	h := newHarness(t, testToken, routes)

	code := h.run()

	if code != 0 {
		t.Errorf("exit code = %d, want 0; stderr: %s", code, h.stderr)
	}
	if h.stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", h.stdout)
	}
	if got, want := h.stderr.String(), "abrosimov: 2 repositories checked, 0 violation(s)\n"; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
	for _, skipped := range []string{"forked", "old"} {
		if got := h.github.Count(rootTreePath(testOwner, skipped)); got != 0 {
			t.Errorf("the tree of %s was read %d times, want 0", skipped, got)
		}
	}
}

func TestRunViolationsExitOneAndPrintEveryViolationOnStdout(t *testing.T) {
	routes := organisationRoutes(testOwner,
		repoJSON{name: "b-none", root: []string{"README.md"}},
		repoJSON{name: "a-mit", licence: "MIT", root: []string{"CONTRIBUTING.md", "SECURITY.md"}},
		repoJSON{name: "c-fine", licence: "Apache-2.0"},
	)
	h := newHarness(t, testToken, routes)

	code := h.run()

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	wantStdout := "abrosimov/a-mit: licence: MIT detected, Apache-2.0 required\n" +
		"abrosimov/a-mit: community health override: CONTRIBUTING.md, SECURITY.md\n" +
		"abrosimov/b-none: licence: none detected, Apache-2.0 required\n"
	if got := h.stdout.String(); got != wantStdout {
		t.Errorf("stdout = %q, want %q", got, wantStdout)
	}
	if got, want := h.stderr.String(), "abrosimov: 3 repositories checked, 3 violation(s)\n"; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
}

func TestRunSendsTokenAndOwnerToGitHub(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantOwner string
	}{
		{"default owner", nil, "abrosimov"},
		{"double dash", []string{"--owner", "someone-else"}, "someone-else"},
		{"single dash with equals", []string{"-owner=another"}, "another"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			routes := organisationRoutes(tc.wantOwner, repoJSON{name: "one", licence: "Apache-2.0"})
			h := newHarness(t, testToken, routes)

			code := h.run(tc.args...)

			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr: %s", code, h.stderr)
			}
			requests := h.github.Requests()
			if len(requests) != 3 {
				t.Fatalf("got %d requests, want 3 (owner, listing, tree)", len(requests))
			}
			if requests[0].path != usersPath(tc.wantOwner) {
				t.Errorf("first request path = %q, want %q", requests[0].path, usersPath(tc.wantOwner))
			}
			for i, r := range requests {
				if want := "Bearer " + testToken; r.authorization != want {
					t.Errorf("request %d (%s) Authorization = %q, want %q", i, r.path, r.authorization, want)
				}
			}
			if want := tc.wantOwner + ": 1 repositories checked"; !strings.Contains(h.stderr.String(), want) {
				t.Errorf("stderr = %q, want it to contain %q", h.stderr, want)
			}
		})
	}
}

func TestRunGitHubFailuresExitTwoWithMessageOnStderrAndNothingOnStdout(t *testing.T) {
	cases := []struct {
		replace map[string]handler
		name    string
		contain string
	}{
		{
			name:    "unauthorised",
			replace: map[string]handler{usersPath(testOwner): respondWith(http.StatusUnauthorized, `{"message":"Bad credentials"}`)},
			contain: "401",
		},
		{
			name:    "server error",
			replace: map[string]handler{usersPath(testOwner): respondWith(http.StatusInternalServerError, "boom")},
			contain: "500",
		},
		{
			name:    "unknown owner",
			replace: map[string]handler{usersPath(testOwner): respondWith(http.StatusNotFound, `{"message":"Not Found"}`)},
			contain: "no GitHub user or organisation",
		},
		{
			name:    "unreadable contents",
			replace: map[string]handler{rootTreePath(testOwner, "hidden"): respondWith(http.StatusForbidden, `{"message":"forbidden"}`)},
			contain: "abrosimov/hidden",
		},
		{
			name:    "malformed JSON",
			replace: map[string]handler{orgReposPath(testOwner): respondWith(http.StatusOK, "{")},
			contain: "decoding repositories",
		},
		{
			name:    "gateway that keeps failing",
			replace: map[string]handler{orgReposPath(testOwner): respondWith(http.StatusBadGateway, "<html>bad gateway</html>")},
			contain: "502 <html>bad gateway</html>",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			routes := organisationRoutes(testOwner, repoJSON{name: "hidden", licence: "Apache-2.0"})
			maps.Copy(routes, tc.replace)
			h := newHarness(t, testToken, routes)

			code := h.run()

			if code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
			if h.stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty", h.stdout)
			}
			if !strings.HasPrefix(h.stderr.String(), "repo-conformance: ") || !strings.Contains(h.stderr.String(), tc.contain) {
				t.Errorf("stderr = %q, want a repo-conformance message containing %q", h.stderr, tc.contain)
			}
			if strings.Contains(h.stderr.String(), testToken) {
				t.Errorf("stderr = %q leaks the token", h.stderr)
			}
		})
	}
}

func TestRunWithUnreachableGitHubExitsTwoWithoutLeakingTheToken(t *testing.T) {
	h := newHarness(t, testToken, nil)
	h.env.endpoint = "http://127.0.0.1:1"

	code := h.run()

	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if h.stdout.Len() != 0 || strings.Contains(h.stderr.String(), testToken) {
		t.Errorf("stdout = %q, stderr = %q; want empty stdout and no token", h.stdout, h.stderr)
	}
	if !strings.Contains(h.stderr.String(), "calling the GitHub API") {
		t.Errorf("stderr = %q, want it to report the failed call", h.stderr)
	}
}

func TestRunRetriesATransientGitHubFailureAndGivesUpAfterThreeAttempts(t *testing.T) {
	t.Run("recovers after one bad gateway", func(t *testing.T) {
		routes := organisationRoutes(testOwner, repoJSON{name: "one", licence: "Apache-2.0"})
		owner := routes[usersPath(testOwner)]
		var calls atomic.Int32
		routes[usersPath(testOwner)] = func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				respondWith(http.StatusBadGateway, "bad gateway")(w, r)
				return
			}
			owner(w, r)
		}
		h := newHarness(t, testToken, routes)

		code := h.run()

		if code != 0 {
			t.Errorf("exit code = %d, want 0; stderr: %s", code, h.stderr)
		}
		if got := h.github.Count(usersPath(testOwner)); got != 2 {
			t.Errorf("the owner was looked up %d times, want 2", got)
		}
		if got, want := h.stderr.String(), "abrosimov: 1 repositories checked, 0 violation(s)\n"; got != want {
			t.Errorf("stderr = %q, want %q", got, want)
		}
	})

	t.Run("gives up after three bad gateways", func(t *testing.T) {
		routes := organisationRoutes(testOwner)
		routes[usersPath(testOwner)] = respondWith(http.StatusBadGateway, "bad gateway")
		h := newHarness(t, testToken, routes)

		code := h.run()

		if code != 2 {
			t.Errorf("exit code = %d, want 2", code)
		}
		if got := h.github.Count(usersPath(testOwner)); got != 3 {
			t.Errorf("the owner was looked up %d times, want 3", got)
		}
		if h.stdout.Len() != 0 {
			t.Errorf("stdout = %q, want empty", h.stdout)
		}
	})
}

func TestRunWithCancelledContextExitsTwo(t *testing.T) {
	h := newHarness(t, testToken, organisationRoutes(testOwner))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	code := run(ctx, nil, h.env)

	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if h.stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", h.stdout)
	}
}

func TestRunWithoutTokenExitsTwoBeforeCallingGitHub(t *testing.T) {
	h := newHarness(t, "", nil)

	code := h.run()

	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if h.stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", h.stdout)
	}
	if !strings.Contains(h.stderr.String(), "GITHUB_TOKEN") {
		t.Errorf("stderr = %q, want it to name GITHUB_TOKEN", h.stderr)
	}
	if got := len(h.github.Requests()); got != 0 {
		t.Errorf("got %d requests without a token, want 0", got)
	}
}

func TestRunTokenUnsetMessageSeparatesTheReasonFromTheInstruction(t *testing.T) {
	h := newHarness(t, "", nil)

	h.run()

	if want := "GITHUB_TOKEN is not set: export a token"; !strings.Contains(h.stderr.String(), want) {
		t.Errorf("stderr = %q, want it to contain %q", h.stderr, want)
	}
}

func TestRunRejectsBadArgumentsWithExitTwoBeforeCallingGitHub(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"positional argument", []string{"someone"}},
		{"positional argument after flag", []string{"--owner", "x", "extra"}},
		{"unknown flag", []string{"--bogus"}},
		{"flag without value", []string{"--owner"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, testToken, nil)

			code := h.run(tc.args...)

			if code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
			if h.stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty", h.stdout)
			}
			if !strings.Contains(h.stderr.String(), "Usage: repo-conformance") {
				t.Errorf("stderr = %q, want the usage text", h.stderr)
			}
			if got := len(h.github.Requests()); got != 0 {
				t.Errorf("got %d requests, want 0", got)
			}
		})
	}
}

func TestRunHelpExitsZeroWithUsageOnStderr(t *testing.T) {
	for _, flag := range []string{"-h", "--help", "-help"} {
		t.Run(flag, func(t *testing.T) {
			h := newHarness(t, "", nil)

			code := h.run(flag)

			if code != 0 {
				t.Errorf("exit code = %d, want 0", code)
			}
			if h.stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty", h.stdout)
			}
			stderr := h.stderr.String()
			if !strings.Contains(stderr, "Usage: repo-conformance") || !strings.Contains(stderr, "-owner") {
				t.Errorf("stderr = %q, want usage listing the owner flag", stderr)
			}
			if got := len(h.github.Requests()); got != 0 {
				t.Errorf("got %d requests, want 0", got)
			}
		})
	}
}

func TestRunExitsTwoWhenViolationsCannotBeWritten(t *testing.T) {
	h := newHarness(t, testToken, organisationRoutes(testOwner, repoJSON{name: "mit", licence: "MIT"}))
	h.env.stdout = refusingWriter{}

	code := h.run()

	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if !strings.Contains(h.stderr.String(), "writing violations") {
		t.Errorf("stderr = %q, want it to report the failed write", h.stderr)
	}
}

func TestRunExitsTwoWhenTheSummaryCannotBeWritten(t *testing.T) {
	h := newHarness(t, testToken, organisationRoutes(testOwner, repoJSON{name: "ok", licence: "Apache-2.0"}))
	h.env.stderr = refusingWriter{}

	if code := h.run(); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}
