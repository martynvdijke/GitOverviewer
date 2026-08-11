// Package forgejo implements the provider.Provider interface against
// a Forgejo (or Gitea-compatible) REST API at <base>/api/v1.
//
// The API is fully Gitea-compatible; the client targets endpoints that
// have been stable since Gitea 1.20. Forgejo Actions is opt-in and not
// assumed to be enabled: workflow status is hard-coded to "unknown".
package forgejo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	ghclient "gitlens/internal/github"
)

// Client is a Forgejo/Gitea REST API client. The same instance is
// shared across all Forgejo users (no per-user state); access tokens
// are passed per-call.
type Client struct {
	clientID     string
	clientSecret string
	defaultURL   string // optional; may be "" if user picks at login
	httpClient   *http.Client
	TokenURL     string // overridable for tests
	APIURL       string // base URL; usually <defaultURL>/api/v1
}

// NewClient constructs a Client. If `defaultURL` is non-empty it is
// used as the default instance base (e.g. "https://codeberg.org").
// `clientID` and `clientSecret` are required; the FORGEJO_CLIENT_ID
// check happens in main.go before this is called.
func NewClient(clientID, clientSecret, defaultURL string) *Client {
	api := strings.TrimRight(defaultURL, "/") + "/api/v1"
	tok := strings.TrimRight(defaultURL, "/") + "/login/oauth/access_token"
	return &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		defaultURL:   strings.TrimRight(defaultURL, "/"),
		httpClient:   &http.Client{Timeout: 30 * time.Second},
		TokenURL:     tok,
		APIURL:       api,
	}
}

// DefaultURL returns the configured default instance base, or "" if
// none was set.
func (c *Client) DefaultURL() string { return c.defaultURL }

// doRequest is the internal HTTP helper. We do NOT track rate limits
// (Forgejo's header set varies by version); rate-limit handling is
// out of scope for v1.
func (c *Client) doRequest(ctx context.Context, method, urlStr, token string, body io.Reader) (*http.Response, error) {
	if c.APIURL == "" {
		return nil, fmt.Errorf("forgejo: APIURL is empty (no default instance configured)")
	}
	req, err := http.NewRequestWithContext(ctx, method, urlStr, body)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("doing request: %w", err)
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("Forgejo API error: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

// resolveBase returns the instance base URL for this call. It honors a
// per-user override (passed in by the auth handler when the user
// connects to a non-default instance) and falls back to defaultURL.
func (c *Client) resolveBase(override string) string {
	if override != "" {
		return strings.TrimRight(override, "/")
	}
	return c.defaultURL
}

// apiURL builds an absolute API URL given an instance base and a path.
func (c *Client) apiURL(base, path string) string {
	return strings.TrimRight(base, "/") + "/api/v1" + path
}

// --- Provider interface ---

// Name implements provider.Provider.
func (c *Client) Name() string { return "forgejo" }

// --- Gitea wire types ---

type gjUser struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	FullName  string `json:"full_name"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatar_url"`
}

type gjRepo struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Description   string `json:"description"`
	HTMLURL       string `json:"html_url"`
	Language      string `json:"language"`
	DefaultBranch string `json:"default_branch"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

type gjCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message   string `json:"message"`
		Committer struct {
			Date string `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
}

type gjRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	PublishedAt string `json:"published_at"`
}

type gjPullRequest struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	User   struct {
		Login string `json:"login"`
	} `json:"user"`
	CreatedAt string `json:"created_at"`
	HTMLURL   string `json:"html_url"`
	Head      struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Merged    bool   `json:"merged"`
	MergedAt  string `json:"merged_at"`
	Mergeable bool   `json:"mergeable"`
	HeadRef   string `json:"head_ref"`
	State     string `json:"state"`
}

// --- OAuth ---

// AuthURL implements provider.Provider. `state` is a CSRF token echoed
// back on the callback. The instance base URL is taken from override if
// provided, else from the constructor's defaultURL.
func (c *Client) AuthURL(state, redirectURL string) string {
	return c.AuthURLFor("", state, redirectURL)
}

// AuthURLFor is the instance-aware version: pass the user's chosen
// instance base (e.g. "https://codeberg.org") as `instanceBase`. If
// empty, falls back to c.defaultURL.
func (c *Client) AuthURLFor(instanceBase, state, redirectURL string) string {
	base := c.resolveBase(instanceBase)
	if base == "" {
		// Caller didn't set an instance and there is no default.
		// Return an empty string; the auth handler must catch this
		// before calling AuthURL.
		return ""
	}
	u := base + "/login/oauth/authorize?client_id=" + url.QueryEscape(c.clientID) +
		"&redirect_uri=" + url.QueryEscape(redirectURL) +
		"&response_type=code" +
		"&scope=repository"
	if state != "" {
		u += "&state=" + url.QueryEscape(state)
	}
	return u
}

// ExchangeCodeFor exchanges a code against the specified instance and
// returns the access token + user identity. The handler passes the
// instance base via the oauth_state cookie.
func (c *Client) ExchangeCodeFor(ctx context.Context, instanceBase, code, redirectURL string) (string, *ghclient.User, error) {
	base := c.resolveBase(instanceBase)
	if base == "" {
		return "", nil, fmt.Errorf("forgejo: no instance base URL provided")
	}
	tokURL := base + "/login/oauth/access_token"
	form := url.Values{
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"code":          {code},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {redirectURL},
	}
	req, err := http.NewRequestWithContext(ctx, "POST", tokURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", nil, fmt.Errorf("creating token request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("exchanging code: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", nil, fmt.Errorf("decoding token response: %w", err)
	}
	if result.Error != "" {
		return "", nil, fmt.Errorf("OAuth error: %s (%s)", result.Error, result.ErrorDesc)
	}
	if result.AccessToken == "" {
		return "", nil, fmt.Errorf("no access token in response")
	}

	// Fetch the user identity from the same instance.
	u, err := c.getUserAt(ctx, base, result.AccessToken)
	if err != nil {
		return "", nil, err
	}
	return result.AccessToken, u, nil
}

// ExchangeCode implements provider.Provider. It uses the default
// instance URL — handlers that support a per-request instance should
// call ExchangeCodeFor directly.
func (c *Client) ExchangeCode(ctx context.Context, code, redirectURL string) (string, *ghclient.User, error) {
	return c.ExchangeCodeFor(ctx, "", code, redirectURL)
}

func (c *Client) getUserAt(ctx context.Context, base, token string) (*ghclient.User, error) {
	resp, err := c.doRequest(ctx, "GET", c.apiURL(base, "/user"), token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var u gjUser
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return nil, fmt.Errorf("decoding user: %w", err)
	}
	name := u.FullName
	return &ghclient.User{
		ID:        u.ID,
		Login:     u.Login,
		AvatarURL: u.AvatarURL,
		Name:      name,
	}, nil
}

// GetUser implements provider.Provider.
func (c *Client) GetUser(ctx context.Context, token string) (*ghclient.User, error) {
	return c.getUserAt(ctx, c.defaultURL, token)
}

// --- Repository listing ---

// ListRepositories implements provider.Provider. Walks /user/repos with
// pagination (page size 50) until an empty page is returned.
func (c *Client) ListRepositories(ctx context.Context, token string) ([]*ghclient.Repository, error) {
	var all []*ghclient.Repository
	page := 1
	for {
		u := c.apiURL(c.defaultURL, fmt.Sprintf("/user/repos?limit=50&page=%d", page))
		resp, err := c.doRequest(ctx, "GET", u, token, nil)
		if err != nil {
			return nil, err
		}
		var repos []gjRepo
		if err := json.NewDecoder(resp.Body).Decode(&repos); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("decoding repos: %w", err)
		}
		resp.Body.Close()
		if len(repos) == 0 {
			break
		}
		for _, r := range repos {
			all = append(all, &ghclient.Repository{
				ID:            r.ID,
				Owner:         r.Owner.Login,
				Name:          r.Name,
				FullName:      r.FullName,
				Description:   r.Description,
				HTMLURL:       r.HTMLURL,
				Language:      r.Language,
				DefaultBranch: r.DefaultBranch,
			})
		}
		if len(repos) < 50 {
			break
		}
		page++
	}
	return all, nil
}

// --- Commits ---

// GetCommitsSince implements provider.Provider. Uses Gitea's `since`
// parameter. If since is zero, returns up to maxCommits recent
// commits. Forgejo's response shape matches GitHub's closely enough
// that we can reuse gjCommit.
func (c *Client) GetCommitsSince(ctx context.Context, token, owner, repo, branch string, since time.Time, maxCommits int) ([]*ghclient.Commit, error) {
	if maxCommits <= 0 || maxCommits > 500 {
		maxCommits = 500
	}
	var all []*ghclient.Commit
	page := 1
	const perPage = 50
	for len(all) < maxCommits {
		u := c.apiURL(c.defaultURL, fmt.Sprintf("/repos/%s/%s/commits?limit=%d&page=%d&sha=%s",
			owner, repo, perPage, page, url.QueryEscape(branch)))
		if !since.IsZero() {
			u += "&since=" + url.QueryEscape(since.UTC().Format(time.RFC3339))
		}
		resp, err := c.doRequest(ctx, "GET", u, token, nil)
		if err != nil {
			return all, err
		}
		var commits []gjCommit
		if err := json.NewDecoder(resp.Body).Decode(&commits); err != nil {
			resp.Body.Close()
			return all, fmt.Errorf("decoding commits: %w", err)
		}
		resp.Body.Close()
		if len(commits) == 0 {
			break
		}
		remaining := maxCommits - len(all)
		for i, c := range commits {
			if i >= remaining {
				break
			}
			t, _ := time.Parse(time.RFC3339, c.Commit.Committer.Date)
			all = append(all, &ghclient.Commit{
				SHA:     c.SHA,
				Message: strings.Split(c.Commit.Message, "\n")[0],
				Date:    t,
			})
		}
		if len(commits) < perPage {
			break
		}
		page++
	}
	return all, nil
}

// ListCommitsPage fetches a single page of commits (newest first) from
// the given branch. perPage is clamped to [1,50] (Gitea's default max
// page size); page is 1-indexed. hasMore reports whether another page
// likely exists (page was full).
func (c *Client) ListCommitsPage(ctx context.Context, token, owner, repo, branch string, page, perPage int) (commits []*ghclient.Commit, hasMore bool, err error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 1
	}
	if perPage > 50 {
		perPage = 50
	}

	u := c.apiURL(c.defaultURL, fmt.Sprintf("/repos/%s/%s/commits?limit=%d&page=%d&sha=%s",
		owner, repo, perPage, page, url.QueryEscape(branch)))
	resp, err := c.doRequest(ctx, "GET", u, token, nil)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()

	var gjCommits []gjCommit
	if err := json.NewDecoder(resp.Body).Decode(&gjCommits); err != nil {
		return nil, false, fmt.Errorf("decoding commits: %w", err)
	}
	for _, gc := range gjCommits {
		t, _ := time.Parse(time.RFC3339, gc.Commit.Committer.Date)
		commits = append(commits, &ghclient.Commit{
			SHA:     gc.SHA,
			Message: strings.Split(gc.Commit.Message, "\n")[0],
			Date:    t,
		})
	}
	return commits, len(gjCommits) == perPage, nil
}

// MergePullRequest merges an open PR using the "merge" method
// (Gitea: POST /repos/{owner}/{repo}/pulls/{index}/merge {"Do":"merge"}).
// Returns (false, message, nil) when the server refuses the merge
// (HTTP 405 — e.g. conflicts, failing checks); other HTTP failures are
// returned as errors.
func (c *Client) MergePullRequest(ctx context.Context, token, owner, repo string, number int) (bool, string, error) {
	u := c.apiURL(c.defaultURL, fmt.Sprintf("/repos/%s/%s/pulls/%d/merge", owner, repo, number))
	payload := struct {
		Do string `json:"Do"`
	}{Do: "merge"}
	var buf strings.Builder
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return false, "", fmt.Errorf("encoding merge request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", u, strings.NewReader(buf.String()))
	if err != nil {
		return false, "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, "", fmt.Errorf("doing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return true, "", nil
	}

	body, _ := io.ReadAll(resp.Body)
	var errResp struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &errResp)
	msg := strings.TrimSpace(errResp.Message)

	// 405: merge not allowed right now (conflicts, checks failing, etc.)
	if resp.StatusCode == http.StatusMethodNotAllowed {
		if msg == "" {
			msg = "merge not allowed"
		}
		return false, msg, nil
	}
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	return false, "", fmt.Errorf("Forgejo API error: %s: %s", resp.Status, msg)
}

// ClosePullRequest closes an open PR
// (Gitea: PATCH /repos/{owner}/{repo}/pulls/{index} {"state":"closed"}).
func (c *Client) ClosePullRequest(ctx context.Context, token, owner, repo string, number int) error {
	u := c.apiURL(c.defaultURL, fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, number))
	payload := struct {
		State string `json:"state"`
	}{State: "closed"}
	var buf strings.Builder
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return fmt.Errorf("encoding close request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "PATCH", u, strings.NewReader(buf.String()))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("doing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	var errResp struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &errResp)
	msg := strings.TrimSpace(errResp.Message)
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	return fmt.Errorf("Forgejo API error: %s: %s", resp.Status, msg)
}

// --- Releases ---

// ListReleases implements provider.Provider.
func (c *Client) ListReleases(ctx context.Context, token, owner, repo string) ([]*ghclient.Release, error) {
	var all []*ghclient.Release
	page := 1
	for {
		u := c.apiURL(c.defaultURL, fmt.Sprintf("/repos/%s/%s/releases?limit=50&page=%d", owner, repo, page))
		resp, err := c.doRequest(ctx, "GET", u, token, nil)
		if err != nil {
			return all, err
		}
		var gjReleases []gjRelease
		if err := json.NewDecoder(resp.Body).Decode(&gjReleases); err != nil {
			resp.Body.Close()
			return all, fmt.Errorf("decoding releases: %w", err)
		}
		resp.Body.Close()
		if len(gjReleases) == 0 {
			break
		}
		for _, r := range gjReleases {
			t, _ := time.Parse(time.RFC3339, r.PublishedAt)
			all = append(all, &ghclient.Release{TagName: r.TagName, Name: r.Name, PublishedAt: t})
		}
		if len(gjReleases) < 50 {
			break
		}
		page++
	}
	return all, nil
}

// --- Pull requests ---

// ListPullRequests implements provider.Provider. Returns OPEN PRs;
// mirrors the GitHub ListPullRequests behavior used by the syncer.
func (c *Client) ListPullRequests(ctx context.Context, token, owner, repo string) ([]*ghclient.PullRequest, error) {
	u := c.apiURL(c.defaultURL, fmt.Sprintf("/repos/%s/%s/pulls?state=open&limit=50", owner, repo))
	resp, err := c.doRequest(ctx, "GET", u, token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var gjPRs []gjPullRequest
	if err := json.NewDecoder(resp.Body).Decode(&gjPRs); err != nil {
		return nil, fmt.Errorf("decoding pull requests: %w", err)
	}
	var prs []*ghclient.PullRequest
	for _, pr := range gjPRs {
		t, _ := time.Parse(time.RFC3339, pr.CreatedAt)
		// Gitea reports a boolean mergeable flag; map to GitHub-style states.
		mergeableState := "unknown"
		if pr.Mergeable {
			mergeableState = "clean"
		} else {
			mergeableState = "dirty"
		}
		prs = append(prs, &ghclient.PullRequest{
			Number:         pr.Number,
			Title:          pr.Title,
			Author:         pr.User.Login,
			CreatedAt:      t,
			HTMLURL:        pr.HTMLURL,
			HeadRef:        pr.Head.Ref,
			BaseRef:        pr.Base.Ref,
			MergeableState: mergeableState,
		})
	}
	return prs, nil
}

// ListRecentlyMergedPRs implements provider.Provider. Returns the
// most recently closed+merged PRs. Forgejo's PR list returns merged
// PRs in updated-desc order when filtered by state=closed.
func (c *Client) ListRecentlyMergedPRs(ctx context.Context, token, owner, repo string) ([]*ghclient.PullRequest, error) {
	u := c.apiURL(c.defaultURL, fmt.Sprintf("/repos/%s/%s/pulls?state=closed&limit=10", owner, repo))
	resp, err := c.doRequest(ctx, "GET", u, token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var gjPRs []gjPullRequest
	if err := json.NewDecoder(resp.Body).Decode(&gjPRs); err != nil {
		return nil, fmt.Errorf("decoding pull requests: %w", err)
	}
	var prs []*ghclient.PullRequest
	for _, pr := range gjPRs {
		if !pr.Merged {
			continue
		}
		t, _ := time.Parse(time.RFC3339, pr.CreatedAt)
		prs = append(prs, &ghclient.PullRequest{
			Number:    pr.Number,
			Title:     pr.Title,
			Author:    pr.User.Login,
			CreatedAt: t,
			HTMLURL:   pr.HTMLURL,
			HeadRef:   pr.Head.Ref,
			BaseRef:   pr.Base.Ref,
		})
	}
	return prs, nil
}

// GetLatestWorkflowRun implements provider.Provider. Forgejo Actions
// is opt-in; we report "unknown" and skip the HTTP call. Callers
// should treat a zero/nil return as "no data".
func (c *Client) GetLatestWorkflowRun(ctx context.Context, token, owner, repo, branch string) (*ghclient.WorkflowRun, error) {
	_ = ctx
	_ = token
	_ = owner
	_ = repo
	_ = branch
	// We deliberately return an "unknown" marker rather than an error
	// so the syncer's "if err == nil" branch is taken and
	// workflow_status is set to "unknown".
	return &ghclient.WorkflowRun{ID: 0, Conclusion: "unknown"}, nil
}

// AuthCodeLenHint is unused; reserved for future instrumentation.
var _ = strconv.Itoa
