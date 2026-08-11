package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"sync"
	"time"

	"gitlens/ent"
	"gitlens/ent/commitactivity"
	"gitlens/ent/event"
	"gitlens/ent/repository"
	"gitlens/ent/user"
	"gitlens/internal/github"
	"gitlens/internal/otel"
	"gitlens/internal/provider"
	"gitlens/internal/ws"
)

type Syncer struct {
	client    *ent.Client
	gh        *github.Client
	providers map[string]provider.Provider
	hub       *ws.Hub
	tmpl      *template.Template

	// repoLocks serializes CommitActivity writes (incremental upserts
	// vs. backfill absolute sets) per repository.
	repoLocks sync.Map // map[int]*sync.Mutex

	// bgSyncs dedupes background SyncOne runs per repo.
	bgSyncs sync.Map // map[int]struct{}
}

func NewSyncer(client *ent.Client, gh *github.Client, providers map[string]provider.Provider, hub *ws.Hub) *Syncer {
	return &Syncer{
		client:    client,
		gh:        gh,
		providers: providers,
		hub:       hub,
	}
}

// repoLock returns the per-repo mutex guarding CommitActivity writes.
func (s *Syncer) repoLock(repoID int) *sync.Mutex {
	v, _ := s.repoLocks.LoadOrStore(repoID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func (s *Syncer) SetTemplate(tmpl *template.Template) {
	s.tmpl = tmpl
}

func (s *Syncer) StartPeriodicSync(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.SyncAll(ctx)
		}
	}
}

func (s *Syncer) SyncAll(ctx context.Context) {
	users, err := s.client.User.Query().All(ctx)
	if err != nil {
		return
	}
	for _, u := range users {
		if !u.SyncedAt.IsZero() {
			elapsed := time.Since(u.SyncedAt)
			if elapsed < time.Duration(u.SyncIntervalMinutes)*time.Minute {
				continue
			}
		}
		s.syncUserRepos(ctx, u)
	}
}

func (s *Syncer) syncUserRepos(ctx context.Context, u *ent.User) {
	repos, err := s.client.Repository.Query().Where(repository.HasUserWith(user.ID(u.ID))).All(ctx)
	if err != nil {
		return
	}
	for _, r := range repos {
		s.SyncOne(ctx, r)
	}
	_, _ = s.client.User.UpdateOne(u).SetSyncedAt(time.Now()).Save(ctx)
}

// ProviderFor exposes getProvider for handlers (e.g. merge endpoints)
// that need provider-aware API calls.
func (s *Syncer) ProviderFor(u *ent.User, repo *ent.Repository) (provider.Provider, string) {
	return s.getProvider(u, repo)
}

// SyncOneBackground runs SyncOne in a goroutine with a background
// context, deduplicated per repo: a second call while a sync for the
// same repo is still in flight is a no-op.
func (s *Syncer) SyncOneBackground(repo *ent.Repository) {
	if _, loaded := s.bgSyncs.LoadOrStore(repo.ID, struct{}{}); loaded {
		return
	}
	go func() {
		defer s.bgSyncs.Delete(repo.ID)
		defer func() {
			if r := recover(); r != nil {
				log.Printf("background sync: panic for repo %d: %v", repo.ID, r)
			}
		}()
		s.SyncOne(context.Background(), repo)
	}()
}

// getProvider returns the appropriate Provider for the repo, plus the
// user's access token for that provider.
func (s *Syncer) getProvider(u *ent.User, repo *ent.Repository) (provider.Provider, string) {
	providerName := repo.Provider
	if providerName == "" {
		providerName = "github"
	}
	p, ok := s.providers[providerName]
	if !ok || p == nil {
		// Fall back to GitHub for back-compat
		p = s.providers["github"]
		providerName = "github"
	}
	var token string
	switch providerName {
	case "forgejo":
		token = u.ForgejoAccessToken
	default:
		token = u.AccessToken
	}
	return p, token
}

func (s *Syncer) SyncOne(ctx context.Context, repo *ent.Repository) *ent.Repository {
	u, err := repo.QueryUser().Only(ctx)
	if err != nil {
		return repo
	}

	p, token := s.getProvider(u, repo)

	// Snapshot before state for event detection
	beforeReleaseTag := repo.LatestReleaseTag
	beforeWorkflowStatus := repo.WorkflowStatus
	beforeReleaseConclusion := repo.LatestReleaseConclusion

	updated := s.client.Repository.UpdateOne(repo)

	s.syncCommits(ctx, p, token, u, repo, updated)
	s.syncRelease(ctx, p, token, u, repo, updated)
	s.syncWorkflows(ctx, p, token, repo, updated)
	s.syncPullRequests(ctx, p, token, repo, updated)

	if !repo.LatestCommitDate.IsZero() && !repo.LatestReleaseDate.IsZero() {
		leadHours := repo.LatestReleaseDate.Sub(repo.LatestCommitDate).Hours()
		if leadHours > 0 {
			updated.SetAvgLeadTimeHours(leadHours)
		}
	}

	updated.SetSyncedAt(time.Now())
	repo, err = updated.Save(ctx)
	if err != nil {
		return repo
	}

	s.recordEvents(ctx, u, repo, beforeReleaseTag, beforeWorkflowStatus, beforeReleaseConclusion)
	s.recordSnapshot(ctx, repo)

	// Kick off historical commit-activity backfill if it has never run
	// (or a previous run went stale). No-op when already running/done.
	if repo.BackfillStatus != "done" {
		s.MaybeStartBackfill(repo.ID)
	}

	if s.hub != nil {
		s.broadcastUpdate(repo, u)
	}
	return repo
}

func (s *Syncer) syncCommits(ctx context.Context, p provider.Provider, token string, u *ent.User, repo *ent.Repository, updated *ent.RepositoryUpdateOne) {
	// Derive the fetch window from the newest previously seen COMMIT
	// (not the SyncedAt wall clock) with a small overlap buffer, then
	// trim at the previously seen head SHA so refetched commits are
	// never counted twice.
	var since time.Time
	if !repo.LatestCommitDate.IsZero() {
		since = repo.LatestCommitDate.Add(-2 * time.Minute)
	}
	commits, err := p.GetCommitsSince(ctx, token, repo.Owner, repo.Name, repo.DefaultBranch, since, 500)
	if err != nil {
		log.Printf("Error fetching commits for %s: %v", repo.FullName, err)
		return
	}

	// Commits come back newest-first; everything from the previously
	// seen head SHA onward has already been counted.
	if repo.LatestCommitSha != "" {
		for i, c := range commits {
			if c.SHA == repo.LatestCommitSha {
				commits = commits[:i]
				break
			}
		}
	}

	total := repo.TotalCommitsFetched
	if repo.SyncedAt.IsZero() {
		total = len(commits)
	} else {
		total += len(commits)
	}
	updated.SetTotalCommitsFetched(total)

	if len(commits) > 0 {
		updated.SetLatestCommitSha(commits[0].SHA)
		updated.SetLatestCommitMessage(commits[0].Message)
		updated.SetLatestCommitDate(commits[0].Date)
	}

	var feat, fix, docs, chore, other int
	for _, c := range commits {
		switch github.ParseCommitType(c.Message) {
		case "feat":
			feat++
		case "fix":
			fix++
		case "docs":
			docs++
		case "chore":
			chore++
		default:
			other++
		}
	}
	if repo.SyncedAt.IsZero() {
		updated.SetFeatCount(feat)
		updated.SetFixCount(fix)
		updated.SetDocsCount(docs)
		updated.SetChoreCount(chore)
		updated.SetOtherCommitCount(other)
	} else {
		updated.SetFeatCount(repo.FeatCount + feat)
		updated.SetFixCount(repo.FixCount + fix)
		updated.SetDocsCount(repo.DocsCount + docs)
		updated.SetChoreCount(repo.ChoreCount + chore)
		updated.SetOtherCommitCount(repo.OtherCommitCount + other)
	}

	// Bucket commits by day and upsert activity rows (non-fatal on
	// error). These are strictly-new commits (SHA-trimmed above), so
	// incrementing is safe. Serialized with backfill writes.
	dayCounts := bucketCommitsByDay(commits)
	if len(dayCounts) > 0 {
		lock := s.repoLock(repo.ID)
		lock.Lock()
		s.upsertCommitActivity(ctx, repo.ID, dayCounts)
		lock.Unlock()
	}
}

// bucketCommitsByDay groups commits by UTC calendar day and returns a map of date string -> count.
func bucketCommitsByDay(commits []*github.Commit) map[string]int {
	dayCounts := make(map[string]int, len(commits))
	for _, c := range commits {
		day := c.Date.UTC().Format("2006-01-02")
		dayCounts[day]++
	}
	return dayCounts
}

// upsertCommitActivity upserts CommitActivity rows for the given repo and day counts.
// Existing rows are incremented; new rows are created. Errors are logged but non-fatal.
func (s *Syncer) upsertCommitActivity(ctx context.Context, repoID int, dayCounts map[string]int) {
	for dayStr, count := range dayCounts {
		day, err := time.Parse("2006-01-02", dayStr)
		if err != nil {
			log.Printf("Error parsing date %q: %v", dayStr, err)
			continue
		}

		existing, err := s.client.CommitActivity.Query().
			Where(commitactivity.RepoID(repoID), commitactivity.Date(day)).
			Only(ctx)
		if err != nil {
			if !ent.IsNotFound(err) {
				log.Printf("Error querying commit activity for repo %d, date %s: %v", repoID, dayStr, err)
				continue
			}
			// Create new row
			_, err = s.client.CommitActivity.Create().
				SetRepoID(repoID).
				SetDate(day).
				SetCommitCount(count).
				Save(ctx)
			if err != nil {
				log.Printf("Error creating commit activity for repo %d, date %s: %v", repoID, dayStr, err)
			}
			continue
		}

		// Update existing row
		_, err = existing.Update().AddCommitCount(count).Save(ctx)
		if err != nil {
			log.Printf("Error updating commit activity for repo %d, date %s: %v", repoID, dayStr, err)
		}
	}
}

func (s *Syncer) syncRelease(ctx context.Context, p provider.Provider, token string, u *ent.User, repo *ent.Repository, updated *ent.RepositoryUpdateOne) {
	releases, err := p.ListReleases(ctx, token, repo.Owner, repo.Name)
	if err != nil {
		log.Printf("Error fetching releases for %s: %v", repo.FullName, err)
		return
	}
	updated.SetReleaseCount(len(releases))

	if len(releases) > 0 {
		updated.SetLatestReleaseTag(releases[0].TagName)
		updated.SetLatestReleaseName(releases[0].Name)
		updated.SetLatestReleaseDate(releases[0].PublishedAt)

		// Get the latest completed workflow run for the release (try tag, then default branch)
		run, err := p.GetLatestWorkflowRun(ctx, token, repo.Owner, repo.Name, releases[0].TagName)
		if err != nil {
			run, err = p.GetLatestWorkflowRun(ctx, token, repo.Owner, repo.Name, repo.DefaultBranch)
		}
		if err == nil {
			updated.SetLatestReleaseConclusion(run.Conclusion)
		} else {
			updated.SetLatestReleaseConclusion("unknown")
		}
	}
}

func (s *Syncer) syncPullRequests(ctx context.Context, p provider.Provider, token string, repo *ent.Repository, updated *ent.RepositoryUpdateOne) {
	prs, err := p.ListPullRequests(ctx, token, repo.Owner, repo.Name)
	if err != nil {
		log.Printf("Error fetching pull requests for %s: %v", repo.FullName, err)
		return
	}

	updated.SetOpenPrCount(len(prs))

	// Build status per PR (GitHub only): match workflow runs to PRs by
	// head commit SHA. Forgejo repos skip this (Forgejo Actions is
	// opt-in) and show no badge.
	var buildByHeadSHA map[string]string
	if repo.Provider == "github" && len(prs) > 0 {
		runs, err := s.gh.GetWorkflowRunsForRepo(token, repo.Owner, repo.Name, 30)
		if err != nil {
			log.Printf("Error fetching workflow runs for %s: %v", repo.FullName, err)
		} else {
			buildByHeadSHA = make(map[string]string, len(runs))
			for _, r := range runs {
				if r.HeadSHA == "" {
					continue
				}
				if _, ok := buildByHeadSHA[r.HeadSHA]; ok {
					continue // keep the first (most recent) run per head
				}
				buildByHeadSHA[r.HeadSHA] = workflowRunBuildStatus(r)
			}
		}
	}

	if len(prs) > 0 {
		type prSummary struct {
			Number         int    `json:"n"`
			Title          string `json:"t"`
			Author         string `json:"a"`
			CreatedAt      string `json:"c"`
			HTMLURL        string `json:"h"`
			HeadRef        string `json:"hr"`
			BaseRef        string `json:"br"`
			MergeableState string `json:"ms,omitempty"`
			BuildStatus    string `json:"bs,omitempty"`
		}
		var summaries []prSummary
		for _, pr := range prs {
			summaries = append(summaries, prSummary{
				Number:         pr.Number,
				Title:          pr.Title,
				Author:         pr.Author,
				CreatedAt:      pr.CreatedAt.Format(time.RFC3339),
				HTMLURL:        pr.HTMLURL,
				HeadRef:        pr.HeadRef,
				BaseRef:        pr.BaseRef,
				MergeableState: pr.MergeableState,
				BuildStatus:    buildByHeadSHA[pr.HeadSHA],
			})
		}
		data, err := json.Marshal(summaries)
		if err == nil {
			updated.SetPullRequests(string(data))
		}
	} else {
		updated.SetPullRequests("[]")
	}
}

// workflowRunBuildStatus reduces a workflow run to the coarse build status
// used by the PR overview badge. "in_progress" covers queued/running runs and
// conclusions we don't special-case.
func workflowRunBuildStatus(r *github.WorkflowRun) string {
	if r.Status != "completed" {
		return "in_progress"
	}
	switch r.Conclusion {
	case "success", "failure", "neutral", "cancelled", "timed_out", "action_required", "stale":
		return r.Conclusion
	default:
		return "in_progress"
	}
}

func (s *Syncer) syncWorkflows(ctx context.Context, p provider.Provider, token string, repo *ent.Repository, updated *ent.RepositoryUpdateOne) {
	// Forgejo Actions is opt-in; we report "unknown" without an HTTP call.
	if repo.Provider == "forgejo" {
		updated.SetWorkflowStatus("unknown")
		return
	}
	// The github client has GetWorkflowRuns which is not part of the
	// Provider interface (GitHub-specific). Fall back to s.gh for now.
	runs, err := s.gh.GetWorkflowRuns(token, repo.Owner, repo.Name, repo.DefaultBranch, 30)
	if err != nil {
		log.Printf("Error fetching workflows for %s: %v", repo.FullName, err)
		return
	}

	var success, failure int
	for _, r := range runs {
		switch r.Conclusion {
		case "success":
			success++
		case "failure":
			failure++
		}
	}
	updated.SetWorkflowSuccessCount(success)
	updated.SetWorkflowFailureCount(failure)

	if len(runs) > 0 {
		updated.SetWorkflowStatus(runs[0].Conclusion)
		updated.SetWorkflowRunID(runs[0].ID)
	}
}

func (s *Syncer) SyncOneByGithubID(ctx context.Context, githubID int64) {
	r, err := s.client.Repository.Query().Where(repository.GithubID(githubID)).Only(ctx)
	if err != nil {
		return
	}
	s.SyncOne(ctx, r)
}

func (s *Syncer) recordEvents(ctx context.Context, u *ent.User, repo *ent.Repository, beforeReleaseTag, beforeWorkflowStatus, beforeReleaseConclusion string) {
	otel.TraceDBQuery(ctx, "event_record", func(ctx context.Context) (struct{}, error) {
		s.recordReleaseEvent(ctx, repo, beforeReleaseTag, beforeReleaseConclusion)
		s.recordWorkflowFailureEvent(ctx, repo, beforeWorkflowStatus)
		s.recordPRMergeEvents(ctx, u, repo)
		return struct{}{}, nil
	})
}

func (s *Syncer) recordReleaseEvent(ctx context.Context, repo *ent.Repository, beforeTag, beforeConclusion string) {
	if repo.LatestReleaseTag == "" || repo.LatestReleaseTag == beforeTag {
		return
	}
	// Check if we already recorded this release event
	exists, _ := s.client.Event.Query().
		Where(event.RepoID(repo.ID)).
		Where(event.TypeEQ(event.TypeRelease)).
		Where(event.TitleEQ(repo.LatestReleaseTag)).
		Exist(ctx)
	if exists {
		return
	}

	meta, _ := json.Marshal(map[string]string{
		"conclusion": repo.LatestReleaseConclusion,
	})
	s.client.Event.Create().
		SetRepoID(repo.ID).
		SetType(event.TypeRelease).
		SetTitle(repo.LatestReleaseTag).
		SetURL(fmt.Sprintf("%s/releases/tag/%s", repo.HTMLURL, repo.LatestReleaseTag)).
		SetMetadata(string(meta)).
		SetTimestamp(repo.LatestReleaseDate).
		Save(ctx)
}

func (s *Syncer) recordWorkflowFailureEvent(ctx context.Context, repo *ent.Repository, beforeStatus string) {
	status := repo.WorkflowStatus
	if status != "failure" && status != "cancelled" {
		return
	}
	if status == beforeStatus {
		return
	}
	// Check if we already recorded this failure
	exists, _ := s.client.Event.Query().
		Where(event.RepoID(repo.ID)).
		Where(event.TypeEQ(event.TypeWorkflowFailure)).
		Where(event.TitleContains("build #")).
		Exist(ctx)
	if !exists || beforeStatus == "" {
		meta, _ := json.Marshal(map[string]any{
			"run_id": repo.WorkflowRunID,
			"status": status,
		})
		s.client.Event.Create().
			SetRepoID(repo.ID).
			SetType(event.TypeWorkflowFailure).
			SetTitle(fmt.Sprintf("CI %s — build #%d", status, repo.WorkflowRunID)).
			SetURL(fmt.Sprintf("%s/actions/runs/%d", repo.HTMLURL, repo.WorkflowRunID)).
			SetMetadata(string(meta)).
			SetTimestamp(time.Now()).
			Save(ctx)
	}
}

func (s *Syncer) recordPRMergeEvents(ctx context.Context, u *ent.User, repo *ent.Repository) {
	p, token := s.getProvider(u, repo)
	mergedPRs, err := p.ListRecentlyMergedPRs(ctx, token, repo.Owner, repo.Name)
	if err != nil {
		log.Printf("Error fetching merged PRs for %s: %v", repo.FullName, err)
		return
	}
	for _, pr := range mergedPRs {
		title := fmt.Sprintf("#%d %s", pr.Number, pr.Title)
		exists, _ := s.client.Event.Query().
			Where(event.RepoID(repo.ID)).
			Where(event.TypeEQ(event.TypePrMerge)).
			Where(event.TitleEQ(title)).
			Exist(ctx)
		if exists {
			continue
		}
		meta, _ := json.Marshal(map[string]any{
			"author":   pr.Author,
			"number":   pr.Number,
			"base_ref": pr.BaseRef,
		})
		s.client.Event.Create().
			SetRepoID(repo.ID).
			SetType(event.TypePrMerge).
			SetTitle(title).
			SetURL(pr.HTMLURL).
			SetMetadata(string(meta)).
			SetTimestamp(pr.CreatedAt).
			Save(ctx)
	}
}

func (s *Syncer) recordSnapshot(ctx context.Context, repo *ent.Repository) {
	_, err := otel.TraceDBQuery(ctx, "snapshot_record", func(ctx context.Context) (*ent.MetricSnapshot, error) {
		return s.client.MetricSnapshot.Create().
			SetRepoID(repo.ID).
			SetTimestamp(time.Now()).
			SetFeatCount(repo.FeatCount).
			SetFixCount(repo.FixCount).
			SetDocsCount(repo.DocsCount).
			SetChoreCount(repo.ChoreCount).
			SetOtherCommitCount(repo.OtherCommitCount).
			SetTotalCommitsFetched(repo.TotalCommitsFetched).
			SetReleaseCount(repo.ReleaseCount).
			SetAvgLeadTimeHours(repo.AvgLeadTimeHours).
			SetWorkflowSuccessCount(repo.WorkflowSuccessCount).
			SetWorkflowFailureCount(repo.WorkflowFailureCount).
			SetWorkflowStatus(repo.WorkflowStatus).
			Save(ctx)
	})
	if err != nil {
		log.Printf("Error recording metric snapshot for %s: %v", repo.FullName, err)
	}
}

func (s *Syncer) broadcastUpdate(repo *ent.Repository, u *ent.User) {
	if s.tmpl == nil {
		return
	}
	var buf bytes.Buffer
	err := s.tmpl.ExecuteTemplate(&buf, "repo_card", repo)
	if err != nil {
		return
	}
	// Wrap in hx-swap-oob so HTMX replaces only the targeted repo card
	// instead of replacing the entire #repo-grid innerHTML.
	msg := fmt.Sprintf(`<div id="repo-card-%d" hx-swap-oob="outerHTML">%s</div>`, repo.ID, buf.String())
	s.hub.BroadcastToUser(int64(u.ID), []byte(msg))
}
