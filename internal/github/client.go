package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	clientID     string
	clientSecret string
	httpClient   *http.Client
	TokenURL     string
	APIURL       string

	RateLimitRemaining int
	RateLimitReset     int64
}

// RateLimitError is returned when GitHub reports the rate limit is
// exhausted (HTTP 403/429 with X-RateLimit-Remaining: 0). Callers can
// use errors.As to detect it and back off until RetryAfter.
type RateLimitError struct {
	// RetryAfter is when the rate limit window resets (may be zero if
	// the server did not report a reset time).
	RetryAfter time.Time
	Status     string
}

func (e *RateLimitError) Error() string {
	if !e.RetryAfter.IsZero() {
		return fmt.Sprintf("github rate limit exhausted, retry after %s", e.RetryAfter.Format(time.RFC3339))
	}
	return fmt.Sprintf("github rate limit exhausted (%s)", e.Status)
}

func NewClient(clientID, clientSecret string) *Client {
	return &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		TokenURL: "https://github.com/login/oauth/access_token",
		APIURL:   "https://api.github.com",
	}
}

// AuthorizeURL returns the URL the browser should be redirected to in
// order to begin the OAuth flow. `state` is an opaque CSRF token
// echoed back on the callback; pass "" to omit.
func (c *Client) AuthorizeURL(state, redirectURL string) string {
	u := "https://github.com/login/oauth/authorize?client_id=" + c.clientID + "&redirect_uri=" + redirectURL + "&scope=repo,read:user"
	if state != "" {
		u += "&state=" + state
	}
	return u
}

type ghUser struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
	Name      string `json:"name"`
}

type ghRepo struct {
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

type ghCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message   string `json:"message"`
		Committer struct {
			Date string `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
}

type ghRelease struct {
	TagName     string `json:"tag_name"`
	Name        string `json:"name"`
	PublishedAt string `json:"published_at"`
}

type ghWorkflowRuns struct {
	WorkflowRuns []ghWorkflowRun `json:"workflow_runs"`
}

type ghWorkflowRun struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HeadSHA    string `json:"head_sha"`
}

type User struct {
	ID        int64
	Login     string
	AvatarURL string
	Name      string
}

type Repository struct {
	ID            int64
	Owner         string
	Name          string
	FullName      string
	Description   string
	HTMLURL       string
	Language      string
	DefaultBranch string
}

type Commit struct {
	SHA     string
	Message string
	Date    time.Time
}

type Release struct {
	TagName     string
	Name        string
	PublishedAt time.Time
}

type WorkflowRun struct {
	ID         int64
	Status     string
	Conclusion string
	HeadSHA    string
}

type PullRequest struct {
	Number    int
	Title     string
	Author    string
	CreatedAt time.Time
	HTMLURL   string
	HeadRef   string
	BaseRef   string
	// MergeableState is the provider's mergeability hint ("clean",
	// "dirty", "unknown", ...). Empty when the provider doesn't report it.
	MergeableState string
	// HeadSHA is the commit SHA the head branch points at. Used to match
	// workflow runs to the PR. Empty when the provider doesn't report it.
	HeadSHA string
}

type ghPullRequest struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	User   struct {
		Login string `json:"login"`
	} `json:"user"`
	CreatedAt string `json:"created_at"`
	HTMLURL   string `json:"html_url"`
	Head      struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	MergeableState string `json:"mergeable_state"`
	MergedAt       string `json:"merged_at"`
	Merged         bool   `json:"merged"`
}

type ghIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
}

func (c *Client) doRequest(method, urlStr, token string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, urlStr, body)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("doing request: %w", err)
	}

	if v := resp.Header.Get("X-RateLimit-Remaining"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.RateLimitRemaining = n
		}
	}
	if v := resp.Header.Get("X-RateLimit-Reset"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			c.RateLimitReset = n
		}
	}

	if resp.StatusCode >= 400 {
		resp.Body.Close()
		// Rate limit exhausted: 403/429 with no remaining quota.
		if (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests) &&
			resp.Header.Get("X-RateLimit-Remaining") == "0" {
			rlErr := &RateLimitError{Status: resp.Status}
			if v := resp.Header.Get("X-RateLimit-Reset"); v != "" {
				if n, err := strconv.ParseInt(v, 10, 64); err == nil {
					rlErr.RetryAfter = time.Unix(n, 0)
				}
			}
			return nil, rlErr
		}
		return nil, fmt.Errorf("GitHub API error: %s", resp.Status)
	}
	return resp, nil
}

func (c *Client) GetAccessToken(code string) (string, error) {
	form := url.Values{
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"code":          {code},
	}
	req, err := http.NewRequest("POST", c.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("creating token request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("exchanging code: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding token response: %w", err)
	}
	if result.Error != "" {
		return "", fmt.Errorf("OAuth error: %s", result.Error)
	}
	if result.AccessToken == "" {
		return "", fmt.Errorf("no access token in response")
	}
	return result.AccessToken, nil
}

func (c *Client) GetUser(token string) (*User, error) {
	resp, err := c.doRequest("GET", c.APIURL+"/user", token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var u ghUser
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return nil, fmt.Errorf("decoding user: %w", err)
	}
	return &User{
		ID:        u.ID,
		Login:     u.Login,
		AvatarURL: u.AvatarURL,
		Name:      u.Name,
	}, nil
}

func (c *Client) ListRepositories(token string) ([]*Repository, error) {
	var allRepos []*Repository
	page := 1
	for {
		url := fmt.Sprintf("%s/user/repos?per_page=100&page=%d&sort=updated", c.APIURL, page)
		resp, err := c.doRequest("GET", url, token, nil)
		if err != nil {
			return nil, err
		}

		var repos []ghRepo
		if err := json.NewDecoder(resp.Body).Decode(&repos); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("decoding repos: %w", err)
		}
		resp.Body.Close()

		if len(repos) == 0 {
			break
		}

		for _, r := range repos {
			allRepos = append(allRepos, &Repository{
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
		page++
	}
	return allRepos, nil
}

func (c *Client) GetLatestCommit(token, owner, repo, branch string) (*Commit, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/commits?per_page=1&sha=%s", c.APIURL, owner, repo, branch)
	resp, err := c.doRequest("GET", url, token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var commits []ghCommit
	if err := json.NewDecoder(resp.Body).Decode(&commits); err != nil {
		return nil, fmt.Errorf("decoding commits: %w", err)
	}
	if len(commits) == 0 {
		return nil, fmt.Errorf("no commits found")
	}

	t, _ := time.Parse(time.RFC3339, commits[0].Commit.Committer.Date)
	return &Commit{
		SHA:     commits[0].SHA,
		Message: strings.Split(commits[0].Commit.Message, "\n")[0],
		Date:    t,
	}, nil
}

// getCommitsPage fetches a single page of commits from the GitHub API.
func (c *Client) getCommitsPage(token, owner, repo, branch string, perPage, page int) ([]*Commit, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/commits?per_page=%d&page=%d&sha=%s", c.APIURL, owner, repo, perPage, page, branch)
	resp, err := c.doRequest("GET", url, token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var ghCommits []ghCommit
	if err := json.NewDecoder(resp.Body).Decode(&ghCommits); err != nil {
		return nil, fmt.Errorf("decoding commits: %w", err)
	}

	var commits []*Commit
	for _, c := range ghCommits {
		t, _ := time.Parse(time.RFC3339, c.Commit.Committer.Date)
		commits = append(commits, &Commit{
			SHA:     c.SHA,
			Message: strings.Split(c.Commit.Message, "\n")[0],
			Date:    t,
		})
	}
	return commits, nil
}

// GetCommits fetches up to perPage commits from the default branch (page 1).
// Use GetCommitsSince for paginated or incremental fetching.
func (c *Client) GetCommits(token, owner, repo, branch string, perPage int) ([]*Commit, error) {
	return c.getCommitsPage(token, owner, repo, branch, perPage, 1)
}

// GetCommitsSince fetches commits since a given time with rate-limit awareness.
// Uses GitHub's `since` parameter for incremental fetching.
// If since is zero time, fetches recent commits up to maxCommits.
// Adds inter-page delays and respects X-RateLimit-Remaining headers.
func (c *Client) GetCommitsSince(token, owner, repo, branch string, since time.Time, maxCommits int) ([]*Commit, error) {
	const perPage = 100
	if maxCommits <= 0 || maxCommits > 500 {
		maxCommits = 500
	}

	var allCommits []*Commit
	page := 1

	for len(allCommits) < maxCommits {
		if c.RateLimitRemaining < 10 {
			if c.RateLimitReset > 0 {
				wait := time.Until(time.Unix(c.RateLimitReset, 0))
				if wait > 0 && wait < 60*time.Second {
					log.Printf("Rate limit low (%d remaining), waiting %v before next request...", c.RateLimitRemaining, wait)
					time.Sleep(wait)
				}
			}
		}

		u := fmt.Sprintf("%s/repos/%s/%s/commits?per_page=%d&page=%d&sha=%s",
			c.APIURL, owner, repo, perPage, page, branch)
		if !since.IsZero() {
			u += "&since=" + url.QueryEscape(since.Format(time.RFC3339))
		}
		resp, err := c.doRequest("GET", u, token, nil)
		if err != nil {
			return allCommits, err
		}

		var ghCommits []ghCommit
		if err := json.NewDecoder(resp.Body).Decode(&ghCommits); err != nil {
			resp.Body.Close()
			return allCommits, fmt.Errorf("decoding commits: %w", err)
		}
		resp.Body.Close()

		if len(ghCommits) == 0 {
			break
		}

		remaining := maxCommits - len(allCommits)
		for i, c := range ghCommits {
			if i >= remaining {
				break
			}
			t, _ := time.Parse(time.RFC3339, c.Commit.Committer.Date)
			allCommits = append(allCommits, &Commit{
				SHA:     c.SHA,
				Message: strings.Split(c.Commit.Message, "\n")[0],
				Date:    t,
			})
		}

		if len(ghCommits) < perPage {
			break
		}

		page++
		time.Sleep(100 * time.Millisecond)
	}

	return allCommits, nil
}

// ListCommitsPage fetches a single page of commits (newest first) from
// the given branch. perPage is clamped to [1,100]; page is 1-indexed.
// hasMore reports whether another page likely exists (page was full).
// A *RateLimitError is returned when the API rate limit is exhausted.
func (c *Client) ListCommitsPage(ctx context.Context, token, owner, repo, branch string, page, perPage int) (commits []*Commit, hasMore bool, err error) {
	_ = ctx
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 1
	}
	if perPage > 100 {
		perPage = 100
	}

	u := fmt.Sprintf("%s/repos/%s/%s/commits?per_page=%d&page=%d&sha=%s",
		c.APIURL, owner, repo, perPage, page, url.QueryEscape(branch))
	resp, err := c.doRequest("GET", u, token, nil)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()

	var ghCommits []ghCommit
	if err := json.NewDecoder(resp.Body).Decode(&ghCommits); err != nil {
		return nil, false, fmt.Errorf("decoding commits: %w", err)
	}

	for _, gc := range ghCommits {
		t, _ := time.Parse(time.RFC3339, gc.Commit.Committer.Date)
		commits = append(commits, &Commit{
			SHA:     gc.SHA,
			Message: strings.Split(gc.Commit.Message, "\n")[0],
			Date:    t,
		})
	}
	return commits, len(ghCommits) == perPage, nil
}

func ParseCommitType(msg string) string {
	// Parse conventional commit prefix like "feat:", "fix:", "feat(scope):", etc.
	msg = strings.TrimSpace(msg)
	before, _, ok := strings.Cut(msg, ":")
	if !ok {
		return "other"
	}
	prefix := before
	// Handle scope like "feat(scope)"
	if parenIdx := strings.Index(prefix, "("); parenIdx != -1 {
		prefix = prefix[:parenIdx]
	}
	switch strings.ToLower(strings.TrimSpace(prefix)) {
	case "feat", "feature":
		return "feat"
	case "fix", "bugfix", "bug":
		return "fix"
	case "docs", "documentation":
		return "docs"
	case "chore", "refactor", "test", "style", "perf", "ci", "build", "revert":
		return "chore"
	default:
		return "other"
	}
}

func (c *Client) GetLatestRelease(token, owner, repo string) (*Release, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/latest", c.APIURL, owner, repo)
	resp, err := c.doRequest("GET", url, token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var r ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("decoding release: %w", err)
	}

	t, _ := time.Parse(time.RFC3339, r.PublishedAt)
	return &Release{
		TagName:     r.TagName,
		Name:        r.Name,
		PublishedAt: t,
	}, nil
}

func (c *Client) ListReleases(token, owner, repo string) ([]*Release, error) {
	var all []*Release
	page := 1
	for {
		url := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=100&page=%d", c.APIURL, owner, repo, page)
		resp, err := c.doRequest("GET", url, token, nil)
		if err != nil {
			return all, err
		}

		var ghReleases []ghRelease
		if err := json.NewDecoder(resp.Body).Decode(&ghReleases); err != nil {
			resp.Body.Close()
			return all, fmt.Errorf("decoding releases: %w", err)
		}
		resp.Body.Close()

		if len(ghReleases) == 0 {
			break
		}
		for _, r := range ghReleases {
			t, _ := time.Parse(time.RFC3339, r.PublishedAt)
			all = append(all, &Release{TagName: r.TagName, Name: r.Name, PublishedAt: t})
		}
		page++
	}
	return all, nil
}

func (c *Client) GetLatestWorkflowRun(token, owner, repo, branch string) (*WorkflowRun, error) {
	u := fmt.Sprintf("%s/repos/%s/%s/actions/runs?per_page=1&status=completed", c.APIURL, owner, repo)
	if branch != "" {
		u += "&branch=" + url.QueryEscape(branch)
	}
	resp, err := c.doRequest("GET", u, token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var runs ghWorkflowRuns
	if err := json.NewDecoder(resp.Body).Decode(&runs); err != nil {
		return nil, fmt.Errorf("decoding workflow runs: %w", err)
	}
	if len(runs.WorkflowRuns) == 0 {
		return nil, fmt.Errorf("no completed workflow runs found")
	}

	run := runs.WorkflowRuns[0]
	return &WorkflowRun{ID: run.ID, Conclusion: run.Conclusion}, nil
}

func (c *Client) GetWorkflowRuns(token, owner, repo, branch string, perPage int) ([]*WorkflowRun, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/actions/runs?per_page=%d&branch=%s", c.APIURL, owner, repo, perPage, branch)
	resp, err := c.doRequest("GET", url, token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var runs ghWorkflowRuns
	if err := json.NewDecoder(resp.Body).Decode(&runs); err != nil {
		return nil, fmt.Errorf("decoding workflow runs: %w", err)
	}
	var result []*WorkflowRun
	for _, r := range runs.WorkflowRuns {
		result = append(result, &WorkflowRun{ID: r.ID, Status: r.Status, Conclusion: r.Conclusion, HeadSHA: r.HeadSHA})
	}
	return result, nil
}

// GetWorkflowRunsForRepo returns the most recent workflow runs for a
// repository without filtering by branch, so runs can be matched to pull
// requests by head SHA. Returns an empty slice (not an error) when no runs
// exist.
func (c *Client) GetWorkflowRunsForRepo(token, owner, repo string, perPage int) ([]*WorkflowRun, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/actions/runs?per_page=%d", c.APIURL, owner, repo, perPage)
	resp, err := c.doRequest("GET", url, token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var runs ghWorkflowRuns
	if err := json.NewDecoder(resp.Body).Decode(&runs); err != nil {
		return nil, fmt.Errorf("decoding workflow runs: %w", err)
	}
	var result []*WorkflowRun
	for _, r := range runs.WorkflowRuns {
		result = append(result, &WorkflowRun{ID: r.ID, Status: r.Status, Conclusion: r.Conclusion, HeadSHA: r.HeadSHA})
	}
	return result, nil
}

func (c *Client) ListPullRequests(token, owner, repo string) ([]*PullRequest, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls?state=open&per_page=50&sort=created&direction=desc", c.APIURL, owner, repo)
	resp, err := c.doRequest("GET", url, token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var ghPRs []ghPullRequest
	if err := json.NewDecoder(resp.Body).Decode(&ghPRs); err != nil {
		return nil, fmt.Errorf("decoding pull requests: %w", err)
	}

	var prs []*PullRequest
	for _, pr := range ghPRs {
		t, _ := time.Parse(time.RFC3339, pr.CreatedAt)
		prs = append(prs, &PullRequest{
			Number:         pr.Number,
			Title:          pr.Title,
			Author:         pr.User.Login,
			CreatedAt:      t,
			HTMLURL:        pr.HTMLURL,
			HeadRef:        pr.Head.Ref,
			BaseRef:        pr.Base.Ref,
			MergeableState: pr.MergeableState,
			HeadSHA:        pr.Head.SHA,
		})
	}
	return prs, nil
}

func (c *Client) ListRecentlyMergedPRs(token, owner, repo string) ([]*PullRequest, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls?state=closed&sort=updated&direction=desc&per_page=10", c.APIURL, owner, repo)
	resp, err := c.doRequest("GET", url, token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var ghPRs []ghPullRequest
	if err := json.NewDecoder(resp.Body).Decode(&ghPRs); err != nil {
		return nil, fmt.Errorf("decoding pull requests: %w", err)
	}

	var prs []*PullRequest
	for _, pr := range ghPRs {
		if pr.MergedAt == "" {
			continue
		}
		t, _ := time.Parse(time.RFC3339, pr.CreatedAt)
		prs = append(prs, &PullRequest{
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

type mergePRRequest struct {
	MergeMethod string `json:"merge_method"`
}

type mergePRResponse struct {
	Merged  bool   `json:"merged"`
	Message string `json:"message"`
}

func (c *Client) MergePullRequest(token, owner, repo string, prNumber int) (bool, string, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/merge", c.APIURL, owner, repo, prNumber)
	body := mergePRRequest{MergeMethod: "merge"}
	var buf strings.Builder
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return false, "", fmt.Errorf("encoding merge request: %w", err)
	}
	resp, err := c.doRequest("PUT", url, token, strings.NewReader(buf.String()))
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()

	var result mergePRResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, "", fmt.Errorf("decoding merge response: %w", err)
	}
	return result.Merged, result.Message, nil
}

type closePRRequest struct {
	State string `json:"state"`
}

// ClosePullRequest closes an open pull request by setting its state to
// "closed".
func (c *Client) ClosePullRequest(token, owner, repo string, prNumber int) error {
	url := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", c.APIURL, owner, repo, prNumber)
	body := closePRRequest{State: "closed"}
	var buf strings.Builder
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return fmt.Errorf("encoding close request: %w", err)
	}
	resp, err := c.doRequest("PATCH", url, token, strings.NewReader(buf.String()))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("closing pull request #%d: %s", prNumber, resp.Status)
	}
	return nil
}

func (c *Client) GetWorkflowStatus(token, owner, repo, branch string) (*WorkflowRun, error) {
	u := fmt.Sprintf("%s/repos/%s/%s/actions/runs?per_page=1&status=completed", c.APIURL, owner, repo)
	if branch != "" {
		u += "&branch=" + url.QueryEscape(branch)
	}
	resp, err := c.doRequest("GET", u, token, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var runs ghWorkflowRuns
	if err := json.NewDecoder(resp.Body).Decode(&runs); err != nil {
		return nil, fmt.Errorf("decoding workflow runs: %w", err)
	}
	if len(runs.WorkflowRuns) == 0 {
		return nil, fmt.Errorf("no workflow runs found")
	}

	run := runs.WorkflowRuns[0]
	return &WorkflowRun{
		ID:         run.ID,
		Conclusion: run.Conclusion,
	}, nil
}

// FindDependencyDashboard locates the Renovate Dependency Dashboard issue in a repository.
// It searches for an open issue with the title "Dependency Dashboard" and returns its number and body.
func (c *Client) FindDependencyDashboard(token, owner, repo string) (issueNumber int, body string, err error) {
	page := 1
	for {
		u := fmt.Sprintf("%s/repos/%s/%s/issues?state=open&per_page=100&page=%d", c.APIURL, owner, repo, page)
		resp, err := c.doRequest("GET", u, token, nil)
		if err != nil {
			return 0, "", err
		}

		var issues []ghIssue
		if err := json.NewDecoder(resp.Body).Decode(&issues); err != nil {
			resp.Body.Close()
			return 0, "", fmt.Errorf("decoding issues: %w", err)
		}
		resp.Body.Close()

		if len(issues) == 0 {
			break
		}

		for _, issue := range issues {
			if issue.Title == "Dependency Dashboard" {
				return issue.Number, issue.Body, nil
			}
		}

		page++
	}

	return 0, "", fmt.Errorf("no Dependency Dashboard issue found in %s/%s", owner, repo)
}

// UpdateIssueBody updates the body of a GitHub issue using the PATCH method.
func (c *Client) UpdateIssueBody(token, owner, repo string, issueNumber int, body string) error {
	u := fmt.Sprintf("%s/repos/%s/%s/issues/%d", c.APIURL, owner, repo, issueNumber)
	payload := struct {
		Body string `json:"body"`
	}{Body: body}

	var buf strings.Builder
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		return fmt.Errorf("encoding update request: %w", err)
	}

	resp, err := c.doRequest("PATCH", u, token, strings.NewReader(buf.String()))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return nil
}

// RebaseAllOpenPRs triggers Renovate to rebase all open PRs by checking the rebase-all checkbox
// in the Dependency Dashboard issue. It finds the issue, locates the checkbox marker, and updates it.
func (c *Client) RebaseAllOpenPRs(token, owner, repo string) error {
	issueNumber, body, err := c.FindDependencyDashboard(token, owner, repo)
	if err != nil {
		return err
	}

	const marker = "<!-- rebase-all-open-prs-checkbox -->"
	lines := strings.Split(body, "\n")
	found := false
	updated := false

	for i, line := range lines {
		if strings.Contains(line, marker) {
			found = true
			// Check if already checked
			if strings.Contains(line, "- [x]") || strings.Contains(line, "- [X]") {
				// Already checked, no-op
				return nil
			}
			// Replace unchecked checkbox with checked
			if strings.Contains(line, "- [ ]") {
				lines[i] = strings.Replace(line, "- [ ]", "- [x]", 1)
				updated = true
			}
			break
		}
	}

	if !found {
		return fmt.Errorf("rebase-all checkbox marker not found in Dependency Dashboard issue")
	}

	if !updated {
		return fmt.Errorf("could not update rebase-all checkbox in Dependency Dashboard issue")
	}

	newBody := strings.Join(lines, "\n")
	return c.UpdateIssueBody(token, owner, repo, issueNumber, newBody)
}
