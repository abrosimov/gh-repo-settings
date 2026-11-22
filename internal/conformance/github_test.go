//go:build unit

package conformance_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/abrosimov/gh-repo-settings/internal/conformance"
)

const (
	testToken = "test-token-value"

	maxResponseBytes = 32 << 20
)

type recordedRequest struct {
	Header    http.Header
	Method    string
	Query     string
	Variables map[string]any
}

type fakeGitHub struct {
	respond  func(index int, w http.ResponseWriter)
	requests []recordedRequest
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
		Query     string         `json:"query"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.requests = append(f.requests, recordedRequest{Header: r.Header.Clone(), Method: r.Method, Query: body.Query, Variables: body.Variables})
	index := len(f.requests) - 1
	f.mu.Unlock()
	f.respond(index, w)
}

func (f *fakeGitHub) Requests() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

func newFakeGitHub(t *testing.T, respond func(index int, w http.ResponseWriter)) (*conformance.GitHubClient, *fakeGitHub) {
	t.Helper()
	fake := &fakeGitHub{respond: respond}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	return conformance.NewGitHubClient(server.Client(), server.URL, testToken), fake
}

func serveBody(status int, body string) func(int, http.ResponseWriter) {
	return func(_ int, w http.ResponseWriter) {
		w.WriteHeader(status)
		if _, err := io.WriteString(w, body); err != nil {
			return
		}
	}
}

func marshal(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encoding fixture: %v", err)
	}
	return string(raw)
}

type treeEntryJSON struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func tree(entries ...treeEntryJSON) map[string]any {
	if entries == nil {
		entries = []treeEntryJSON{}
	}
	return map[string]any{"entries": entries}
}

func node(name string, overrides map[string]any) map[string]any {
	n := map[string]any{
		"name":        name,
		"isFork":      false,
		"isArchived":  false,
		"isEmpty":     false,
		"licenseInfo": map[string]any{"spdxId": "Apache-2.0"},
		"root":        tree(treeEntryJSON{"README.md", "blob"}),
		"dotGitHub":   nil,
		"docs":        nil,
	}
	maps.Copy(n, overrides)
	return n
}

func pageResponse(t *testing.T, nodes []map[string]any, hasNext bool, cursor string) string {
	t.Helper()
	var endCursor any
	if cursor != "" {
		endCursor = cursor
	}
	return marshal(t, map[string]any{"data": map[string]any{"repositoryOwner": map[string]any{"repositories": map[string]any{
		"nodes":    nodes,
		"pageInfo": map[string]any{"hasNextPage": hasNext, "endCursor": endCursor},
	}}}})
}

func listRepositories(t *testing.T, client *conformance.GitHubClient) ([]conformance.Repository, error) {
	t.Helper()
	return client.Repositories(t.Context(), testOwner)
}

func TestRepositoriesFollowsPaginationAndSendsAuthorisedRequests(t *testing.T) {
	client, fake := newFakeGitHub(t, func(index int, w http.ResponseWriter) {
		var body string
		switch index {
		case 0:
			body = pageResponse(t, []map[string]any{node("first", nil)}, true, "cursor-1")
		default:
			body = pageResponse(t, []map[string]any{node("second", nil)}, false, "cursor-2")
		}
		serveBody(http.StatusOK, body)(index, w)
	})

	repositories, err := listRepositories(t, client)

	if err != nil {
		t.Fatalf("Repositories: %v", err)
	}
	names := make([]string, 0, len(repositories))
	for _, repo := range repositories {
		names = append(names, repo.Name)
	}
	if !slices.Equal(names, []string{"first", "second"}) {
		t.Errorf("repositories = %v, want [first second]", names)
	}
	requests := fake.Requests()
	if len(requests) != 2 {
		t.Fatalf("got %d requests, want 2", len(requests))
	}
	for i, request := range requests {
		if request.Method != http.MethodPost {
			t.Errorf("request %d method = %s, want POST", i, request.Method)
		}
		if got, want := request.Header.Get("Authorization"), "Bearer "+testToken; got != want {
			t.Errorf("request %d Authorization = %q, want %q", i, got, want)
		}
		if got := request.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("request %d Content-Type = %q, want application/json", i, got)
		}
		if request.Variables["owner"] != testOwner {
			t.Errorf("request %d owner = %v, want %s", i, request.Variables["owner"], testOwner)
		}
		if got, ok := request.Variables["first"].(float64); !ok || got <= 0 {
			t.Errorf("request %d first = %v, want a positive page size", i, request.Variables["first"])
		}
	}
	if _, ok := requests[0].Variables["cursor"]; ok {
		t.Errorf("first request carries cursor %v, want none", requests[0].Variables["cursor"])
	}
	if got := requests[1].Variables["cursor"]; got != "cursor-1" {
		t.Errorf("second request cursor = %v, want cursor-1", got)
	}
}

func TestRepositoriesQueryReadsLicenceAndTheThreeTreesOfTheDefaultBranch(t *testing.T) {
	client, fake := newFakeGitHub(t, serveBody(http.StatusOK, pageResponse(t, nil, false, "")))

	if _, err := listRepositories(t, client); err != nil {
		t.Fatalf("Repositories: %v", err)
	}

	requests := fake.Requests()
	if len(requests) != 1 {
		t.Fatalf("got %d requests, want 1", len(requests))
	}
	for _, fragment := range []string{"spdxId", `"HEAD:"`, `"HEAD:.github"`, `"HEAD:docs"`, "isFork", "isArchived", "isEmpty"} {
		if !strings.Contains(requests[0].Query, fragment) {
			t.Errorf("query does not request %s:\n%s", fragment, requests[0].Query)
		}
	}
}

func TestRepositoriesMapsNodesToRepositories(t *testing.T) {
	nodes := []map[string]any{
		node("project", map[string]any{
			"licenseInfo": map[string]any{"spdxId": "MIT"},
			"root": tree(
				treeEntryJSON{"CONTRIBUTING.md", "blob"},
				treeEntryJSON{"src", "tree"},
				treeEntryJSON{"vendored", "commit"},
				treeEntryJSON{"mystery", "tag"},
			),
			"dotGitHub": tree(treeEntryJSON{"ISSUE_TEMPLATE", "tree"}, treeEntryJSON{"FUNDING.yml", "blob"}),
			"docs":      tree(treeEntryJSON{"SECURITY.md", "blob"}),
		}),
		node("unlicensed", map[string]any{"licenseInfo": nil}),
		node("null-spdx", map[string]any{"licenseInfo": map[string]any{"spdxId": nil}}),
		node("path-is-a-file", map[string]any{"dotGitHub": map[string]any{}, "docs": map[string]any{}}),
	}
	client, _ := newFakeGitHub(t, serveBody(http.StatusOK, pageResponse(t, nodes, false, "")))

	repositories, err := listRepositories(t, client)

	if err != nil {
		t.Fatalf("Repositories: %v", err)
	}
	if len(repositories) != 4 {
		t.Fatalf("got %d repositories, want 4: %+v", len(repositories), repositories)
	}
	project := repositories[0]
	if project.Owner != testOwner || project.Name != "project" || project.Licence != "MIT" {
		t.Errorf("project = %+v, want owner %s, name project, licence MIT", project, testOwner)
	}
	wantRoot := []conformance.Entry{file("CONTRIBUTING.md"), directory("src")}
	if !slices.Equal(project.Root, wantRoot) {
		t.Errorf("Root = %+v, want %+v (submodules and unknown types ignored)", project.Root, wantRoot)
	}
	if want := []conformance.Entry{directory("ISSUE_TEMPLATE"), file("FUNDING.yml")}; !slices.Equal(project.DotGitHub, want) {
		t.Errorf("DotGitHub = %+v, want %+v", project.DotGitHub, want)
	}
	if want := []conformance.Entry{file("SECURITY.md")}; !slices.Equal(project.Docs, want) {
		t.Errorf("Docs = %+v, want %+v", project.Docs, want)
	}
	if repositories[1].Licence != "" || repositories[2].Licence != "" {
		t.Errorf("licences = %q and %q, want both empty", repositories[1].Licence, repositories[2].Licence)
	}
	if len(repositories[3].DotGitHub) != 0 || len(repositories[3].Docs) != 0 {
		t.Errorf("a path that is not a directory yielded entries: %+v", repositories[3])
	}
}

func TestRepositoriesOfAnOwnerWithoutRepositoriesIsEmpty(t *testing.T) {
	client, _ := newFakeGitHub(t, serveBody(http.StatusOK, pageResponse(t, nil, false, "")))

	repositories, err := listRepositories(t, client)

	if err != nil || len(repositories) != 0 {
		t.Errorf("Repositories = %+v, %v; want no repositories and no error", repositories, err)
	}
}

func TestRepositoriesDoesNotRequireContentsOfSkippedRepositories(t *testing.T) {
	cases := map[string]map[string]any{
		"fork":     {"isFork": true},
		"archived": {"isArchived": true},
		"empty":    {"isEmpty": true},
	}
	for name, flag := range cases {
		t.Run(name, func(t *testing.T) {
			overrides := map[string]any{"root": nil, "licenseInfo": nil}
			maps.Copy(overrides, flag)
			client, _ := newFakeGitHub(t, serveBody(http.StatusOK, pageResponse(t, []map[string]any{node("skipped", overrides)}, false, "")))

			repositories, err := listRepositories(t, client)

			if err != nil {
				t.Fatalf("Repositories: %v", err)
			}
			if len(repositories) != 1 || !repositories[0].Skipped() || repositories[0].Name != "skipped" {
				t.Errorf("repositories = %+v, want the one skipped repository", repositories)
			}
		})
	}
}

func TestRepositoriesFailures(t *testing.T) {
	validPage := func(t *testing.T) string { return pageResponse(t, []map[string]any{node("ok", nil)}, false, "") }
	cases := []struct {
		name    string
		respond func(t *testing.T) func(int, http.ResponseWriter)
		want    error
		contain string
	}{
		{
			name: "unauthorised status",
			respond: func(*testing.T) func(int, http.ResponseWriter) {
				return serveBody(http.StatusUnauthorized, `{"message":"Bad credentials"}`)
			},
			want:    conformance.ErrGitHubStatus,
			contain: "401",
		},
		{
			name: "forbidden status keeps the excerpt of the body",
			respond: func(*testing.T) func(int, http.ResponseWriter) {
				return serveBody(http.StatusForbidden, `{"message":"rate limit exceeded"}`)
			},
			want:    conformance.ErrGitHubStatus,
			contain: "rate limit exceeded",
		},
		{
			name: "server error",
			respond: func(*testing.T) func(int, http.ResponseWriter) {
				return serveBody(http.StatusBadGateway, "<html>bad gateway</html>")
			},
			want:    conformance.ErrGitHubStatus,
			contain: "502",
		},
		{
			name: "non-OK success status",
			respond: func(*testing.T) func(int, http.ResponseWriter) {
				return serveBody(http.StatusAccepted, `{"data":null}`)
			},
			want:    conformance.ErrGitHubStatus,
			contain: "202",
		},
		{
			name: "graphql errors alongside data",
			respond: func(t *testing.T) func(int, http.ResponseWriter) {
				body := strings.Replace(validPage(t), `{"data":`, `{"errors":[{"type":"FORBIDDEN","message":"Resource not accessible"}],"data":`, 1)
				return serveBody(http.StatusOK, body)
			},
			want:    conformance.ErrGitHubQuery,
			contain: "FORBIDDEN: Resource not accessible",
		},
		{
			name: "several graphql errors without type",
			respond: func(*testing.T) func(int, http.ResponseWriter) {
				return serveBody(http.StatusOK, `{"data":null,"errors":[{"message":"first"},{"type":"T","message":"second"}]}`)
			},
			want:    conformance.ErrGitHubQuery,
			contain: "first; T: second",
		},
		{
			name: "response without data",
			respond: func(*testing.T) func(int, http.ResponseWriter) {
				return serveBody(http.StatusOK, `{}`)
			},
			want:    conformance.ErrGitHubQuery,
			contain: "no data",
		},
		{
			name: "null data",
			respond: func(*testing.T) func(int, http.ResponseWriter) {
				return serveBody(http.StatusOK, `{"data":null}`)
			},
			want: conformance.ErrGitHubQuery,
		},
		{
			name: "owner does not exist",
			respond: func(*testing.T) func(int, http.ResponseWriter) {
				return serveBody(http.StatusOK, `{"data":{"repositoryOwner":null}}`)
			},
			want:    conformance.ErrOwnerNotFound,
			contain: testOwner,
		},
		{
			name: "checked repository with unreadable default branch",
			respond: func(t *testing.T) func(int, http.ResponseWriter) {
				return serveBody(http.StatusOK, pageResponse(t, []map[string]any{node("hidden", map[string]any{"root": nil})}, false, ""))
			},
			want:    conformance.ErrContentsUnreadable,
			contain: "abrosimov/hidden",
		},
		{
			name: "next page announced without a cursor",
			respond: func(t *testing.T) func(int, http.ResponseWriter) {
				return serveBody(http.StatusOK, pageResponse(t, []map[string]any{node("ok", nil)}, true, ""))
			},
			want: conformance.ErrBrokenPagination,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := newFakeGitHub(t, tc.respond(t))

			repositories, err := listRepositories(t, client)

			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if repositories != nil {
				t.Errorf("repositories = %+v, want none alongside an error", repositories)
			}
			if tc.contain != "" && !strings.Contains(err.Error(), tc.contain) {
				t.Errorf("err = %q, want it to contain %q", err, tc.contain)
			}
			if strings.Contains(err.Error(), testToken) {
				t.Errorf("err = %q leaks the token", err)
			}
		})
	}
}

func TestRepositoriesTruncatesLongErrorBodies(t *testing.T) {
	client, _ := newFakeGitHub(t, serveBody(http.StatusInternalServerError, strings.Repeat("x", 5000)))

	_, err := listRepositories(t, client)

	if !errors.Is(err, conformance.ErrGitHubStatus) {
		t.Fatalf("err = %v, want %v", err, conformance.ErrGitHubStatus)
	}
	if len(err.Error()) > 1000 {
		t.Errorf("error message is %d bytes, want the body excerpted", len(err.Error()))
	}
}

func TestRepositoriesFailureOnALaterPageDiscardsEarlierPages(t *testing.T) {
	client, fake := newFakeGitHub(t, func(index int, w http.ResponseWriter) {
		if index == 0 {
			serveBody(http.StatusOK, pageResponse(t, []map[string]any{node("first", nil)}, true, "cursor-1"))(index, w)
			return
		}
		serveBody(http.StatusOK, pageResponse(t, []map[string]any{node("hidden", map[string]any{"root": nil})}, false, ""))(index, w)
	})

	repositories, err := listRepositories(t, client)

	if !errors.Is(err, conformance.ErrContentsUnreadable) {
		t.Fatalf("err = %v, want %v", err, conformance.ErrContentsUnreadable)
	}
	if repositories != nil {
		t.Errorf("repositories = %+v, want none", repositories)
	}
	if got := len(fake.Requests()); got != 2 {
		t.Errorf("got %d requests, want 2", got)
	}
}

func TestRepositoriesRejectsMalformedJSON(t *testing.T) {
	t.Run("not JSON", func(t *testing.T) {
		client, _ := newFakeGitHub(t, serveBody(http.StatusOK, "this is not json"))

		_, err := listRepositories(t, client)

		if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
			t.Errorf("err = %v, want a *json.SyntaxError", err)
		}
	})

	t.Run("truncated JSON", func(t *testing.T) {
		client, _ := newFakeGitHub(t, serveBody(http.StatusOK, `{"data":{"repositoryOwner":`))

		_, err := listRepositories(t, client)

		if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
			t.Errorf("err = %v, want a *json.SyntaxError", err)
		}
	})

	t.Run("wrong field type", func(t *testing.T) {
		body := `{"data":{"repositoryOwner":{"repositories":{"nodes":[],"pageInfo":{"hasNextPage":"yes"}}}}}`
		client, _ := newFakeGitHub(t, serveBody(http.StatusOK, body))

		_, err := listRepositories(t, client)

		if _, ok := errors.AsType[*json.UnmarshalTypeError](err); !ok {
			t.Errorf("err = %v, want a *json.UnmarshalTypeError", err)
		}
	})
}

func TestRepositoriesBoundsTheResponseSize(t *testing.T) {
	padTo := func(t *testing.T, size int) func(int, http.ResponseWriter) {
		body := pageResponse(t, []map[string]any{node("ok", nil)}, false, "")
		return serveBody(http.StatusOK, body+strings.Repeat(" ", size-len(body)))
	}

	t.Run("exactly at the limit is accepted", func(t *testing.T) {
		client, _ := newFakeGitHub(t, padTo(t, maxResponseBytes))

		repositories, err := listRepositories(t, client)

		if err != nil || len(repositories) != 1 {
			t.Errorf("Repositories = %+v, %v; want one repository and no error", repositories, err)
		}
	})

	t.Run("one byte over the limit is rejected", func(t *testing.T) {
		client, _ := newFakeGitHub(t, padTo(t, maxResponseBytes+1))

		repositories, err := listRepositories(t, client)

		if !errors.Is(err, conformance.ErrResponseTooLarge) {
			t.Errorf("err = %v, want %v", err, conformance.ErrResponseTooLarge)
		}
		if repositories != nil {
			t.Errorf("repositories = %+v, want none", repositories)
		}
	})
}

func TestRepositoriesTransportFailures(t *testing.T) {
	t.Run("server unreachable", func(t *testing.T) {
		server := httptest.NewServer(http.NotFoundHandler())
		client := conformance.NewGitHubClient(server.Client(), server.URL, testToken)
		server.Close()

		_, err := listRepositories(t, client)

		if _, ok := errors.AsType[*url.Error](err); !ok {
			t.Fatalf("err = %v, want a *url.Error", err)
		}
		if strings.Contains(err.Error(), testToken) {
			t.Errorf("err = %q leaks the token", err)
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		client, fake := newFakeGitHub(t, serveBody(http.StatusOK, pageResponse(t, nil, false, "")))
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := client.Repositories(ctx, testOwner)

		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		if got := len(fake.Requests()); got != 0 {
			t.Errorf("got %d requests with a cancelled context, want 0", got)
		}
	})

	t.Run("malformed endpoint", func(t *testing.T) {
		client := conformance.NewGitHubClient(http.DefaultClient, "http://bad host/graphql", testToken)

		_, err := listRepositories(t, client)

		if err == nil {
			t.Fatal("err = nil, want an error")
		}
		if strings.Contains(err.Error(), testToken) {
			t.Errorf("err = %q leaks the token", err)
		}
	})
}
