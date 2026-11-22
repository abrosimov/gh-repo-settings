//go:build unit

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const testToken = "s3cr3t-token-value"

var errWriteRefused = errors.New("write refused")

type refusingWriter struct{}

func (refusingWriter) Write([]byte) (int, error) { return 0, errWriteRefused }

type request struct {
	authorization string
	variables     map[string]any
}

type fakeGitHub struct {
	respond  func(index int, w http.ResponseWriter)
	requests []request
	mu       sync.Mutex
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var body struct {
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.requests = append(f.requests, request{authorization: r.Header.Get("Authorization"), variables: body.Variables})
	index := len(f.requests) - 1
	f.mu.Unlock()
	f.respond(index, w)
}

func (f *fakeGitHub) Requests() []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]request(nil), f.requests...)
}

type harness struct {
	env    *environment
	stdout *bytes.Buffer
	stderr *bytes.Buffer
	github *fakeGitHub
}

func newHarness(t *testing.T, token string, respond func(index int, w http.ResponseWriter)) *harness {
	t.Helper()
	github := &fakeGitHub{respond: respond}
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
		endpoint:   server.URL,
	}
	return h
}

func (h *harness) run(args ...string) int {
	return run(context.Background(), args, h.env)
}

func respondWith(status int, body string) func(int, http.ResponseWriter) {
	return func(_ int, w http.ResponseWriter) {
		w.WriteHeader(status)
		if _, err := io.WriteString(w, body); err != nil {
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

func page(t *testing.T, repos ...repoJSON) string {
	t.Helper()
	type entry struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	nodes := make([]map[string]any, 0, len(repos))
	for _, repo := range repos {
		entries := make([]entry, 0, len(repo.root))
		for _, name := range repo.root {
			entries = append(entries, entry{Name: name, Type: "blob"})
		}
		node := map[string]any{
			"name":       repo.name,
			"isFork":     repo.fork,
			"isArchived": repo.archived,
			"isEmpty":    false,
			"root":       map[string]any{"entries": entries},
			"dotGitHub":  nil,
			"docs":       nil,
		}
		if repo.licence != "" {
			node["licenseInfo"] = map[string]any{"spdxId": repo.licence}
		} else {
			node["licenseInfo"] = nil
		}
		nodes = append(nodes, node)
	}
	raw, err := json.Marshal(map[string]any{"data": map[string]any{"repositoryOwner": map[string]any{"repositories": map[string]any{
		"nodes":    nodes,
		"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
	}}}})
	if err != nil {
		t.Fatalf("encoding fixture: %v", err)
	}
	return string(raw)
}

func TestRunConformingOwnerExitsZeroWithSummaryOnStderrOnly(t *testing.T) {
	body := page(t,
		repoJSON{name: "one", licence: "Apache-2.0", root: []string{"README.md"}},
		repoJSON{name: "two", licence: "Apache-2.0"},
		repoJSON{name: "forked", licence: "MIT", root: []string{"CONTRIBUTING.md"}, fork: true},
		repoJSON{name: "old", licence: "MIT", archived: true},
	)
	h := newHarness(t, testToken, respondWith(http.StatusOK, body))

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
}

func TestRunViolationsExitOneAndPrintEveryViolationOnStdout(t *testing.T) {
	body := page(t,
		repoJSON{name: "b-none", root: []string{"README.md"}},
		repoJSON{name: "a-mit", licence: "MIT", root: []string{"CONTRIBUTING.md", "SECURITY.md"}},
		repoJSON{name: "c-fine", licence: "Apache-2.0"},
	)
	h := newHarness(t, testToken, respondWith(http.StatusOK, body))

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
			h := newHarness(t, testToken, respondWith(http.StatusOK, page(t, repoJSON{name: "one", licence: "Apache-2.0"})))

			code := h.run(tc.args...)

			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr: %s", code, h.stderr)
			}
			requests := h.github.Requests()
			if len(requests) != 1 {
				t.Fatalf("got %d requests, want 1", len(requests))
			}
			if requests[0].variables["owner"] != tc.wantOwner {
				t.Errorf("owner variable = %v, want %s", requests[0].variables["owner"], tc.wantOwner)
			}
			if want := "Bearer " + testToken; requests[0].authorization != want {
				t.Errorf("Authorization = %q, want %q", requests[0].authorization, want)
			}
			if want := tc.wantOwner + ": 1 repositories checked"; !strings.Contains(h.stderr.String(), want) {
				t.Errorf("stderr = %q, want it to contain %q", h.stderr, want)
			}
		})
	}
}

func TestRunGitHubFailuresExitTwoWithMessageOnStderrAndNothingOnStdout(t *testing.T) {
	cases := []struct {
		name    string
		respond func(int, http.ResponseWriter)
		contain string
	}{
		{"unauthorised", respondWith(http.StatusUnauthorized, `{"message":"Bad credentials"}`), "401"},
		{"server error", respondWith(http.StatusInternalServerError, "boom"), "500"},
		{"graphql errors", respondWith(http.StatusOK, `{"data":null,"errors":[{"message":"query refused"}]}`), "query refused"},
		{"unknown owner", respondWith(http.StatusOK, `{"data":{"repositoryOwner":null}}`), "no GitHub user or organisation"},
		{
			"unreadable contents",
			respondWith(http.StatusOK, strings.Replace(page(t, repoJSON{name: "hidden"}), `"root":{"entries":[]}`, `"root":null`, 1)),
			"abrosimov/hidden",
		},
		{"malformed JSON", respondWith(http.StatusOK, "{"), "decoding repositories"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, testToken, tc.respond)

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
	h := newHarness(t, testToken, respondWith(http.StatusOK, "{}"))
	h.env.endpoint = "http://127.0.0.1:1/graphql"

	code := h.run()

	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if h.stdout.Len() != 0 || strings.Contains(h.stderr.String(), testToken) {
		t.Errorf("stdout = %q, stderr = %q; want empty stdout and no token", h.stdout, h.stderr)
	}
}

func TestRunWithCancelledContextExitsTwo(t *testing.T) {
	h := newHarness(t, testToken, respondWith(http.StatusOK, page(t)))
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
	h := newHarness(t, "", respondWith(http.StatusOK, page(t)))

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
	h := newHarness(t, "", respondWith(http.StatusOK, page(t)))

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
			h := newHarness(t, testToken, respondWith(http.StatusOK, page(t)))

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
			h := newHarness(t, "", respondWith(http.StatusOK, page(t)))

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
	h := newHarness(t, testToken, respondWith(http.StatusOK, page(t, repoJSON{name: "mit", licence: "MIT"})))
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
	h := newHarness(t, testToken, respondWith(http.StatusOK, page(t, repoJSON{name: "ok", licence: "Apache-2.0"})))
	h.env.stderr = refusingWriter{}

	if code := h.run(); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}
