package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const (
	GitHubGraphQLEndpoint = "https://api.github.com/graphql"

	repositoriesPerPage = 50
	maxResponseBytes    = 32 << 20
	maxErrorBodyBytes   = 512
)

// HEAD in an object expression resolves to the default branch.
const repositoriesQuery = `query($owner: String!, $first: Int!, $cursor: String) {
  repositoryOwner(login: $owner) {
    repositories(first: $first, after: $cursor, ownerAffiliations: [OWNER], orderBy: {field: NAME, direction: ASC}) {
      pageInfo { hasNextPage endCursor }
      nodes {
        name
        isFork
        isArchived
        isEmpty
        licenseInfo { spdxId }
        root: object(expression: "HEAD:") { ... on Tree { entries { name type } } }
        dotGitHub: object(expression: "HEAD:.github") { ... on Tree { entries { name type } } }
        docs: object(expression: "HEAD:docs") { ... on Tree { entries { name type } } }
      }
    }
  }
}`

var (
	ErrGitHubStatus       = errors.New("unexpected GitHub API status")
	ErrGitHubQuery        = errors.New("GitHub GraphQL query failed")
	ErrResponseTooLarge   = errors.New("GitHub API response exceeds the size limit")
	ErrOwnerNotFound      = errors.New("no GitHub user or organisation with this login")
	ErrContentsUnreadable = errors.New("default branch tree is unreadable: the token may lack read access to contents")
	ErrBrokenPagination   = errors.New("GitHub reported a next page without a cursor")
)

type GitHubClient struct {
	httpClient *http.Client
	endpoint   string
	token      string
}

type graphQLRequest struct {
	Variables map[string]any `json:"variables"`
	Query     string         `json:"query"`
}

type graphQLResponse struct {
	Data   *repositoriesData `json:"data"`
	Errors []graphQLError    `json:"errors"`
}

type graphQLError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type repositoriesData struct {
	RepositoryOwner *struct {
		Repositories struct {
			Nodes    []repositoryNode `json:"nodes"`
			PageInfo struct {
				EndCursor   string `json:"endCursor"`
				HasNextPage bool   `json:"hasNextPage"`
			} `json:"pageInfo"`
		} `json:"repositories"`
	} `json:"repositoryOwner"`
}

type repositoryNode struct {
	LicenseInfo *struct {
		SPDXID string `json:"spdxId"`
	} `json:"licenseInfo"`
	Root       *treeObject `json:"root"`
	DotGitHub  *treeObject `json:"dotGitHub"`
	Docs       *treeObject `json:"docs"`
	Name       string      `json:"name"`
	IsFork     bool        `json:"isFork"`
	IsArchived bool        `json:"isArchived"`
	IsEmpty    bool        `json:"isEmpty"`
}

// A path that is missing or is not a directory decodes as a nil or entry-less object.
type treeObject struct {
	Entries []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"entries"`
}

func NewGitHubClient(httpClient *http.Client, endpoint, token string) *GitHubClient {
	return &GitHubClient{httpClient: httpClient, endpoint: endpoint, token: token}
}

func (c *GitHubClient) Repositories(ctx context.Context, owner string) ([]Repository, error) {
	var repositories []Repository
	cursor := ""
	for {
		data, err := c.queryPage(ctx, owner, cursor)
		if err != nil {
			return nil, err
		}
		if data.RepositoryOwner == nil {
			return nil, fmt.Errorf("%w: %s", ErrOwnerNotFound, owner)
		}
		page := &data.RepositoryOwner.Repositories
		for i := range page.Nodes {
			repo, err := toRepository(owner, &page.Nodes[i])
			if err != nil {
				return nil, err
			}
			repositories = append(repositories, repo)
		}
		if !page.PageInfo.HasNextPage {
			return repositories, nil
		}
		if page.PageInfo.EndCursor == "" {
			return nil, fmt.Errorf("listing repositories of %s: %w", owner, ErrBrokenPagination)
		}
		cursor = page.PageInfo.EndCursor
	}
}

func (c *GitHubClient) queryPage(ctx context.Context, owner, cursor string) (*repositoriesData, error) {
	variables := map[string]any{"owner": owner, "first": repositoriesPerPage}
	if cursor != "" {
		variables["cursor"] = cursor
	}
	body, err := json.Marshal(graphQLRequest{Query: repositoriesQuery, Variables: variables})
	if err != nil {
		return nil, fmt.Errorf("encoding the repositories query: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building the repositories request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	payload, status, err := c.send(req)
	if err != nil {
		return nil, fmt.Errorf("listing repositories of %s: %w", owner, err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("listing repositories of %s: %w: %d %s", owner, ErrGitHubStatus, status, errorExcerpt(payload))
	}
	var response graphQLResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return nil, fmt.Errorf("decoding repositories of %s: %w", owner, err)
	}
	if len(response.Errors) > 0 {
		return nil, fmt.Errorf("listing repositories of %s: %w: %s", owner, ErrGitHubQuery, joinErrors(response.Errors))
	}
	if response.Data == nil {
		return nil, fmt.Errorf("listing repositories of %s: %w: response has no data", owner, ErrGitHubQuery)
	}
	return response.Data, nil
}

func (c *GitHubClient) send(req *http.Request) (payload []byte, status int, err error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("calling the GitHub API: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("closing the GitHub API response: %w", closeErr))
		}
	}()
	payload, err = io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, 0, fmt.Errorf("reading the GitHub API response: %w", err)
	}
	if len(payload) > maxResponseBytes {
		return nil, 0, ErrResponseTooLarge
	}
	return payload, resp.StatusCode, nil
}

func toRepository(owner string, node *repositoryNode) (Repository, error) {
	repo := Repository{
		Owner:    owner,
		Name:     node.Name,
		Fork:     node.IsFork,
		Archived: node.IsArchived,
		Empty:    node.IsEmpty,
	}
	if node.LicenseInfo != nil {
		repo.Licence = node.LicenseInfo.SPDXID
	}
	if repo.Skipped() {
		return repo, nil
	}
	if node.Root == nil {
		return Repository{}, fmt.Errorf("%s/%s: %w", owner, node.Name, ErrContentsUnreadable)
	}
	repo.Root = toEntries(node.Root)
	repo.DotGitHub = toEntries(node.DotGitHub)
	repo.Docs = toEntries(node.Docs)
	return repo, nil
}

func toEntries(tree *treeObject) []Entry {
	if tree == nil {
		return nil
	}
	entries := make([]Entry, 0, len(tree.Entries))
	for _, e := range tree.Entries {
		switch e.Type {
		case "blob":
			entries = append(entries, Entry{Name: e.Name})
		case "tree":
			entries = append(entries, Entry{Name: e.Name, IsDir: true})
		default:
		}
	}
	return entries
}

func joinErrors(errs []graphQLError) string {
	var order []string
	counts := make(map[string]int, len(errs))
	for i := range errs {
		message := errs[i].Message
		if errs[i].Type != "" {
			message = errs[i].Type + ": " + message
		}
		if counts[message] == 0 {
			order = append(order, message)
		}
		counts[message]++
	}
	messages := make([]string, 0, len(order))
	for _, message := range order {
		if counts[message] > 1 {
			message += " (" + strconv.Itoa(counts[message]) + " times)"
		}
		messages = append(messages, message)
	}
	return strings.Join(messages, "; ")
}

func errorExcerpt(payload []byte) string {
	text := strings.TrimSpace(string(payload))
	if len(text) > maxErrorBodyBytes {
		return text[:maxErrorBodyBytes] + "…"
	}
	return text
}
