package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	GitHubAPIURL = "https://api.github.com"

	repositoriesPerPage = "100"
	maxAttempts         = 3
	maxResponseBytes    = 32 << 20
	maxErrorBodyBytes   = 512

	ownerTypeUser = "User"
	treeEntryBlob = "blob"
	treeEntryTree = "tree"
)

var (
	ErrGitHubStatus         = errors.New("unexpected GitHub API status")
	ErrResponseTooLarge     = errors.New("GitHub API response exceeds the size limit")
	ErrOwnerNotFound        = errors.New("no GitHub user or organisation with this login")
	ErrUserNotAuthenticated = errors.New("the owner is a user other than the token's own account, whose private repositories cannot be listed")
	ErrContentsUnreadable   = errors.New("default branch tree is unreadable: the token may lack read access to contents")
	ErrBrokenPagination     = errors.New("GitHub reported a next page link that cannot be followed")

	errNotFound        = errors.New("not found")
	errRepositoryEmpty = errors.New("repository is empty")
)

// Backoff returns how long to wait before the given retry, counted from 1.
type Backoff func(retry int) time.Duration

type GitHubClient struct {
	httpClient *http.Client
	backoff    Backoff
	baseURL    string
	token      string
}

type response struct {
	header  http.Header
	payload []byte
	status  int
}

type account struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

type listedRepository struct {
	License *struct {
		SPDXID string `json:"spdx_id"`
	} `json:"license"`
	Owner         account `json:"owner"`
	Name          string  `json:"name"`
	DefaultBranch string  `json:"default_branch"`
	Fork          bool    `json:"fork"`
	Archived      bool    `json:"archived"`
}

type gitTree struct {
	Tree []treeEntry `json:"tree"`
}

type treeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

func DefaultBackoff(retry int) time.Duration {
	return time.Duration(retry) * time.Second
}

func NewGitHubClient(httpClient *http.Client, baseURL, token string, backoff Backoff) *GitHubClient {
	if backoff == nil {
		backoff = DefaultBackoff
	}
	return &GitHubClient{httpClient: httpClient, backoff: backoff, baseURL: strings.TrimSuffix(baseURL, "/"), token: token}
}

func (c *GitHubClient) Repositories(ctx context.Context, owner string) ([]Repository, error) {
	listURL, err := c.repositoriesURL(ctx, owner)
	if err != nil {
		return nil, err
	}
	listed, err := c.listRepositories(ctx, owner, listURL)
	if err != nil {
		return nil, err
	}
	repositories := make([]Repository, 0, len(listed))
	for i := range listed {
		if !strings.EqualFold(listed[i].Owner.Login, owner) {
			continue
		}
		repo, err := c.toRepository(ctx, owner, &listed[i])
		if err != nil {
			return nil, err
		}
		repositories = append(repositories, repo)
	}
	return repositories, nil
}

// A user's private repositories are listed only by /user/repos, which serves the token's own account.
func (c *GitHubClient) repositoriesURL(ctx context.Context, owner string) (string, error) {
	query := url.Values{"per_page": {repositoriesPerPage}, "sort": {"full_name"}}
	var ownerAccount account
	if err := c.getJSON(ctx, "/users/"+url.PathEscape(owner), "looking up "+owner, &ownerAccount); err != nil {
		if errors.Is(err, errNotFound) {
			return "", fmt.Errorf("%w: %s", ErrOwnerNotFound, owner)
		}
		return "", err
	}
	if ownerAccount.Type != ownerTypeUser {
		query.Set("type", "all")
		return c.baseURL + "/orgs/" + url.PathEscape(owner) + "/repos?" + query.Encode(), nil
	}
	var authenticated account
	if err := c.getJSON(ctx, "/user", "looking up the token's account", &authenticated); err != nil {
		return "", err
	}
	if !strings.EqualFold(authenticated.Login, owner) {
		return "", fmt.Errorf("listing repositories of %s: %w (the token belongs to %s)", owner, ErrUserNotAuthenticated, authenticated.Login)
	}
	query.Set("affiliation", "owner")
	query.Set("visibility", "all")
	return c.baseURL + "/user/repos?" + query.Encode(), nil
}

func (c *GitHubClient) getJSON(ctx context.Context, path, operation string, target any) error {
	resp, err := c.get(ctx, c.baseURL+path)
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	switch resp.status {
	case http.StatusOK:
	case http.StatusNotFound:
		return fmt.Errorf("%s: %w", operation, errNotFound)
	default:
		return fmt.Errorf("%s: %w: %d %s", operation, ErrGitHubStatus, resp.status, errorExcerpt(resp.payload))
	}
	if err := json.Unmarshal(resp.payload, target); err != nil {
		return fmt.Errorf("decoding the response to %s: %w", operation, err)
	}
	return nil
}

func (c *GitHubClient) listRepositories(ctx context.Context, owner, pageURL string) ([]listedRepository, error) {
	operation := "listing repositories of " + owner
	var listed []listedRepository
	for pageURL != "" {
		resp, err := c.get(ctx, pageURL)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", operation, err)
		}
		if resp.status != http.StatusOK {
			return nil, fmt.Errorf("%s: %w: %d %s", operation, ErrGitHubStatus, resp.status, errorExcerpt(resp.payload))
		}
		var page []listedRepository
		if decodeErr := json.Unmarshal(resp.payload, &page); decodeErr != nil {
			return nil, fmt.Errorf("decoding repositories of %s: %w", owner, decodeErr)
		}
		listed = append(listed, page...)
		if pageURL, err = c.nextPage(resp.header); err != nil {
			return nil, fmt.Errorf("%s: %w", operation, err)
		}
	}
	return listed, nil
}

// Every request carries the token, so a next link is followed only on the API's own host.
func (c *GitHubClient) nextPage(header http.Header) (string, error) {
	for link := range strings.SplitSeq(header.Get("Link"), ",") {
		target, params, found := strings.Cut(strings.TrimSpace(link), ";")
		if !found || !hasNextRelation(params) {
			continue
		}
		next := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(target), "<"), ">")
		nextURL, err := url.Parse(next)
		if err != nil {
			return "", fmt.Errorf("%w: %w", ErrBrokenPagination, err)
		}
		base, err := url.Parse(c.baseURL)
		if err != nil {
			return "", fmt.Errorf("parsing the GitHub API URL: %w", err)
		}
		if nextURL.Scheme != base.Scheme || nextURL.Host != base.Host {
			return "", fmt.Errorf("%w: %s", ErrBrokenPagination, next)
		}
		return next, nil
	}
	return "", nil
}

func hasNextRelation(params string) bool {
	for param := range strings.SplitSeq(params, ";") {
		if strings.TrimSpace(param) == `rel="next"` {
			return true
		}
	}
	return false
}

func (c *GitHubClient) toRepository(ctx context.Context, owner string, listed *listedRepository) (Repository, error) {
	repo := Repository{
		Owner:    owner,
		Name:     listed.Name,
		Fork:     listed.Fork,
		Archived: listed.Archived,
	}
	if listed.License != nil {
		repo.Licence = listed.License.SPDXID
	}
	if repo.Skipped() {
		return repo, nil
	}
	root, err := c.tree(ctx, &repo, listed.DefaultBranch)
	if errors.Is(err, errRepositoryEmpty) {
		repo.Empty = true
		return repo, nil
	}
	if err != nil {
		return Repository{}, err
	}
	repo.Root = toEntries(root)
	if repo.DotGitHub, err = c.subtree(ctx, &repo, root, ".github"); err != nil {
		return Repository{}, err
	}
	if repo.Docs, err = c.subtree(ctx, &repo, root, "docs"); err != nil {
		return Repository{}, err
	}
	return repo, nil
}

func (c *GitHubClient) subtree(ctx context.Context, repo *Repository, root *gitTree, name string) ([]Entry, error) {
	for _, entry := range root.Tree {
		if entry.Path == name && entry.Type == treeEntryTree {
			tree, err := c.tree(ctx, repo, entry.SHA)
			if err != nil {
				return nil, err
			}
			return toEntries(tree), nil
		}
	}
	return nil, nil
}

func (c *GitHubClient) tree(ctx context.Context, repo *Repository, ref string) (*gitTree, error) {
	operation := "reading the tree " + ref + " of " + repo.Owner + "/" + repo.Name
	treeURL := c.baseURL + "/repos/" + url.PathEscape(repo.Owner) + "/" + url.PathEscape(repo.Name) + "/git/trees/" + escapeRef(ref)
	resp, err := c.get(ctx, treeURL)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	switch resp.status {
	case http.StatusOK:
	case http.StatusConflict:
		return nil, errRepositoryEmpty
	case http.StatusForbidden, http.StatusNotFound:
		return nil, fmt.Errorf("%s/%s: %w", repo.Owner, repo.Name, ErrContentsUnreadable)
	default:
		return nil, fmt.Errorf("%s: %w: %d %s", operation, ErrGitHubStatus, resp.status, errorExcerpt(resp.payload))
	}
	var tree gitTree
	if err := json.Unmarshal(resp.payload, &tree); err != nil {
		return nil, fmt.Errorf("decoding the response to %s: %w", operation, err)
	}
	return &tree, nil
}

func escapeRef(ref string) string {
	segments := strings.Split(ref, "/")
	for i := range segments {
		segments[i] = url.PathEscape(segments[i])
	}
	return strings.Join(segments, "/")
}

func toEntries(tree *gitTree) []Entry {
	entries := make([]Entry, 0, len(tree.Tree))
	for _, e := range tree.Tree {
		switch e.Type {
		case treeEntryBlob:
			entries = append(entries, Entry{Name: e.Path})
		case treeEntryTree:
			entries = append(entries, Entry{Name: e.Path, IsDir: true})
		default:
		}
	}
	return entries
}

func (c *GitHubClient) get(ctx context.Context, requestURL string) (*response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("building the GitHub API request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	for attempt := 1; ; attempt++ {
		resp, sendErr := c.send(req.Clone(ctx))
		if attempt == maxAttempts || !retryable(ctx, resp, sendErr) {
			return resp, sendErr
		}
		if waitErr := wait(ctx, c.backoff(attempt)); waitErr != nil {
			return nil, fmt.Errorf("waiting to retry the GitHub API: %w", waitErr)
		}
	}
}

func retryable(ctx context.Context, resp *response, err error) bool {
	if err != nil {
		return ctx.Err() == nil && !errors.Is(err, ErrResponseTooLarge)
	}
	switch resp.status {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *GitHubClient) send(req *http.Request) (resp *response, err error) {
	httpResp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling the GitHub API: %w", err)
	}
	defer func() {
		if closeErr := httpResp.Body.Close(); closeErr != nil {
			resp, err = nil, errors.Join(err, fmt.Errorf("closing the GitHub API response: %w", closeErr))
		}
	}()
	payload, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading the GitHub API response: %w", err)
	}
	if len(payload) > maxResponseBytes {
		return nil, ErrResponseTooLarge
	}
	return &response{header: httpResp.Header, payload: payload, status: httpResp.StatusCode}, nil
}

func errorExcerpt(payload []byte) string {
	text := strings.TrimSpace(string(payload))
	if len(text) > maxErrorBodyBytes {
		return text[:maxErrorBodyBytes] + "…"
	}
	return text
}
