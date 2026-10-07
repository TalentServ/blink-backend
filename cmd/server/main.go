package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nisha-ts-40599/blink-backend/internal/agent"
	"github.com/nisha-ts-40599/blink-backend/internal/auth"
	"github.com/nisha-ts-40599/blink-backend/internal/chat"
	"github.com/nisha-ts-40599/blink-backend/internal/canonical"
	"github.com/nisha-ts-40599/blink-backend/internal/config"
	"github.com/nisha-ts-40599/blink-backend/internal/crypto"
	"github.com/nisha-ts-40599/blink-backend/internal/db"
	"github.com/nisha-ts-40599/blink-backend/internal/httpapi"
	"github.com/nisha-ts-40599/blink-backend/internal/integrations"
	"github.com/nisha-ts-40599/blink-backend/internal/mailer"
	"github.com/nisha-ts-40599/blink-backend/internal/project"
	"github.com/nisha-ts-40599/blink-backend/internal/s3ws"
	"github.com/nisha-ts-40599/blink-backend/internal/zipkit"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx := context.Background()
	dbCtx, dbCancel := context.WithTimeout(ctx, 90*time.Second)
	pool, err := db.Connect(dbCtx, cfg.DatabaseURL)
	dbCancel()
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer pool.Close()

	if strings.EqualFold(strings.TrimSpace(os.Getenv("BLINK_SKIP_MIGRATE")), "1") {
		log.Printf("warning: BLINK_SKIP_MIGRATE=1; skipping schema migrate")
	} else {
		migrateCtx, migrateCancel := context.WithTimeout(ctx, 90*time.Second)
		if err := db.Migrate(migrateCtx, pool); err != nil {
			migrateCancel()
			log.Fatalf("migrate: %v", err)
		}
		migrateCancel()
	}

	secret := strings.TrimSpace(cfg.IntegrationSecretKey)
	if secret == "" {
		secret = "blink-local-dev-only-change-me"
		log.Printf("warning: BLINK_INTEGRATION_SECRET_KEY unset; using local default")
	}
	box, err := crypto.New(secret)
	if err != nil {
		log.Fatalf("crypto: %v", err)
	}

	mail := mailer.New(cfg)
	authSvc := auth.New(pool, cfg, mail)
	proj := project.New(pool)
	canonicalSvc := canonical.New(pool)
	agentClient := agent.New(cfg)
	integ := integrations.New(pool, cfg, box)
	s3svc := s3ws.New(cfg)
	if kit, err := zipkit.Ensure(cfg.AutomationSDLCPath, cfg.AutomationSDLCGit); err != nil {
		log.Printf("warning: automation_sdlc kit not ready yet: %v", err)
	} else {
		cfg.AutomationSDLCPath = kit
		s3svc = s3ws.New(cfg)
	}
	chatStore := chat.New(pool)
	handler := httpapi.New(cfg, authSvc, proj, canonicalSvc, agentClient, mail, integ, s3svc, chatStore)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// Chat SSE can last several minutes; do not bound the whole response write window.
		ReadTimeout:  0,
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Printf("blink-backend (go) listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if err := canonicalSvc.ProcessQueuedProjections(context.Background(), 20); err != nil {
					log.Printf("projection worker: %v", err)
				}
			}
		}
	}()

	<-stop

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
