// Command pff runs the PFF backend: REST API, background scheduler and
// database migrations.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // the container image may lack zoneinfo; PFF_TIMEZONE must always resolve

	firebase "firebase.google.com/go/v4"
	"google.golang.org/api/option"

	"github.com/cecinuga/pff/ms/internal/analytics"
	"github.com/cecinuga/pff/ms/internal/auth"
	"github.com/cecinuga/pff/ms/internal/bank"
	"github.com/cecinuga/pff/ms/internal/bank/enablebanking"
	"github.com/cecinuga/pff/ms/internal/bank/mock"
	"github.com/cecinuga/pff/ms/internal/budget"
	"github.com/cecinuga/pff/ms/internal/config"
	"github.com/cecinuga/pff/ms/internal/connect"
	"github.com/cecinuga/pff/ms/internal/httpapi"
	"github.com/cecinuga/pff/ms/internal/ingest"
	"github.com/cecinuga/pff/ms/internal/jobs"
	"github.com/cecinuga/pff/ms/internal/notify"
	"github.com/cecinuga/pff/ms/internal/platform/db"
	"github.com/cecinuga/pff/ms/internal/platform/mail"
	"github.com/cecinuga/pff/ms/internal/platform/secretbox"
	"github.com/cecinuga/pff/ms/internal/warehouse"
)

func main() {
	// `pff healthcheck` lets the distroless container probe itself (no curl).
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func healthcheck() int {
	addr := os.Getenv("PFF_HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "unhealthy:", resp.Status)
		return 1
	}
	return 0
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	if cfg.IsDev() {
		log.Warn("running in DEVELOPMENT mode", "auth", cfg.Auth.Mode, "bank", cfg.Bank.Provider, "mail", cfg.Mail.Provider, "push", cfg.Push.Provider)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool, log); err != nil {
		return err
	}

	store := &warehouse.Store{Pool: pool}
	if err := store.SyncCatalog(ctx); err != nil {
		return fmt.Errorf("sync metric catalog: %w", err)
	}
	n, err := auth.SyncWhitelistFile(ctx, pool, cfg.AllowedEmailsFile)
	if err != nil {
		return fmt.Errorf("sync whitelist: %w", err)
	}
	log.Info("whitelist synchronised", "emails", n)

	box, err := secretbox.New(cfg.SecretKey)
	if err != nil {
		return fmt.Errorf("PFF_SECRET_KEY: %w", err)
	}

	// Firebase is needed for token verification and for push.
	var fbApp *firebase.App
	if cfg.Auth.Mode == "firebase" || cfg.Push.Provider == "fcm" {
		var opts []option.ClientOption
		if cfg.Firebase.CredentialsFile != "" {
			opts = append(opts, option.WithCredentialsFile(cfg.Firebase.CredentialsFile))
		}
		fbApp, err = firebase.NewApp(ctx, &firebase.Config{ProjectID: cfg.Firebase.ProjectID}, opts...)
		if err != nil {
			return fmt.Errorf("firebase: %w", err)
		}
	}

	var verifier auth.Verifier
	switch cfg.Auth.Mode {
	case "firebase":
		client, err := fbApp.Auth(ctx)
		if err != nil {
			return fmt.Errorf("firebase auth: %w", err)
		}
		verifier = auth.FirebaseVerifier{Client: client}
	case "dev":
		verifier = auth.DevVerifier{Started: time.Now()}
	}

	var mailer mail.Sender
	switch cfg.Mail.Provider {
	case "smtp":
		mailer = mail.SMTPSender{Host: cfg.Mail.SMTPHost, Port: cfg.Mail.SMTPPort, User: cfg.Mail.SMTPUser, Password: cfg.Mail.SMTPPass, From: cfg.Mail.From}
	case "log":
		mailer = mail.LogSender{Log: log}
	}

	var pusher notify.Pusher
	switch cfg.Push.Provider {
	case "fcm":
		client, err := fbApp.Messaging(ctx)
		if err != nil {
			return fmt.Errorf("firebase messaging: %w", err)
		}
		pusher = notify.FCMPusher{Client: client, AppURL: cfg.PublicAppURL}
	case "log":
		pusher = notify.LogPusher{Log: log}
	}

	var provider bank.Provider
	switch cfg.Bank.Provider {
	case "enablebanking":
		key, err := enablebanking.LoadPrivateKey(cfg.Bank.EBKeyFile)
		if err != nil {
			return err
		}
		provider, err = enablebanking.New(enablebanking.Config{BaseURL: cfg.Bank.EBBaseURL, AppID: cfg.Bank.EBAppID, PrivateKey: key})
		if err != nil {
			return err
		}
	case "mock":
		provider = mock.New(cfg.Location)
	}

	notifier := &notify.Service{Pool: pool, Pusher: pusher, Log: log}
	budgets := &budget.Service{Pool: pool, Notifier: notifier, Currency: cfg.ReportingCurrency, Location: cfg.Location, Log: log, Now: time.Now}
	ing := &ingest.Service{
		Store:    store,
		Provider: provider,
		Policy: ingest.GapPolicy{
			FreshTTL:       cfg.Sync.FreshTTL,
			SettlementDays: cfg.Sync.SettlementDays,
			MaxBridgeDays:  7,
			Location:       cfg.Location,
		},
		BackgroundDailyMax: cfg.Sync.BackgroundDailyMax,
		BackfillDays:       cfg.Sync.BackfillDays,
		MaxHistoryDays:     cfg.Sync.MaxHistoryDays,
		LockWait:           20 * time.Second,
		Log:                log,
		Now:                time.Now,
		OnChange: func(userID string) {
			bctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			budgets.EvaluateUser(bctx, userID)
		},
	}

	router, err := httpapi.NewRouter(httpapi.Deps{
		Cfg:    cfg,
		Log:    log,
		Pool:   pool,
		Auth:   &auth.Authenticator{Verifier: verifier, Store: auth.PGStore{Pool: pool}, RequireMFA: cfg.Auth.RequireMFA, Log: log},
		OTP:    &auth.EmailOTP{Pool: pool, Box: box, Mailer: mailer, Now: time.Now},
		Ingest: ing,
		Connect: &connect.Service{
			Store: store, Ingest: ing, Provider: provider, Box: box,
			RedirectURL: cfg.Bank.RedirectURL, Banks: cfg.Bank.Banks, PSUType: cfg.Bank.PSUType, ConsentDays: cfg.Bank.ConsentDays,
			Log: log, Now: time.Now,
		},
		Analytics: &analytics.Service{Pool: pool, Currency: cfg.ReportingCurrency},
		Warehouse: store,
		Budgets:   budgets,
		Notify:    notifier,
	})
	if err != nil {
		return err
	}

	sched := &jobs.Scheduler{Pool: pool, Ingest: ing, Budgets: budgets, Notifier: notifier, Interval: cfg.Sync.SchedulerInterval, Log: log}
	go sched.Run(ctx)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second, // bank fetches can be slow
		IdleTimeout:       120 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.HTTPAddr, "bank", provider.Name())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
