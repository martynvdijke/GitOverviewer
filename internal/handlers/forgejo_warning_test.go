package handlers

import (
	"context"
	"testing"

	"gitlens/ent"
)

// TestComputeForgejoWarning covers the cross-provider warning banner logic:
// a GitHub-tracked repo with no counterpart on the user's connected Forgejo
// instance should trigger the warning. (Manual-smoke task 11 from the
// add-forgejo-integration change, covered via unit tests.)

func TestComputeForgejoWarning_NoForgejoConnected(t *testing.T) {
	h := &SettingsHandler{}
	u := &ent.User{ForgejoID: 0}
	repos := []*ent.Repository{
		{Provider: "github", FullName: "martynvdijke/gitlens"},
	}
	w := h.computeForgejoWarning(context.Background(), u, repos)
	if w.Show {
		t.Fatal("expected no warning when Forgejo is not connected")
	}
}

func TestComputeForgejoWarning_AllReposMirrored(t *testing.T) {
	h := &SettingsHandler{}
	u := &ent.User{ForgejoID: 7, ForgejoURL: "https://git.example.com"}
	repos := []*ent.Repository{
		{Provider: "github", FullName: "martynvdijke/gitlens"},
		{Provider: "forgejo", FullName: "martynvdijke/gitlens", ForgejoFullName: "martynvdijke/gitlens"},
		{Provider: "github", FullName: "org/other"},
		{Provider: "forgejo", FullName: "org/other", ForgejoFullName: "org/other"},
	}
	w := h.computeForgejoWarning(context.Background(), u, repos)
	if w.Show {
		t.Fatalf("expected no warning when all GitHub repos have Forgejo counterparts, got: %+v", w)
	}
}

func TestComputeForgejoWarning_MissingCounterpart(t *testing.T) {
	h := &SettingsHandler{}
	u := &ent.User{ForgejoID: 7, ForgejoURL: "https://git.example.com"}
	repos := []*ent.Repository{
		{Provider: "github", FullName: "martynvdijke/gitlens"},
		{Provider: "github", FullName: "org/gh-only"},
		{Provider: "forgejo", FullName: "martynvdijke/gitlens", ForgejoFullName: "martynvdijke/gitlens"},
	}
	w := h.computeForgejoWarning(context.Background(), u, repos)
	if !w.Show {
		t.Fatal("expected warning when a GitHub repo has no Forgejo counterpart")
	}
	if w.TotalMissing != 1 {
		t.Fatalf("expected 1 missing repo, got %d", w.TotalMissing)
	}
	if len(w.MissingRepos) != 1 || w.MissingRepos[0] != "org/gh-only" {
		t.Fatalf("expected missing repo 'org/gh-only', got: %v", w.MissingRepos)
	}
	if w.ForgejoURL != "https://git.example.com" {
		t.Fatalf("expected ForgejoURL, got %q", w.ForgejoURL)
	}
	if w.Capped {
		t.Fatal("expected not capped")
	}
}

func TestComputeForgejoWarning_NoForgejoReposAtAll(t *testing.T) {
	h := &SettingsHandler{}
	u := &ent.User{ForgejoID: 7, ForgejoURL: "https://git.example.com"}
	repos := []*ent.Repository{
		{Provider: "github", FullName: "martynvdijke/gitlens"},
	}
	w := h.computeForgejoWarning(context.Background(), u, repos)
	if w.Show {
		t.Fatal("expected no warning when the user has zero Forgejo-tracked repos (setup still in progress)")
	}
}

func TestComputeForgejoWarning_DismissedRepo(t *testing.T) {
	h := &SettingsHandler{}
	u := &ent.User{
		ForgejoID:                  7,
		ForgejoURL:                 "https://git.example.com",
		DismissedForgejoWarningFor: `["org/gh-only"]`,
	}
	repos := []*ent.Repository{
		{Provider: "github", FullName: "martynvdijke/gitlens"},
		{Provider: "github", FullName: "org/gh-only"},
		{Provider: "forgejo", FullName: "martynvdijke/gitlens", ForgejoFullName: "martynvdijke/gitlens"},
	}
	w := h.computeForgejoWarning(context.Background(), u, repos)
	if w.Show {
		t.Fatalf("expected no warning after dismissal, got: %+v", w)
	}
}

func TestComputeForgejoWarning_CappedAtTen(t *testing.T) {
	h := &SettingsHandler{}
	u := &ent.User{ForgejoID: 7, ForgejoURL: "https://git.example.com"}
	var repos []*ent.Repository
	// 12 GitHub-only repos, plus one mirrored repo on Forgejo.
	repos = append(repos, &ent.Repository{Provider: "forgejo", FullName: "org/mirrored", ForgejoFullName: "org/mirrored"})
	for i := 0; i < 12; i++ {
		repos = append(repos, &ent.Repository{Provider: "github", FullName: "org/repo" + string(rune('a'+i))})
	}
	w := h.computeForgejoWarning(context.Background(), u, repos)
	if !w.Show {
		t.Fatal("expected warning")
	}
	if w.TotalMissing != 12 {
		t.Fatalf("expected 12 total missing, got %d", w.TotalMissing)
	}
	if len(w.MissingRepos) != 10 {
		t.Fatalf("expected 10 displayed repos when capped, got %d", len(w.MissingRepos))
	}
	if !w.Capped {
		t.Fatal("expected capped flag")
	}
}

func TestComputeForgejoWarning_CaseInsensitiveMatch(t *testing.T) {
	h := &SettingsHandler{}
	u := &ent.User{ForgejoID: 7, ForgejoURL: "https://git.example.com"}
	repos := []*ent.Repository{
		{Provider: "github", FullName: "Org/Repo"},
		{Provider: "forgejo", FullName: "Org/Repo", ForgejoFullName: "org/repo"},
	}
	w := h.computeForgejoWarning(context.Background(), u, repos)
	if w.Show {
		t.Fatalf("expected case-insensitive match to suppress warning, got: %+v", w)
	}
}
