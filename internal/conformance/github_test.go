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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	"github.com/abrosimov/gh-repo-settings/internal/conformance"
)

const (
	testToken = "test-token-value"

	maxResponseBytes = 32 << 20
	maxExcerptBytes  = 512

	defaultBranch = "main"
	dotGitHubSHA  = "sha-dotgithub"
	docsSHA       = "sha-docs"

	usersPath            = "/users/" + testOwner
	tokenAccountPath     = "/user"
	userReposPath        = "/user/repos"
	orgReposPath         = "/orgs/" + testOwner + "/repos"
	projectRootPath      = "/repos/" + testOwner + "/project/git/trees/" + defaultBranch
	projectDotGitHubPath = "/repos/" + testOwner + "/project/git/trees/" + dotGitHubSHA

	unreachableBaseURL = "https://api.example.test"
)

var (
	errTransport = errors.New("transport broke")
	errBodyRead  = errors.New("body read broke")
	errBodyClose = errors.New("body close broke")
)

type recordedRequest struct {
	Header http.Header
	Query  url.Values
	Method string
	Path   string
}

type handler func(attempt int, w http.ResponseWriter, r *http.Request)

type fakeGitHub struct {
	t        *testing.T
	routes   map[string]handler
	attempts map[string]int
	requests []recordedRequest
	backoffs []int
	mu       sync.Mutex
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.EscapedPath()
	f.mu.Lock()
	f.requests = append(f.requests, recordedRequest{Header: r.Header.Clone(), Query: r.URL.Query(), Method: r.Method, Path: path})
	f.attempts[path]++
	attempt := f.attempts[path]
	route := f.routes[path]
	f.mu.Unlock()
	if route == nil {
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusTeapot)
		return
	}
	route(attempt, w, r)
}

func (f *fakeGitHub) Requests() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

func (f *fakeGitHub) Paths() []string {
	paths := make([]string, 0, len(f.Requests()))
	for _, request := range f.Requests() {
		paths = append(paths, request.Path)
	}
	return paths
}

func (f *fakeGitHub) Count(path string) int {
	count := 0
	for _, requested := range f.Paths() {
		if requested == path {
			count++
		}
	}
	return count
}

func (f *fakeGitHub) Backoffs() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.backoffs)
}

func (f *fakeGitHub) Backoff(retry int) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.backoffs = append(f.backoffs, retry)
	return 0
}

func startFakeGitHub(t *testing.T, routes map[string]handler) (*fakeGitHub, *httptest.Server) {
	t.Helper()
	fake := &fakeGitHub{t: t, routes: routes, attempts: map[string]int{}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	return fake, server
}

func newFakeGitHub(t *testing.T, routes map[string]handler) (*conformance.GitHubClient, *fakeGitHub) {
	t.Helper()
	fake, server := startFakeGitHub(t, routes)
	return conformance.NewGitHubClient(server.Client(), server.URL, testToken, fake.Backoff), fake
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}

func writeJSON(w http.ResponseWriter, value any) {
	if err := json.NewEncoder(w).Encode(value); err != nil {
		return
	}
}

func respond(status int, body string) handler {
	return func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		if _, err := io.WriteString(w, body); err != nil {
			return
		}
	}
}

func respondJSON(value any) handler {
	return func(_ int, w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, value)
	}
}

func sequence(handlers ...handler) handler {
	return func(attempt int, w http.ResponseWriter, r *http.Request) {
		handlers[min(attempt, len(handlers))-1](attempt, w, r)
	}
}

func failThenServe(failures, status int, next handler) handler {
	return func(attempt int, w http.ResponseWriter, r *http.Request) {
		if attempt <= failures {
			respond(status, "bad gateway")(attempt, w, r)
			return
		}
		next(attempt-failures, w, r)
	}
}

func dropConnection(_ int, w http.ResponseWriter, _ *http.Request) {
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	if err := conn.Close(); err != nil {
		return
	}
}

func blockUntilAbandoned(_ int, _ http.ResponseWriter, r *http.Request) {
	<-r.Context().Done()
}

type treeEntryJSON struct {
	Path string `json:"path"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

func blobEntry(path string) treeEntryJSON {
	return treeEntryJSON{Path: path, Type: "blob", SHA: "sha-" + path}
}

func treeEntry(path, sha string) treeEntryJSON {
	return treeEntryJSON{Path: path, Type: "tree", SHA: sha}
}

func commitEntry(path string) treeEntryJSON {
	return treeEntryJSON{Path: path, Type: "commit", SHA: "sha-" + path}
}

func treeJSON(entries ...treeEntryJSON) map[string]any {
	if entries == nil {
		entries = []treeEntryJSON{}
	}
	return map[string]any{"tree": entries}
}

func account(login, accountType string) map[string]any {
	return map[string]any{"login": login, "type": accountType}
}

func repoJSON(name string, overrides map[string]any) map[string]any {
	repo := map[string]any{
		"name":           name,
		"owner":          account(testOwner, "Organization"),
		"fork":           false,
		"archived":       false,
		"license":        map[string]any{"spdx_id": conformance.RequiredLicence},
		"default_branch": defaultBranch,
	}
	maps.Copy(repo, overrides)
	return repo
}

func treeAt(repo, ref string) string {
	return "/repos/" + testOwner + "/" + repo + "/git/trees/" + ref
}

func rootTreeOf(repo string) string {
	return treeAt(repo, defaultBranch)
}

func organisationRoutes(repos ...map[string]any) map[string]handler {
	if repos == nil {
		repos = []map[string]any{}
	}
	return map[string]handler{
		usersPath:    respondJSON(account(testOwner, "Organization")),
		orgReposPath: respondJSON(repos),
	}
}

func userRoutes(tokenLogin string, repos ...map[string]any) map[string]handler {
	if repos == nil {
		repos = []map[string]any{}
	}
	return map[string]handler{
		usersPath:        respondJSON(account(testOwner, "User")),
		tokenAccountPath: respondJSON(account(tokenLogin, "User")),
		userReposPath:    respondJSON(repos),
	}
}

func withRootTrees(routes map[string]handler, names ...string) {
	for _, name := range names {
		routes[rootTreeOf(name)] = respondJSON(treeJSON(blobEntry("README.md")))
	}
}

func withProjectTrees(routes map[string]handler) {
	routes[projectRootPath] = respondJSON(treeJSON(blobEntry("README.md"), treeEntry(".github", dotGitHubSHA)))
	routes[projectDotGitHubPath] = respondJSON(treeJSON(blobEntry("FUNDING.yml")))
}

func projectRoutes() map[string]handler {
	routes := userRoutes(testOwner, repoJSON("project", nil))
	withProjectTrees(routes)
	return routes
}

func listRepositories(t *testing.T, client *conformance.GitHubClient) ([]conformance.Repository, error) {
	t.Helper()
	return client.Repositories(t.Context(), testOwner)
}

func repoNames(repositories []conformance.Repository) []string {
	names := make([]string, 0, len(repositories))
	for i := range repositories {
		names = append(names, repositories[i].Name)
	}
	return names
}

func sameRepository(got, want conformance.Repository) bool {
	return got.Owner == want.Owner && got.Name == want.Name && got.Licence == want.Licence &&
		got.Fork == want.Fork && got.Archived == want.Archived && got.Empty == want.Empty &&
		slices.Equal(got.Root, want.Root) && slices.Equal(got.DotGitHub, want.DotGitHub) && slices.Equal(got.Docs, want.Docs)
}

func requireProject(t *testing.T, repositories []conformance.Repository) {
	t.Helper()
	want := conformance.Repository{
		Owner:     testOwner,
		Name:      "project",
		Licence:   conformance.RequiredLicence,
		Root:      []conformance.Entry{file("README.md"), directory(".github")},
		DotGitHub: []conformance.Entry{file("FUNDING.yml")},
	}
	if len(repositories) != 1 || !sameRepository(repositories[0], want) {
		t.Fatalf("repositories = %+v, want [%+v]", repositories, want)
	}
}

func requireFailure(t *testing.T, repositories []conformance.Repository, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if repositories != nil {
		t.Errorf("repositories = %+v, want none alongside an error", repositories)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("err = %q leaks the token", err)
	}
}

func requirePaths(t *testing.T, fake *fakeGitHub, want ...string) {
	t.Helper()
	if got := fake.Paths(); !slices.Equal(got, want) {
		t.Errorf("requested paths = %v, want %v", got, want)
	}
}

func requireBackoffs(t *testing.T, fake *fakeGitHub, want ...int) {
	t.Helper()
	if got := fake.Backoffs(); !slices.Equal(got, want) {
		t.Errorf("Backoff called with %v, want %v", got, want)
	}
}

func requireAuthorisedGets(t *testing.T, requests []recordedRequest) {
	t.Helper()
	want := map[string]string{
		"Authorization":        "Bearer " + testToken,
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
	}
	for i, request := range requests {
		if request.Method != http.MethodGet {
			t.Errorf("request %d (%s) method = %s, want GET", i, request.Path, request.Method)
		}
		for name, value := range want {
			if got := request.Header.Get(name); got != value {
				t.Errorf("request %d (%s) %s = %q, want %q", i, request.Path, name, got, value)
			}
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type faultyBody struct {
	reader   io.Reader
	closeErr error
}

func (b *faultyBody) Read(p []byte) (int, error) { return b.reader.Read(p) }

func (b *faultyBody) Close() error { return b.closeErr }

func staticResponse(r *http.Request, status int, body io.ReadCloser) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: body, Request: r}
}

type transportFault struct {
	do   func(r *http.Request) (*http.Response, error)
	want error
	name string
}

func transportFaults() []transportFault {
	return []transportFault{
		{
			name: "the request fails",
			want: errTransport,
			do:   func(*http.Request) (*http.Response, error) { return nil, errTransport },
		},
		{
			name: "the body cannot be read",
			want: errBodyRead,
			do: func(r *http.Request) (*http.Response, error) {
				body := &faultyBody{reader: io.MultiReader(strings.NewReader(`{"login":`), iotest.ErrReader(errBodyRead))}
				return staticResponse(r, http.StatusOK, body), nil
			},
		},
		{
			name: "the body cannot be closed",
			want: errBodyClose,
			do: func(r *http.Request) (*http.Response, error) {
				body := &faultyBody{reader: strings.NewReader(`{"login":"abrosimov","type":"User"}`), closeErr: errBodyClose}
				return staticResponse(r, http.StatusOK, body), nil
			},
		},
	}
}

func newFaultyClient(server *httptest.Server, fault transportFault, failures int) (*http.Client, *atomic.Int32) {
	var calls atomic.Int32
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if int(calls.Add(1)) <= failures {
			return fault.do(r)
		}
		return server.Client().Transport.RoundTrip(r)
	})
	return &http.Client{Transport: transport}, &calls
}

func newFreshConnectionClient() *http.Client {
	return &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
}

func isSyntaxError(err error) bool {
	_, ok := errors.AsType[*json.SyntaxError](err)
	return ok
}

func isTypeError(err error) bool {
	_, ok := errors.AsType[*json.UnmarshalTypeError](err)
	return ok
}

func TestRepositoriesOfAnOrganisationAreListedWithoutAskingForTheTokensAccount(t *testing.T) {
	routes := organisationRoutes(repoJSON("alpha", nil), repoJSON("beta", nil))
	withRootTrees(routes, "alpha", "beta")
	client, fake := newFakeGitHub(t, routes)

	repositories, err := listRepositories(t, client)

	if err != nil {
		t.Fatalf("Repositories: %v", err)
	}
	if got := repoNames(repositories); !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Errorf("repositories = %v, want [alpha beta]", got)
	}
	requirePaths(t, fake, usersPath, orgReposPath, rootTreeOf("alpha"), rootTreeOf("beta"))
	wantQuery := url.Values{"type": {"all"}, "per_page": {"100"}, "sort": {"full_name"}}
	if got := fake.Requests()[1].Query; got.Encode() != wantQuery.Encode() {
		t.Errorf("listing query = %q, want %q", got.Encode(), wantQuery.Encode())
	}
}

func TestRepositoriesOfTheTokensOwnAccountKeepOnlyThoseItOwns(t *testing.T) {
	routes := userRoutes("Abrosimov",
		repoJSON("mine", map[string]any{"owner": account(testOwner, "User")}),
		repoJSON("shared", map[string]any{"owner": account("someone-else", "User")}),
		repoJSON("Cased", map[string]any{"owner": account("ABROSIMOV", "User")}),
	)
	withRootTrees(routes, "mine", "Cased")
	client, fake := newFakeGitHub(t, routes)

	repositories, err := listRepositories(t, client)

	if err != nil {
		t.Fatalf("Repositories: %v", err)
	}
	if got := repoNames(repositories); !slices.Equal(got, []string{"mine", "Cased"}) {
		t.Errorf("repositories = %v, want [mine Cased]: the foreign-owned one dropped, the case difference kept", got)
	}
	for i := range repositories {
		if repositories[i].Owner != testOwner {
			t.Errorf("repository %s owner = %q, want %q", repositories[i].Name, repositories[i].Owner, testOwner)
		}
	}
	requirePaths(t, fake, usersPath, tokenAccountPath, userReposPath, rootTreeOf("mine"), rootTreeOf("Cased"))
	wantQuery := url.Values{"affiliation": {"owner"}, "visibility": {"all"}, "per_page": {"100"}, "sort": {"full_name"}}
	if got := fake.Requests()[2].Query; got.Encode() != wantQuery.Encode() {
		t.Errorf("listing query = %q, want %q", got.Encode(), wantQuery.Encode())
	}
}

func TestRepositoriesOfAnotherUserFailWithoutListingAnything(t *testing.T) {
	client, fake := newFakeGitHub(t, userRoutes("token-owner", repoJSON("private", nil)))

	repositories, err := listRepositories(t, client)

	requireFailure(t, repositories, err, conformance.ErrUserNotAuthenticated)
	want := "listing repositories of " + testOwner + ": " + conformance.ErrUserNotAuthenticated.Error() +
		" (the token belongs to token-owner)"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
	requirePaths(t, fake, usersPath, tokenAccountPath)
}

func TestRepositoriesOfAnUnknownOwnerFailWithOwnerNotFound(t *testing.T) {
	client, fake := newFakeGitHub(t, map[string]handler{usersPath: respond(http.StatusNotFound, `{"message":"Not Found"}`)})

	repositories, err := listRepositories(t, client)

	requireFailure(t, repositories, err, conformance.ErrOwnerNotFound)
	if want := conformance.ErrOwnerNotFound.Error() + ": " + testOwner; err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
	if errors.Is(err, conformance.ErrGitHubStatus) {
		t.Errorf("err = %v, want it not to be reported as an unexpected status", err)
	}
	requirePaths(t, fake, usersPath)
}

func TestRepositoriesOfAnOwnerWithoutRepositoriesIsEmpty(t *testing.T) {
	client, fake := newFakeGitHub(t, organisationRoutes())

	repositories, err := listRepositories(t, client)

	if err != nil || len(repositories) != 0 {
		t.Errorf("Repositories = %+v, %v; want no repositories and no error", repositories, err)
	}
	requirePaths(t, fake, usersPath, orgReposPath)
}

func TestRepositoriesFollowsNextLinksThroughEveryPageInOrder(t *testing.T) {
	routes := organisationRoutes()
	routes[orgReposPath] = func(_ int, w http.ResponseWriter, r *http.Request) {
		link := func(page, relation string) string {
			return "<" + serverURL(r) + orgReposPath + "?per_page=100&sort=full_name&type=all&page=" + page + `>; rel="` + relation + `"`
		}
		switch r.URL.Query().Get("page") {
		case "":
			w.Header().Set("Link", link("2", "next")+", "+link("3", "last"))
			writeJSON(w, []map[string]any{repoJSON("first", nil)})
		case "2":
			w.Header().Set("Link", strings.Join([]string{link("1", "prev"), link("3", "next"), link("1", "first"), link("3", "last")}, ", "))
			writeJSON(w, []map[string]any{repoJSON("second", nil)})
		default:
			w.Header().Set("Link", link("2", "prev")+", "+link("1", "first"))
			writeJSON(w, []map[string]any{repoJSON("third", nil)})
		}
	}
	withRootTrees(routes, "first", "second", "third")
	client, fake := newFakeGitHub(t, routes)

	repositories, err := listRepositories(t, client)

	if err != nil {
		t.Fatalf("Repositories: %v", err)
	}
	if got := repoNames(repositories); !slices.Equal(got, []string{"first", "second", "third"}) {
		t.Errorf("repositories = %v, want [first second third]", got)
	}
	var pages []string
	for _, request := range fake.Requests() {
		if request.Path == orgReposPath {
			pages = append(pages, request.Query.Get("page"))
		}
	}
	if !slices.Equal(pages, []string{"", "2", "3"}) {
		t.Errorf("pages requested = %q, want the first page and then the links to 2 and 3", pages)
	}
	requirePaths(t, fake, usersPath, orgReposPath, orgReposPath, orgReposPath,
		rootTreeOf("first"), rootTreeOf("second"), rootTreeOf("third"))
}

func TestRepositoriesRejectsANextLinkItCannotFollow(t *testing.T) {
	var foreignRequests atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { foreignRequests.Add(1) }))
	t.Cleanup(foreign.Close)
	cases := []struct {
		target func(r *http.Request) string
		name   string
	}{
		{name: "another host", target: func(*http.Request) string { return foreign.URL + orgReposPath + "?page=2" }},
		{name: "another scheme", target: func(r *http.Request) string { return "https://" + r.Host + orgReposPath + "?page=2" }},
		{name: "unparseable target", target: func(*http.Request) string { return "%zz" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			routes := organisationRoutes()
			routes[orgReposPath] = func(_ int, w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Link", "<"+tc.target(r)+`>; rel="next"`)
				writeJSON(w, []map[string]any{repoJSON("ok", nil)})
			}
			client, fake := newFakeGitHub(t, routes)

			repositories, err := listRepositories(t, client)

			requireFailure(t, repositories, err, conformance.ErrBrokenPagination)
			if prefix := "listing repositories of " + testOwner + ": "; !strings.HasPrefix(err.Error(), prefix) {
				t.Errorf("err = %q, want it to start with %q", err, prefix)
			}
			requirePaths(t, fake, usersPath, orgReposPath)
			if got := foreignRequests.Load(); got != 0 {
				t.Errorf("the other host received %d requests carrying the token, want 0", got)
			}
		})
	}
}

func TestRepositoriesFailureOnALaterPageDiscardsEarlierPages(t *testing.T) {
	routes := organisationRoutes()
	routes[orgReposPath] = func(attempt int, w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "" {
			respond(http.StatusInternalServerError, "boom")(attempt, w, r)
			return
		}
		w.Header().Set("Link", "<"+serverURL(r)+orgReposPath+`?page=2>; rel="next"`)
		writeJSON(w, []map[string]any{repoJSON("first", nil)})
	}
	client, fake := newFakeGitHub(t, routes)

	repositories, err := listRepositories(t, client)

	requireFailure(t, repositories, err, conformance.ErrGitHubStatus)
	want := "listing repositories of " + testOwner + ": " + conformance.ErrGitHubStatus.Error() + ": 500 boom"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
	requirePaths(t, fake, usersPath, orgReposPath, orgReposPath)
}

func TestRepositoriesMapsTheLicenceOfEachRepository(t *testing.T) {
	routes := organisationRoutes(
		repoJSON("mit", map[string]any{"license": map[string]any{"spdx_id": "MIT"}}),
		repoJSON("unlicensed", map[string]any{"license": nil}),
		repoJSON("null-spdx", map[string]any{"license": map[string]any{"spdx_id": nil}}),
	)
	withRootTrees(routes, "mit", "unlicensed", "null-spdx")
	client, _ := newFakeGitHub(t, routes)

	repositories, err := listRepositories(t, client)

	if err != nil {
		t.Fatalf("Repositories: %v", err)
	}
	if len(repositories) != 3 {
		t.Fatalf("got %d repositories, want 3: %+v", len(repositories), repositories)
	}
	for i, want := range []string{"MIT", "", ""} {
		if repositories[i].Licence != want {
			t.Errorf("%s licence = %q, want %q", repositories[i].Name, repositories[i].Licence, want)
		}
	}
}

func TestRepositoriesDoesNotReadTheTreesOfForksAndArchivedRepositories(t *testing.T) {
	cases := map[string]map[string]any{
		"fork":               {"fork": true},
		"archived":           {"archived": true},
		"archived fork":      {"fork": true, "archived": true},
		"licence still read": {"fork": true, "license": map[string]any{"spdx_id": "MIT"}},
	}
	for name, overrides := range cases {
		t.Run(name, func(t *testing.T) {
			client, fake := newFakeGitHub(t, organisationRoutes(repoJSON("skipped", overrides)))

			repositories, err := listRepositories(t, client)

			if err != nil {
				t.Fatalf("Repositories: %v", err)
			}
			if len(repositories) != 1 || !repositories[0].Skipped() || repositories[0].Name != "skipped" {
				t.Fatalf("repositories = %+v, want the one skipped repository", repositories)
			}
			if got, want := repositories[0].Fork, overrides["fork"] == true; got != want {
				t.Errorf("Fork = %t, want %t", got, want)
			}
			if got, want := repositories[0].Archived, overrides["archived"] == true; got != want {
				t.Errorf("Archived = %t, want %t", got, want)
			}
			if repositories[0].Root != nil || repositories[0].DotGitHub != nil || repositories[0].Docs != nil {
				t.Errorf("a skipped repository carries trees: %+v", repositories[0])
			}
			requirePaths(t, fake, usersPath, orgReposPath)
		})
	}
}

func TestRepositoriesTakesAnEmptyRepositoryAsEmptyAndGoesOn(t *testing.T) {
	routes := organisationRoutes(repoJSON("empty", nil), repoJSON("full", nil))
	routes[rootTreeOf("empty")] = respond(http.StatusConflict, `{"message":"Git Repository is empty."}`)
	withRootTrees(routes, "full")
	client, fake := newFakeGitHub(t, routes)

	repositories, err := listRepositories(t, client)

	if err != nil {
		t.Fatalf("Repositories: %v", err)
	}
	if got := repoNames(repositories); !slices.Equal(got, []string{"empty", "full"}) {
		t.Fatalf("repositories = %v, want [empty full]", got)
	}
	empty := repositories[0]
	if !empty.Empty || !empty.Skipped() {
		t.Errorf("empty repository = %+v, want Empty and skipped", empty)
	}
	if empty.Root != nil || empty.DotGitHub != nil || empty.Docs != nil {
		t.Errorf("empty repository carries trees: %+v", empty)
	}
	if want := []conformance.Entry{file("README.md")}; !slices.Equal(repositories[1].Root, want) {
		t.Errorf("full repository Root = %+v, want %+v", repositories[1].Root, want)
	}
	requirePaths(t, fake, usersPath, orgReposPath, rootTreeOf("empty"), rootTreeOf("full"))
}

func TestRepositoriesReportsAnUnreadableTreeWhereverItIsRefused(t *testing.T) {
	trees := []struct {
		name string
		path string
	}{
		{"root tree", rootTreeOf("hidden")},
		{".github tree", treeAt("hidden", dotGitHubSHA)},
		{"docs tree", treeAt("hidden", docsSHA)},
	}
	for _, tree := range trees {
		for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
			t.Run(tree.name+" "+strconv.Itoa(status), func(t *testing.T) {
				routes := organisationRoutes(repoJSON("first", nil), repoJSON("hidden", nil))
				withRootTrees(routes, "first")
				routes[rootTreeOf("hidden")] = respondJSON(treeJSON(treeEntry(".github", dotGitHubSHA), treeEntry("docs", docsSHA)))
				routes[treeAt("hidden", dotGitHubSHA)] = respondJSON(treeJSON())
				routes[treeAt("hidden", docsSHA)] = respondJSON(treeJSON())
				routes[tree.path] = respond(status, `{"message":"refused"}`)
				client, fake := newFakeGitHub(t, routes)

				repositories, err := listRepositories(t, client)

				requireFailure(t, repositories, err, conformance.ErrContentsUnreadable)
				if want := testOwner + "/hidden: " + conformance.ErrContentsUnreadable.Error(); err.Error() != want {
					t.Errorf("err = %q, want %q", err, want)
				}
				if errors.Is(err, conformance.ErrGitHubStatus) {
					t.Errorf("err = %v, want it not to be reported as an unexpected status", err)
				}
				if got := fake.Count(tree.path); got != 1 {
					t.Errorf("%s requested %d times, want 1", tree.path, got)
				}
				requireBackoffs(t, fake)
			})
		}
	}
}

func TestRepositoriesReadsTheGitHubAndDocsTreesBySHAAndMapsEntries(t *testing.T) {
	routes := organisationRoutes(repoJSON("project", nil))
	routes[rootTreeOf("project")] = respondJSON(treeJSON(
		blobEntry("CONTRIBUTING.md"),
		treeEntry("src", "sha-src"),
		commitEntry("vendored"),
		treeEntryJSON{Path: "mystery", Type: "tag", SHA: "sha-mystery"},
		blobEntry("zeta.txt"),
		treeEntry(".github", dotGitHubSHA),
		treeEntry("docs", docsSHA),
	))
	routes[treeAt("project", dotGitHubSHA)] = respondJSON(treeJSON(
		treeEntry("ISSUE_TEMPLATE", "sha-issue-template"), blobEntry("FUNDING.yml"), commitEntry("nested-submodule"),
	))
	routes[treeAt("project", docsSHA)] = respondJSON(treeJSON(blobEntry("SECURITY.md")))
	client, fake := newFakeGitHub(t, routes)

	repositories, err := listRepositories(t, client)

	if err != nil {
		t.Fatalf("Repositories: %v", err)
	}
	want := conformance.Repository{
		Owner:   testOwner,
		Name:    "project",
		Licence: conformance.RequiredLicence,
		Root: []conformance.Entry{
			file("CONTRIBUTING.md"), directory("src"), file("zeta.txt"), directory(".github"), directory("docs"),
		},
		DotGitHub: []conformance.Entry{directory("ISSUE_TEMPLATE"), file("FUNDING.yml")},
		Docs:      []conformance.Entry{file("SECURITY.md")},
	}
	if len(repositories) != 1 || !sameRepository(repositories[0], want) {
		t.Errorf("repositories = %+v, want [%+v]: submodules and unknown types ignored, order kept", repositories, want)
	}
	requirePaths(t, fake, usersPath, orgReposPath,
		rootTreeOf("project"), treeAt("project", dotGitHubSHA), treeAt("project", docsSHA))
}

func TestRepositoriesDoesNotReadSubtreesThatAreAbsentOrNotTrees(t *testing.T) {
	cases := []struct {
		root          map[string]any
		name          string
		wantDotGitHub bool
	}{
		{name: "neither exists", root: treeJSON(blobEntry("README.md"))},
		{name: "both are files", root: treeJSON(blobEntry(".github"), blobEntry("docs"))},
		{name: "both are submodules", root: treeJSON(commitEntry(".github"), commitEntry("docs"))},
		{name: "only .github exists", root: treeJSON(treeEntry(".github", dotGitHubSHA)), wantDotGitHub: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			routes := organisationRoutes(repoJSON("project", nil))
			routes[rootTreeOf("project")] = respondJSON(tc.root)
			routes[treeAt("project", dotGitHubSHA)] = respondJSON(treeJSON(blobEntry("FUNDING.yml")))
			client, fake := newFakeGitHub(t, routes)

			repositories, err := listRepositories(t, client)

			if err != nil {
				t.Fatalf("Repositories: %v", err)
			}
			if len(repositories) != 1 {
				t.Fatalf("got %d repositories, want 1", len(repositories))
			}
			if repositories[0].Docs != nil {
				t.Errorf("Docs = %+v, want nil", repositories[0].Docs)
			}
			if tc.wantDotGitHub != (repositories[0].DotGitHub != nil) {
				t.Errorf("DotGitHub = %+v, want it read: %t", repositories[0].DotGitHub, tc.wantDotGitHub)
			}
			wantPaths := []string{usersPath, orgReposPath, rootTreeOf("project")}
			if tc.wantDotGitHub {
				wantPaths = append(wantPaths, treeAt("project", dotGitHubSHA))
			}
			requirePaths(t, fake, wantPaths...)
		})
	}
}

func TestRepositoriesEscapesEachSegmentOfADefaultBranch(t *testing.T) {
	routes := organisationRoutes(repoJSON("project", map[string]any{"default_branch": "release/1.0 beta/q?x#y%"}))
	routes[treeAt("project", "release/1.0%20beta/q%3Fx%23y%25")] = respondJSON(treeJSON())
	client, fake := newFakeGitHub(t, routes)

	repositories, err := listRepositories(t, client)

	if err != nil || len(repositories) != 1 {
		t.Fatalf("Repositories = %+v, %v; want one repository and no error", repositories, err)
	}
	requirePaths(t, fake, usersPath, orgReposPath, treeAt("project", "release/1.0%20beta/q%3Fx%23y%25"))
}

func TestRepositoriesSendsEveryRequestAsAnAuthorisedGet(t *testing.T) {
	t.Run("user owner", func(t *testing.T) {
		client, fake := newFakeGitHub(t, projectRoutes())

		repositories, err := listRepositories(t, client)

		if err != nil {
			t.Fatalf("Repositories: %v", err)
		}
		requireProject(t, repositories)
		requirePaths(t, fake, usersPath, tokenAccountPath, userReposPath, projectRootPath, projectDotGitHubPath)
		requireAuthorisedGets(t, fake.Requests())
	})

	t.Run("organisation owner", func(t *testing.T) {
		routes := organisationRoutes(repoJSON("project", nil))
		withProjectTrees(routes)
		client, fake := newFakeGitHub(t, routes)

		if _, err := listRepositories(t, client); err != nil {
			t.Fatalf("Repositories: %v", err)
		}

		requirePaths(t, fake, usersPath, orgReposPath, projectRootPath, projectDotGitHubPath)
		requireAuthorisedGets(t, fake.Requests())
	})
}

func TestNewGitHubClientTrimsATrailingSlashFromTheBaseURL(t *testing.T) {
	fake, server := startFakeGitHub(t, projectRoutes())
	client := conformance.NewGitHubClient(server.Client(), server.URL+"/", testToken, fake.Backoff)

	repositories, err := listRepositories(t, client)

	if err != nil {
		t.Fatalf("Repositories: %v", err)
	}
	requireProject(t, repositories)
	requirePaths(t, fake, usersPath, tokenAccountPath, userReposPath, projectRootPath, projectDotGitHubPath)
}

func TestRepositoriesRetriesATransientStatusOnEveryKindOfRequest(t *testing.T) {
	paths := []string{usersPath, tokenAccountPath, userReposPath, projectRootPath, projectDotGitHubPath}
	statuses := []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout}
	for _, path := range paths {
		for _, status := range statuses {
			t.Run(strconv.Itoa(status)+" on "+path, func(t *testing.T) {
				routes := projectRoutes()
				routes[path] = failThenServe(1, status, routes[path])
				client, fake := newFakeGitHub(t, routes)

				repositories, err := listRepositories(t, client)

				if err != nil {
					t.Fatalf("Repositories: %v", err)
				}
				requireProject(t, repositories)
				if got := fake.Count(path); got != 2 {
					t.Errorf("%s requested %d times, want 2", path, got)
				}
				requireBackoffs(t, fake, 1)
			})
		}
	}
}

func TestRepositoriesGivesEveryRequestItsOwnRetryBudget(t *testing.T) {
	routes := projectRoutes()
	paths := []string{usersPath, tokenAccountPath, userReposPath, projectRootPath, projectDotGitHubPath}
	for _, path := range paths {
		routes[path] = failThenServe(2, http.StatusBadGateway, routes[path])
	}
	client, fake := newFakeGitHub(t, routes)

	repositories, err := listRepositories(t, client)

	if err != nil {
		t.Fatalf("Repositories: %v", err)
	}
	requireProject(t, repositories)
	for _, path := range paths {
		if got := fake.Count(path); got != 3 {
			t.Errorf("%s requested %d times, want 3", path, got)
		}
	}
	requireBackoffs(t, fake, 1, 2, 1, 2, 1, 2, 1, 2, 1, 2)
}

func TestRepositoriesGivesUpAfterThreeAttemptsAndReportsTheLastStatus(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		prefix string
		status int
	}{
		{"owner lookup", usersPath, "looking up " + testOwner, http.StatusBadGateway},
		{"token account lookup", tokenAccountPath, "looking up the token's account", http.StatusServiceUnavailable},
		{"listing page", userReposPath, "listing repositories of " + testOwner, http.StatusGatewayTimeout},
		{"root tree", projectRootPath, "reading the tree main of " + testOwner + "/project", http.StatusBadGateway},
		{"subtree", projectDotGitHubPath, "reading the tree " + dotGitHubSHA + " of " + testOwner + "/project", http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			routes := projectRoutes()
			routes[tc.path] = respond(tc.status, "  upstream timed out\n")
			client, fake := newFakeGitHub(t, routes)

			repositories, err := listRepositories(t, client)

			requireFailure(t, repositories, err, conformance.ErrGitHubStatus)
			want := tc.prefix + ": " + conformance.ErrGitHubStatus.Error() + ": " + strconv.Itoa(tc.status) + " upstream timed out"
			if err.Error() != want {
				t.Errorf("err = %q, want %q", err, want)
			}
			if got := fake.Count(tc.path); got != 3 {
				t.Errorf("%s requested %d times, want 3", tc.path, got)
			}
			requireBackoffs(t, fake, 1, 2)
		})
	}
}

func TestRepositoriesAttemptsOtherFailuresOnce(t *testing.T) {
	cases := []struct {
		want   error
		path   string
		status int
	}{
		{conformance.ErrGitHubStatus, usersPath, http.StatusInternalServerError},
		{conformance.ErrGitHubStatus, usersPath, http.StatusUnauthorized},
		{conformance.ErrOwnerNotFound, usersPath, http.StatusNotFound},
		{conformance.ErrGitHubStatus, tokenAccountPath, http.StatusUnauthorized},
		{conformance.ErrGitHubStatus, tokenAccountPath, http.StatusForbidden},
		{conformance.ErrGitHubStatus, userReposPath, http.StatusInternalServerError},
		{conformance.ErrGitHubStatus, userReposPath, http.StatusUnauthorized},
		{conformance.ErrGitHubStatus, userReposPath, http.StatusNotFound},
		{conformance.ErrGitHubStatus, projectRootPath, http.StatusInternalServerError},
		{conformance.ErrContentsUnreadable, projectRootPath, http.StatusForbidden},
		{conformance.ErrContentsUnreadable, projectRootPath, http.StatusNotFound},
		{conformance.ErrGitHubStatus, projectDotGitHubPath, http.StatusInternalServerError},
		{conformance.ErrContentsUnreadable, projectDotGitHubPath, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(strconv.Itoa(tc.status)+" on "+tc.path, func(t *testing.T) {
			routes := projectRoutes()
			routes[tc.path] = respond(tc.status, `{"message":"no"}`)
			client, fake := newFakeGitHub(t, routes)

			repositories, err := listRepositories(t, client)

			requireFailure(t, repositories, err, tc.want)
			if got := fake.Count(tc.path); got != 1 {
				t.Errorf("%s requested %d times, want 1", tc.path, got)
			}
			requireBackoffs(t, fake)
		})
	}
}

func TestRepositoriesRetriesAConnectionDroppedByTheServer(t *testing.T) {
	routes := projectRoutes()
	routes[usersPath] = sequence(dropConnection, routes[usersPath])
	fake, server := startFakeGitHub(t, routes)
	client := conformance.NewGitHubClient(newFreshConnectionClient(), server.URL, testToken, fake.Backoff)

	repositories, err := listRepositories(t, client)

	if err != nil {
		t.Fatalf("Repositories: %v", err)
	}
	requireProject(t, repositories)
	if got := fake.Count(usersPath); got != 2 {
		t.Errorf("%s requested %d times, want 2", usersPath, got)
	}
	requireBackoffs(t, fake, 1)
}

func TestRepositoriesGivesUpOnAServerThatKeepsDroppingTheConnection(t *testing.T) {
	routes := projectRoutes()
	routes[usersPath] = dropConnection
	fake, server := startFakeGitHub(t, routes)
	client := conformance.NewGitHubClient(newFreshConnectionClient(), server.URL, testToken, fake.Backoff)

	repositories, err := listRepositories(t, client)

	if _, ok := errors.AsType[*url.Error](err); !ok {
		t.Fatalf("err = %v, want a *url.Error", err)
	}
	if repositories != nil || strings.Contains(err.Error(), testToken) {
		t.Errorf("repositories = %+v, err = %q; want none and no token", repositories, err)
	}
	if got := fake.Count(usersPath); got != 3 {
		t.Errorf("%s requested %d times, want 3", usersPath, got)
	}
	requireBackoffs(t, fake, 1, 2)
}

func TestRepositoriesRetriesARequestThatTimesOut(t *testing.T) {
	routes := projectRoutes()
	routes[usersPath] = sequence(blockUntilAbandoned, routes[usersPath])
	fake, server := startFakeGitHub(t, routes)
	httpClient := &http.Client{Transport: server.Client().Transport, Timeout: 250 * time.Millisecond}
	client := conformance.NewGitHubClient(httpClient, server.URL, testToken, fake.Backoff)

	repositories, err := listRepositories(t, client)

	if err != nil {
		t.Fatalf("Repositories: %v", err)
	}
	requireProject(t, repositories)
	requireBackoffs(t, fake, 1)
}

func TestRepositoriesRetriesATransportFailureOfAnyKind(t *testing.T) {
	for _, fault := range transportFaults() {
		t.Run(fault.name, func(t *testing.T) {
			fake, server := startFakeGitHub(t, projectRoutes())
			httpClient, _ := newFaultyClient(server, fault, 1)
			client := conformance.NewGitHubClient(httpClient, server.URL, testToken, fake.Backoff)

			repositories, err := listRepositories(t, client)

			if err != nil {
				t.Fatalf("Repositories: %v", err)
			}
			requireProject(t, repositories)
			requireBackoffs(t, fake, 1)
		})
	}
}

func TestRepositoriesGivesUpAfterThreeTransportFailures(t *testing.T) {
	for _, fault := range transportFaults() {
		t.Run(fault.name, func(t *testing.T) {
			fake, server := startFakeGitHub(t, nil)
			httpClient, calls := newFaultyClient(server, fault, 3)
			client := conformance.NewGitHubClient(httpClient, server.URL, testToken, fake.Backoff)

			repositories, err := listRepositories(t, client)

			requireFailure(t, repositories, err, fault.want)
			if prefix := "looking up " + testOwner + ": "; !strings.HasPrefix(err.Error(), prefix) {
				t.Errorf("err = %q, want it to start with %q", err, prefix)
			}
			if got := calls.Load(); got != 3 {
				t.Errorf("transport called %d times, want 3", got)
			}
			requireBackoffs(t, fake, 1, 2)
		})
	}
}

func TestRepositoriesRetriesAnUnreachableServerThreeTimes(t *testing.T) {
	fake, server := startFakeGitHub(t, nil)
	client := conformance.NewGitHubClient(server.Client(), server.URL, testToken, fake.Backoff)
	server.Close()

	repositories, err := listRepositories(t, client)

	if _, ok := errors.AsType[*url.Error](err); !ok {
		t.Fatalf("err = %v, want a *url.Error", err)
	}
	if repositories != nil || strings.Contains(err.Error(), testToken) {
		t.Errorf("repositories = %+v, err = %q; want none and no token", repositories, err)
	}
	requireBackoffs(t, fake, 1, 2)
}

func TestRepositoriesDoesNotRetryARequestThatCannotBeBuilt(t *testing.T) {
	var retries []int
	backoff := func(retry int) time.Duration {
		retries = append(retries, retry)
		return 0
	}
	client := conformance.NewGitHubClient(http.DefaultClient, "http://bad host", testToken, backoff)

	repositories, err := listRepositories(t, client)

	if err == nil {
		t.Fatal("err = nil, want an error")
	}
	if repositories != nil || strings.Contains(err.Error(), testToken) {
		t.Errorf("repositories = %+v, err = %q; want none and no token", repositories, err)
	}
	if len(retries) != 0 {
		t.Errorf("Backoff called with %v, want no retries: a malformed base URL does not heal", retries)
	}
}

func TestRepositoriesStopsWaitingToRetryWhenTheContextIsCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls int
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			return staticResponse(r, http.StatusBadGateway, io.NopCloser(strings.NewReader("bad gateway"))), nil
		})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var retries []int
		backoff := func(retry int) time.Duration {
			retries = append(retries, retry)
			cancel()
			return time.Hour
		}
		client := conformance.NewGitHubClient(&http.Client{Transport: transport}, unreachableBaseURL, testToken, backoff)

		repositories, err := client.Repositories(ctx, testOwner)

		requireFailure(t, repositories, err, context.Canceled)
		want := "looking up " + testOwner + ": waiting to retry the GitHub API: context canceled"
		if err.Error() != want {
			t.Errorf("err = %q, want %q", err, want)
		}
		if calls != 1 || !slices.Equal(retries, []int{1}) {
			t.Errorf("requests = %d, backoffs = %v; want 1 request and a single backoff for retry 1", calls, retries)
		}
	})
}

func TestRepositoriesDoesNotRetryOnceTheContextIsDone(t *testing.T) {
	fake, server := startFakeGitHub(t, projectRoutes())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls atomic.Int32
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		cancel()
		return nil, errTransport
	})
	client := conformance.NewGitHubClient(&http.Client{Transport: transport}, server.URL, testToken, fake.Backoff)

	repositories, err := client.Repositories(ctx, testOwner)

	requireFailure(t, repositories, err, errTransport)
	if got := calls.Load(); got != 1 {
		t.Errorf("transport called %d times, want 1", got)
	}
	requireBackoffs(t, fake)
}

func TestRepositoriesWithACancelledContextMakeNoRequest(t *testing.T) {
	client, fake := newFakeGitHub(t, projectRoutes())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	repositories, err := client.Repositories(ctx, testOwner)

	requireFailure(t, repositories, err, context.Canceled)
	requirePaths(t, fake)
	requireBackoffs(t, fake)
}

func TestNewGitHubClientWithoutABackoffWaitsOneSecondThenTwo(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return staticResponse(r, http.StatusBadGateway, io.NopCloser(strings.NewReader("bad gateway"))), nil
		})
		client := conformance.NewGitHubClient(&http.Client{Transport: transport}, unreachableBaseURL, testToken, nil)
		start := time.Now()

		_, err := listRepositories(t, client)

		if !errors.Is(err, conformance.ErrGitHubStatus) {
			t.Fatalf("err = %v, want %v", err, conformance.ErrGitHubStatus)
		}
		if got := time.Since(start); got != 3*time.Second {
			t.Errorf("waited %s over three attempts, want 3s (1s, then 2s)", got)
		}
	})
}

func TestRepositoriesBoundsTheResponseSize(t *testing.T) {
	padded := func(body string, size int) string { return body + strings.Repeat(" ", size-len(body)) }
	wantPrefix := "listing repositories of " + testOwner + ": "

	t.Run("exactly at the limit is accepted", func(t *testing.T) {
		routes := organisationRoutes()
		routes[orgReposPath] = respond(http.StatusOK, padded("[]", maxResponseBytes))
		client, _ := newFakeGitHub(t, routes)

		repositories, err := listRepositories(t, client)

		if err != nil || len(repositories) != 0 {
			t.Errorf("Repositories = %+v, %v; want no repositories and no error", repositories, err)
		}
	})

	cases := map[string]struct {
		body   string
		status int
	}{
		"one byte over the limit":                 {padded("[]", maxResponseBytes+1), http.StatusOK},
		"an oversized body of a retryable status": {strings.Repeat("x", maxResponseBytes+1), http.StatusBadGateway},
	}
	for name, tc := range cases {
		t.Run(name+" is rejected without a retry", func(t *testing.T) {
			routes := organisationRoutes()
			routes[orgReposPath] = respond(tc.status, tc.body)
			client, fake := newFakeGitHub(t, routes)

			repositories, err := listRepositories(t, client)

			requireFailure(t, repositories, err, conformance.ErrResponseTooLarge)
			if want := wantPrefix + conformance.ErrResponseTooLarge.Error(); err.Error() != want {
				t.Errorf("err = %q, want %q", err, want)
			}
			if got := fake.Count(orgReposPath); got != 1 {
				t.Errorf("%s requested %d times, want 1", orgReposPath, got)
			}
			requireBackoffs(t, fake)
		})
	}
}

func TestRepositoriesQuotesOnlyTheStartOfAnErrorBody(t *testing.T) {
	longBody := strings.Repeat("x", maxExcerptBytes)
	cases := []struct {
		name string
		body string
		want string
	}{
		{"a body of exactly the limit is whole", longBody, longBody},
		{"one byte over the limit is cut", longBody + "x", longBody + "…"},
		{"a far longer body is cut", strings.Repeat("x", 5000), longBody + "…"},
		{"surrounding whitespace is trimmed", "  \n gone wrong\n", "gone wrong"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			routes := organisationRoutes()
			routes[orgReposPath] = respond(http.StatusInternalServerError, tc.body)
			client, _ := newFakeGitHub(t, routes)

			repositories, err := listRepositories(t, client)

			requireFailure(t, repositories, err, conformance.ErrGitHubStatus)
			want := "listing repositories of " + testOwner + ": " + conformance.ErrGitHubStatus.Error() + ": 500 " + tc.want
			if err.Error() != want {
				t.Errorf("err = %q, want %q", err, want)
			}
		})
	}
}

func TestRepositoriesRejectsMalformedJSONWithoutRetrying(t *testing.T) {
	cases := []struct {
		is     func(error) bool
		name   string
		path   string
		body   string
		prefix string
	}{
		{isSyntaxError, "owner lookup", usersPath, "this is not json", "decoding the response to looking up " + testOwner},
		{isSyntaxError, "token account lookup", tokenAccountPath, `{"login":`, "decoding the response to looking up the token's account"},
		{isTypeError, "listing page", userReposPath, `{}`, "decoding repositories of " + testOwner},
		{isSyntaxError, "root tree", projectRootPath, "{", "decoding the response to reading the tree main of " + testOwner + "/project"},
		{
			isTypeError, "subtree", projectDotGitHubPath, `{"tree":"x"}`,
			"decoding the response to reading the tree " + dotGitHubSHA + " of " + testOwner + "/project",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			routes := projectRoutes()
			routes[tc.path] = respond(http.StatusOK, tc.body)
			client, fake := newFakeGitHub(t, routes)

			repositories, err := listRepositories(t, client)

			if err == nil || !tc.is(err) {
				t.Fatalf("err = %v, want the JSON decoding error", err)
			}
			if !strings.HasPrefix(err.Error(), tc.prefix+": ") {
				t.Errorf("err = %q, want it to start with %q", err, tc.prefix+": ")
			}
			if repositories != nil {
				t.Errorf("repositories = %+v, want none alongside an error", repositories)
			}
			if got := fake.Count(tc.path); got != 1 {
				t.Errorf("%s requested %d times, want 1", tc.path, got)
			}
		})
	}
}
