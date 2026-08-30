package main

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"gitlens/ent"
	"gitlens/ent/migrate"
	"gitlens/ent/repository"
	"gitlens/ent/user"
	"gitlens/internal/deploy"
	"gitlens/internal/forgejo"
	"gitlens/internal/github"
	"gitlens/internal/gotify"
	"gitlens/internal/telegram"
	"gitlens/internal/handlers"
	"gitlens/internal/middleware"
	"gitlens/internal/otel"
	"gitlens/internal/provider"
	"gitlens/internal/sync"
	"gitlens/internal/ws"

	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
)

//go:embed views/*.html
var viewsFS embed.FS

var Version = "dev" // overridden by ldflags at build time

func main() {
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "gitlens.db"
	}
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?_journal_mode=wal&_fk=1")
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := ent.NewClient(ent.Driver(drv))
	defer client.Close()

	// One-shot dedup before adding the composite unique index.
	// See ent/migrate/dedupe.go.
	if err := migrate.DedupeRepositories(context.Background(), drv); err != nil {
		log.Fatalf("Failed to dedupe repositories: %v", err)
	}

	if err := client.Schema.Create(context.Background(), migrate.WithDropIndex(true)); err != nil {
		log.Fatalf("Failed to run migrations: %v", err)
	}

	// Promote all existing users to admin. The first-user-auto-admin logic in
	// the auth handlers only fires at registration, so users created before
	// that (or by earlier releases) would never see the admin sections.
	// This app is single-admin by design, so everyone is an admin.
	if n, err := client.User.Update().SetIsAdmin(true).Save(context.Background()); err != nil {
		log.Fatalf("Failed to promote users to admin: %v", err)
	} else if n > 0 {
		log.Printf("Promoted %d user(s) to admin", n)
	}

	sessionStore := middleware.NewSessionStore(db)

	otelManager := otel.NewManager(client)

	ghClient := github.NewClient(
		os.Getenv("GITHUB_CLIENT_ID"),
		os.Getenv("GITHUB_CLIENT_SECRET"),
	)

	// Provider map: keyed by canonical name ("github", "forgejo"). New
	// code reads from this map. Existing handlers also accept *github.Client
	// directly for back-compat and will be migrated in follow-ups.
	providers := map[string]provider.Provider{
		"github": provider.NewGitHubAdapter(ghClient),
	}

	// Forgejo is opt-in. Only register the provider if the env vars
	// are configured. If FORGEJO_CLIENT_ID is set but the secret is
	// missing we fail fast to avoid confusing "403 Forbidden" errors
	// at login time.
	fjID := os.Getenv("FORGEJO_CLIENT_ID")
	fjSecret := os.Getenv("FORGEJO_CLIENT_SECRET")
	if fjID != "" && fjSecret == "" {
		log.Fatalf("FORGEJO_CLIENT_ID is set but FORGEJO_CLIENT_SECRET is missing")
	}
	if fjID != "" && fjSecret != "" {
		fj := forgejo.NewClient(
			fjID,
			fjSecret,
			os.Getenv("FORGEJO_DEFAULT_URL"), // may be "" — picked at login time
		)
		providers["forgejo"] = provider.NewForgejoAdapter(fj)
		log.Println("Forgejo provider registered")
	}

	hub := ws.NewHub()
	go hub.Run()

	syncer := sync.NewSyncer(client, ghClient, providers, hub)

	tmpl := template.Must(template.New("").Funcs(template.FuncMap{
		"appVersion": func() string { return Version },
		"shortSHA": func(s string) string {
			if len(s) > 7 {
				return s[:7]
			}
			return s
		},
		"formatTime": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.Format("Jan 2, 2006 15:04")
		},
		"truncate": func(s string, n int) string {
			runes := []rune(s)
			if len(runes) > n {
				return string(runes[:n]) + "..."
			}
			return s
		},
		"workflowIcon": func(status string) string {
			switch status {
			case "success":
				return "\u2705"
			case "failure":
				return "\u274C"
			case "neutral", "skipped":
				return "\u23FA"
			default:
				return "\u2753"
			}
		},
		"workflowLabel": func(status string) string {
			switch status {
			case "success":
				return "Passing"
			case "failure":
				return "Failing"
			case "neutral":
				return "Neutral"
			case "skipped":
				return "Skipped"
			case "cancelled":
				return "Cancelled"
			default:
				return "Unknown"
			}
		},
		"hasWorkflowRun": func(status string) bool {
			return status != "" && status != "unknown"
		},
		"prBuildClass": func(status string) string {
			switch status {
			case "success":
				return "bg-success"
			case "failure":
				return "bg-danger"
			case "in_progress", "action_required":
				return "bg-warning text-dark"
			default:
				return "bg-secondary"
			}
		},
		"prBuildLabel": func(status string) string {
			switch status {
			case "success":
				return "Passing"
			case "failure":
				return "Failing"
			case "in_progress":
				return "Running"
			case "action_required":
				return "Action needed"
			case "neutral":
				return "Neutral"
			case "cancelled":
				return "Cancelled"
			case "timed_out":
				return "Timed out"
			case "stale":
				return "Stale"
			default:
				return "Unknown"
			}
		},
		"prBuildRerunnable": func(status string) bool {
			switch status {
			case "failure", "cancelled", "timed_out", "action_required", "stale":
				return true
			}
			return false
		},
		"printf": func(format string, args ...any) string {
			return fmt.Sprintf(format, args...)
		},
		"releaseIcon": func(conclusion string) string {
			switch conclusion {
			case "success":
				return "\u2705"
			case "failure":
				return "\u274C"
			default:
				return "\u2753"
			}
		},
		"releaseLabel": func(conclusion string) string {
			switch conclusion {
			case "success":
				return "Passing"
			case "failure":
				return "Failing"
			default:
				return "Unknown"
			}
		},
		"hasReleaseConclusion": func(s string) bool {
			return s != "" && s != "unknown"
		},
		"contains": func(s, substr string) bool {
			return strings.Contains(s, substr)
		},
		"sub": func(a, b int) int {
			return a - b
		},
		"timeSince": func(t time.Time) string {
			d := time.Since(t)
			switch {
			case d < time.Minute:
				return "just now"
			case d < time.Hour:
				m := int(d.Minutes())
				if m == 1 {
					return "1 minute ago"
				}
				return fmt.Sprintf("%d minutes ago", m)
			case d < 24*time.Hour:
				h := int(d.Hours())
				if h == 1 {
					return "1 hour ago"
				}
				return fmt.Sprintf("%d hours ago", h)
			default:
				days := int(d.Hours() / 24)
				if days == 1 {
					return "1 day ago"
				}
				return fmt.Sprintf("%d days ago", days)
			}
		},
		"eventIcon": func(eventType string) template.HTML {
			switch eventType {
			case "release":
				return template.HTML(`<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="#3fb950" stroke-width="2"><polyline points="23 6 13.5 15.5 8.5 10.5 1 18"/><polyline points="17 6 23 6 23 12"/></svg>`)
			case "workflow_failure":
				return template.HTML(`<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="#f85149" stroke-width="2"><circle cx="12" cy="12" r="10"/><line x1="15" y1="9" x2="9" y2="15"/><line x1="9" y1="9" x2="15" y2="15"/></svg>`)
			case "pr_merge":
				return template.HTML(`<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="#58a6ff" stroke-width="2"><circle cx="18" cy="18" r="3"/><circle cx="6" cy="6" r="3"/><path d="M6 21V9c0 2 2 3 6 3s6 1 6 3v3"/></svg>`)
			default:
				return ""
			}
		},
	}).ParseFS(viewsFS, "views/*.html"))
	syncer.SetTemplate(tmpl)

	authHandler := handlers.NewAuthHandler(client, sessionStore, ghClient, providers)
	dashHandler := handlers.NewDashboardHandler(client, sessionStore, ghClient, syncer)
	settingsHandler := handlers.NewSettingsHandler(client, sessionStore, ghClient, providers, syncer)
	apiTokenHandler := handlers.NewApiTokenHandler(client)
	chartHandler := handlers.NewChartHandler(client)
	badgeHandler := handlers.NewBadgeHandler(client)
	trmnlHandler := handlers.NewTRMNLSummaryHandler(client)
	gitHubAppHandler := handlers.NewGitHubAppHandler(client)
	webhookHandler := handlers.NewWebhookHandler(client, syncer, os.Getenv("GITHUB_WEBHOOK_SECRET"))
	// Deploy subsystem: release webhook → Docker pull/restart + Gotify notification.
	// Safe default: no deploy targets → deployer is nil, release events no-op.
	if targets, err := deploy.LoadAllTargets(); err != nil {
		log.Printf("Deploy: error loading targets: %v (deploy disabled)", err)
	} else if len(targets) > 0 {
		d := deploy.NewDeployer()
		// Resolve targets per release event so label-based discovery picks up
		// newly added containers without a restart.
		webhookHandler.SetDeployer(func(context.Context) ([]deploy.Target, error) {
			return deploy.LoadAllTargets()
		}, d, nil)
		gitHubAppHandler.SetDeployEnabled(true)
		log.Printf("Deploy: %d target(s) configured, backend=%s", len(targets), deploy.DeployBackend())
	}
	deployHandler := handlers.NewDeployHandler(false)
	feedHandler := handlers.NewFeedHandler(client)
	trendsHandler := handlers.NewTrendsHandler(client)
	yearOverviewHandler := handlers.NewYearOverviewHandler(client, syncer)
	adminHandler := handlers.NewAdminHandler(client, otelManager)

	// Push notifications: admin-panel settings take precedence over env vars
	// (Gotify only). reloadChannels re-reads the DB so an admin save applies
	// live (no restart), and updates the deploy webhook notifier, the deploy
	// tab indicator, and every configured channel client.
	reloadChannels := func() (*gotify.Client, *telegram.Client) {
		g := gotify.New() // env fallback
		var t *telegram.Client
		if cfg, err := client.AdminConfig.Get(context.Background(), 1); err == nil {
			if dbg := gotify.NewWith(cfg.GotifyURL, cfg.GotifyToken); dbg != nil {
				g = dbg
			}
			t = telegram.NewWith(cfg.TelegramBotToken, cfg.TelegramChatID)
		}
		webhookHandler.SetNotifiers(g, t)
		deployHandler.SetGotifyOn(g != nil)
		return g, t
	}
	adminHandler.SetGotifyReload(func() { reloadChannels() })
	adminHandler.SetTelegramReload(func() { reloadChannels() })
	adminHandler.SetGotifyTest(func(ctx context.Context) error {
		g, _ := reloadChannels()
		if g == nil {
			return handlers.ErrGotifyNotConfigured
		}
		return g.Send(ctx, "[GitLens test]", "This is a GitLens Gotify test notification.", 1)
	})
	adminHandler.SetTelegramTest(func(ctx context.Context) error {
		_, t := reloadChannels()
		if t == nil {
			return handlers.ErrTelegramNotConfigured
		}
		return t.Send(ctx, "[GitLens test]", "This is a GitLens Telegram test notification.", 1)
	})
	reloadChannels()

	r := gin.New()
	r.Use(gin.Recovery())
	// otelgin middleware automatically creates spans for every HTTP request
	// with method, path, and status code attributes. Placed after Recovery
	// but before Logger so trace context is available in log records.
	r.Use(otelgin.Middleware("gitlens"))
	r.Use(otel.MetricsMiddleware())
	r.Use(gin.Logger())

	r.SetHTMLTemplate(tmpl)
	r.Static("/static", "./static")

	r.GET("/auth/github", authHandler.Login)
	r.GET("/auth/github/callback", authHandler.Callback)
	r.GET("/auth/forgejo", authHandler.LoginForgejo)
	r.GET("/auth/forgejo/callback", authHandler.CallbackForgejo)
	r.POST("/auth/logout", authHandler.Logout)

	r.GET("/", dashHandler.Index)

	r.GET("/badge/:owner/:repo", badgeHandler.Badge)
	r.GET("/api/trmnl/summary", trmnlHandler.Summary)
	r.POST("/webhook/github", webhookHandler.HandlePush)
	r.POST("/webhook/github-app", gitHubAppHandler.HandleInstallation)

	authed := r.Group("/")
	authed.Use(middleware.AuthRequired(sessionStore))
	{
		authed.GET("/dashboard", middleware.HTMXOnly(), dashHandler.ReposTab)
		authed.GET("/dashboard/wall", middleware.HTMXOnly(), dashHandler.WallTab)
		authed.GET("/prs", middleware.HTMXOnly(), dashHandler.PRsTab)
		authed.POST("/prs/merge", dashHandler.MergeSinglePR)
		authed.POST("/prs/batch-merge", dashHandler.BatchMergePRs)
		authed.POST("/prs/batch-merge-passing", dashHandler.BatchMergePassingPRs)
		authed.POST("/prs/close", dashHandler.CloseSinglePR)
		authed.POST("/prs/batch-close", dashHandler.BatchClosePRs)
		authed.POST("/prs/rerun", dashHandler.RerunPRBuilds)
		authed.GET("/metrics", middleware.HTMXOnly(), dashHandler.MetricsTab)
		authed.GET("/repos", middleware.HTMXOnly(), dashHandler.ListRepos)
		authed.POST("/repos/:id/sync", dashHandler.SyncRepo)
		authed.POST("/repos/import-all", dashHandler.ImportAllRepos)
		authed.GET("/repos/import-progress", dashHandler.ImportProgressHandler)
		authed.GET("/repos/:id/prs", dashHandler.ListPullRequests)
		authed.POST("/repos/:id/prs/:number/merge", dashHandler.MergePR)
		authed.POST("/repos/:id/prs/:number/close", dashHandler.ClosePR)
		authed.POST("/repos/:id/prs/merge-all", dashHandler.MergeAllPRs)
		authed.POST("/repos/:id/prs/merge-passing", dashHandler.MergePassingPRs)
		authed.POST("/repos/:id/prs/close-all", dashHandler.CloseAllPRs)
		authed.POST("/repos/:id/renovate/rebase-all", dashHandler.RenovateRebaseAll)

		authed.GET("/settings", middleware.HTMXOnly(), settingsHandler.Index)
		authed.POST("/settings/interval", settingsHandler.UpdateInterval)
		authed.POST("/settings/umami", settingsHandler.UpdateUmami)
		authed.GET("/settings/repos/available", settingsHandler.AvailableRepos)
		authed.POST("/settings/repos/select", settingsHandler.SelectRepos)
		authed.POST("/settings/forgejo/disconnect", settingsHandler.DisconnectForgejo)
		authed.GET("/settings/forgejo/available", settingsHandler.AvailableForgejoRepos)
		authed.POST("/settings/forgejo/select", settingsHandler.SelectForgejoRepos)
		authed.POST("/settings/eink", settingsHandler.UpdateEinkMode)
		authed.POST("/settings/forgejo/warning/dismiss", settingsHandler.DismissForgejoWarning)
		authed.DELETE("/repos/:id", settingsHandler.RemoveRepo)

		authed.GET("/metrics/history", trendsHandler.MetricsHistory)
		authed.GET("/trends", middleware.HTMXOnly(), trendsHandler.TrendsPage)
		authed.GET("/deploy", middleware.HTMXOnly(), deployHandler.Dashboard)
		authed.GET("/year-overview", middleware.HTMXOnly(), yearOverviewHandler.YearOverview)
		authed.GET("/year-overview/stats", yearOverviewHandler.Stats)
		authed.GET("/year-overview/backfill-status", yearOverviewHandler.BackfillStatus)
		authed.POST("/year-overview/refresh", yearOverviewHandler.Refresh)
		authed.GET("/charts/data", chartHandler.Data)
		authed.GET("/feed", feedHandler.Feed)
		authed.POST("/feed/filter", feedHandler.FeedFilter)
		authed.POST("/repos/setup-webhooks", gitHubAppHandler.SetupAutoWebhooks)

		admin := authed.Group("/admin")
		admin.Use(middleware.AdminRequired(client))
		{
			// Settings and admin panels were merged into /settings; keep
			// /admin as a redirect so old links/bookmarks still work.
			admin.GET("", func(c *gin.Context) {
				c.Redirect(http.StatusFound, "/settings")
			})
			admin.POST("/otel", adminHandler.UpdateOTEL)
			admin.POST("/gotify", adminHandler.UpdateGotify)
			admin.POST("/gotify/test", adminHandler.TestGotify)
			admin.POST("/telegram", adminHandler.UpdateTelegram)
			admin.POST("/telegram/test", adminHandler.TestTelegram)
			admin.GET("/users", adminHandler.ListUsers)
			admin.POST("/users/:id/toggle-admin", adminHandler.ToggleAdmin)
		}

		// Admin-only JSON API.
		api := authed.Group("/api")
		api.Use(middleware.AdminRequired(client))
		{
			api.GET("/settings", adminHandler.ListSettings)
		}

		// Personal API token management — accepts session cookie OR bearer
		// token (so existing tokens can bootstrap new ones).
		apiTokens := r.Group("/api/tokens")
		apiTokens.Use(middleware.SessionOrToken(sessionStore, client))
		{
			apiTokens.POST("", apiTokenHandler.Create)
			apiTokens.GET("", apiTokenHandler.List)
			apiTokens.DELETE("/:id", apiTokenHandler.Revoke)
			apiTokens.POST("/:id/rotate", apiTokenHandler.Rotate)
		}
	}

	r.GET("/ws", func(c *gin.Context) {
		var userID int64
		cookie, err := c.Cookie("gitlens_session")
		if err == nil {
			userID, _ = sessionStore.Get(cookie)
		}
		hub.HandleWebSocket(c.Writer, c.Request, userID)
	})

	_ = repository.FieldName
	_ = user.FieldLogin

	port := os.Getenv("PORT")
	if port == "" {
		port = "6270"
	}

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go syncer.StartPeriodicSync(ctx)

	go func() {
		log.Printf("Server starting on :%s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("Server shutdown error: %v", err)
	}

	if err := otelManager.Shutdown(shutdownCtx); err != nil {
		log.Printf("OTEL shutdown error: %v", err)
	}
}
