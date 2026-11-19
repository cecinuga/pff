// Package config loads PFF backend settings from environment variables.
// Every variable is documented in cluster/.env.example.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the complete backend configuration.
type Config struct {
	Env               string // "development" or "production"
	HTTPAddr          string
	DatabaseURL       string
	SecretKey         string // base64, 32 bytes
	Location          *time.Location
	ReportingCurrency string
	PublicAppURL      string // browser-facing origin of the web app
	TrustedProxies    []string

	AllowedEmailsFile string

	Auth     AuthConfig
	Firebase FirebaseConfig
	Bank     BankConfig
	Sync     SyncConfig
	Push     PushConfig
	Mail     MailConfig
}

// AuthConfig selects how API callers are authenticated.
type AuthConfig struct {
	Mode       string // "firebase" or "dev"
	RequireMFA bool
}

// FirebaseConfig holds server credentials and the public web-app config that
// the backend hands to the client at runtime.
type FirebaseConfig struct {
	ProjectID         string
	CredentialsFile   string // service-account JSON; empty = application default credentials
	WebAPIKey         string
	AuthDomain        string
	AppID             string
	MessagingSenderID string
	VAPIDKey          string
}

// BankConfig selects the banking provider and the user's bank(s). Which banks
// PFF connects to is personal configuration, so it comes from PFF_BANKS and is
// never chosen freely in the UI.
type BankConfig struct {
	Provider    string    // "enablebanking" or "mock"
	Banks       []BankRef // PFF_BANKS: the only banks users can connect
	PSUType     string    // "personal" or "business"
	ConsentDays int
	RedirectURL string
	EBAppID     string
	EBKeyFile   string
	EBBaseURL   string // banking API address
}

// BankRef identifies a bank (ASPSP) at the provider.
type BankRef struct {
	Country string // ISO 3166 alpha-2, upper case
	Name    string // exact ASPSP name as listed by the provider
}

// ParseBanks parses PFF_BANKS: comma-separated "CC:Bank name" entries, e.g.
// "IT:Intesa Sanpaolo,IT:Banca Fineco".
func ParseBanks(s string) ([]BankRef, error) {
	var out []BankRef
	seen := map[BankRef]bool{}
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		country, name, ok := strings.Cut(item, ":")
		country, name = strings.ToUpper(strings.TrimSpace(country)), strings.TrimSpace(name)
		if !ok || len(country) != 2 || name == "" {
			return nil, fmt.Errorf("PFF_BANKS entry %q must look like \"IT:Bank name\"", item)
		}
		ref := BankRef{Country: country, Name: name}
		if !seen[ref] {
			seen[ref] = true
			out = append(out, ref)
		}
	}
	return out, nil
}

// SyncConfig tunes the GetOrSet cache and background jobs.
type SyncConfig struct {
	FreshTTL           time.Duration // how long data for not-yet-final days is served from cache
	SettlementDays     int           // days after which a fetched day is final
	BackgroundDailyMax int           // bank fetches/day/account without the user present
	BackfillDays       int           // history fetched right after a new consent
	SchedulerInterval  time.Duration
	MaxHistoryDays     int
}

// PushConfig selects the push sender.
type PushConfig struct {
	Provider string // "fcm" or "log"
}

// MailConfig configures e-mail delivery of one-time codes.
type MailConfig struct {
	Provider string // "smtp" or "log"
	SMTPHost string
	SMTPPort int
	SMTPUser string
	SMTPPass string
	From     string
}

// IsDev reports whether PFF runs in development mode.
func (c Config) IsDev() bool { return c.Env == "development" }

// Load reads and validates the configuration.
func Load() (Config, error) {
	var errs []error
	get := func(key, def string) string {
		if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return def
	}
	getInt := func(key string, def int) int {
		v := get(key, "")
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", key, err))
		}
		return n
	}
	getBool := func(key string, def bool) bool {
		v := get(key, "")
		if v == "" {
			return def
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", key, err))
		}
		return b
	}
	getDur := func(key string, def time.Duration) time.Duration {
		v := get(key, "")
		if v == "" {
			return def
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", key, err))
		}
		return d
	}

	c := Config{
		Env:               get("PFF_ENV", "production"),
		HTTPAddr:          get("PFF_HTTP_ADDR", ":8080"),
		DatabaseURL:       get("PFF_DATABASE_URL", ""),
		SecretKey:         get("PFF_SECRET_KEY", ""),
		ReportingCurrency: strings.ToUpper(get("PFF_REPORTING_CURRENCY", "EUR")),
		PublicAppURL:      strings.TrimRight(get("PFF_PUBLIC_APP_URL", "http://localhost:5173"), "/"),
		AllowedEmailsFile: get("PFF_ALLOWED_EMAILS_FILE", ""),
		Auth: AuthConfig{
			Mode:       get("PFF_AUTH_MODE", "firebase"),
			RequireMFA: getBool("PFF_AUTH_REQUIRE_MFA", false),
		},
		Firebase: FirebaseConfig{
			ProjectID:         get("PFF_FIREBASE_PROJECT_ID", ""),
			CredentialsFile:   get("GOOGLE_APPLICATION_CREDENTIALS", ""),
			WebAPIKey:         get("PFF_FIREBASE_WEB_API_KEY", ""),
			AuthDomain:        get("PFF_FIREBASE_AUTH_DOMAIN", ""),
			AppID:             get("PFF_FIREBASE_APP_ID", ""),
			MessagingSenderID: get("PFF_FIREBASE_MESSAGING_SENDER_ID", ""),
			VAPIDKey:          get("PFF_FIREBASE_VAPID_KEY", ""),
		},
		Bank: BankConfig{
			Provider:    get("PFF_BANK_PROVIDER", "enablebanking"),
			PSUType:     get("PFF_BANK_PSU_TYPE", "personal"),
			ConsentDays: getInt("PFF_BANK_CONSENT_DAYS", 90),
			RedirectURL: get("PFF_BANK_REDIRECT_URL", ""),
			EBAppID:     get("PFF_EB_APP_ID", ""),
			EBKeyFile:   get("PFF_EB_PRIVATE_KEY_FILE", ""),
			EBBaseURL:   get("PFF_EB_BASE_URL", "https://api.enablebanking.com"),
		},
		Sync: SyncConfig{
			FreshTTL:           getDur("PFF_SYNC_FRESH_TTL", time.Hour),
			SettlementDays:     getInt("PFF_SYNC_SETTLEMENT_DAYS", 3),
			BackgroundDailyMax: getInt("PFF_SYNC_BACKGROUND_DAILY_MAX", 4),
			BackfillDays:       getInt("PFF_SYNC_BACKFILL_DAYS", 730),
			SchedulerInterval:  getDur("PFF_SCHEDULER_INTERVAL", 6*time.Hour),
			MaxHistoryDays:     getInt("PFF_SYNC_MAX_HISTORY_DAYS", 5*366),
		},
		Push: PushConfig{Provider: get("PFF_PUSH_PROVIDER", "fcm")},
		Mail: MailConfig{
			Provider: get("PFF_MAIL_PROVIDER", "smtp"),
			SMTPHost: get("PFF_SMTP_HOST", ""),
			SMTPPort: getInt("PFF_SMTP_PORT", 587),
			SMTPUser: get("PFF_SMTP_USER", ""),
			SMTPPass: get("PFF_SMTP_PASSWORD", ""),
			From:     get("PFF_MAIL_FROM", ""),
		},
	}
	if p := get("PFF_TRUSTED_PROXIES", ""); p != "" {
		for _, s := range strings.Split(p, ",") {
			if s = strings.TrimSpace(s); s != "" {
				c.TrustedProxies = append(c.TrustedProxies, s)
			}
		}
	}
	loc, err := time.LoadLocation(get("PFF_TIMEZONE", "Europe/Rome"))
	if err != nil {
		errs = append(errs, fmt.Errorf("PFF_TIMEZONE: %w", err))
		loc = time.UTC
	}
	c.Location = loc
	if c.Bank.RedirectURL == "" {
		c.Bank.RedirectURL = c.PublicAppURL + "/connections/callback"
	}
	banks, err := ParseBanks(get("PFF_BANKS", ""))
	if err != nil {
		errs = append(errs, err)
	}
	if len(banks) == 0 && c.Bank.Provider == "mock" {
		banks = []BankRef{{Country: "IT", Name: "PFF Demo Bank"}}
	}
	c.Bank.Banks = banks

	errs = append(errs, c.validate()...)
	return c, errors.Join(errs...)
}

func (c Config) validate() []error {
	var errs []error
	req := func(name, v string) {
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
	}
	oneOf := func(name, v string, allowed ...string) {
		for _, a := range allowed {
			if v == a {
				return
			}
		}
		errs = append(errs, fmt.Errorf("%s must be one of %v, got %q", name, allowed, v))
	}

	oneOf("PFF_ENV", c.Env, "development", "production")
	req("PFF_DATABASE_URL", c.DatabaseURL)
	req("PFF_SECRET_KEY", c.SecretKey)
	req("PFF_ALLOWED_EMAILS_FILE", c.AllowedEmailsFile)
	if u, err := url.Parse(c.PublicAppURL); err != nil || u.Scheme == "" || u.Host == "" {
		errs = append(errs, fmt.Errorf("PFF_PUBLIC_APP_URL must be an absolute URL"))
	}

	oneOf("PFF_AUTH_MODE", c.Auth.Mode, "firebase", "dev")
	oneOf("PFF_BANK_PROVIDER", c.Bank.Provider, "enablebanking", "mock")
	oneOf("PFF_PUSH_PROVIDER", c.Push.Provider, "fcm", "log")
	oneOf("PFF_MAIL_PROVIDER", c.Mail.Provider, "smtp", "log")

	// Development shortcuts must never reach production.
	if !c.IsDev() {
		if c.Auth.Mode == "dev" {
			errs = append(errs, errors.New("PFF_AUTH_MODE=dev is only allowed with PFF_ENV=development"))
		}
		if c.Mail.Provider == "log" {
			errs = append(errs, errors.New("PFF_MAIL_PROVIDER=log would print one-time codes; only allowed with PFF_ENV=development"))
		}
		if c.Bank.Provider == "mock" {
			errs = append(errs, errors.New("PFF_BANK_PROVIDER=mock is only allowed with PFF_ENV=development"))
		}
	}
	if c.Auth.Mode == "firebase" || c.Push.Provider == "fcm" {
		req("PFF_FIREBASE_PROJECT_ID", c.Firebase.ProjectID)
	}
	if c.Bank.Provider == "enablebanking" {
		req("PFF_EB_APP_ID", c.Bank.EBAppID)
		req("PFF_EB_PRIVATE_KEY_FILE", c.Bank.EBKeyFile)
		if len(c.Bank.Banks) == 0 {
			errs = append(errs, errors.New(`PFF_BANKS is required, e.g. PFF_BANKS="IT:Intesa Sanpaolo"`))
		}
	}
	oneOf("PFF_BANK_PSU_TYPE", c.Bank.PSUType, "personal", "business")
	if u, err := url.Parse(c.Bank.EBBaseURL); err != nil || u.Scheme != "https" || u.Host == "" {
		errs = append(errs, fmt.Errorf("PFF_EB_BASE_URL must be an https URL"))
	}
	if c.Mail.Provider == "smtp" {
		req("PFF_SMTP_HOST", c.Mail.SMTPHost)
		req("PFF_MAIL_FROM", c.Mail.From)
	}
	if c.Bank.ConsentDays < 1 || c.Bank.ConsentDays > 180 {
		errs = append(errs, fmt.Errorf("PFF_BANK_CONSENT_DAYS must be between 1 and 180"))
	}
	if c.Sync.BackgroundDailyMax < 0 || c.Sync.SettlementDays < 0 || c.Sync.BackfillDays < 1 || c.Sync.MaxHistoryDays < 1 {
		errs = append(errs, fmt.Errorf("PFF_SYNC_* values must be positive"))
	}
	if c.Sync.FreshTTL <= 0 || c.Sync.SchedulerInterval <= 0 {
		errs = append(errs, fmt.Errorf("PFF_SYNC_FRESH_TTL and PFF_SCHEDULER_INTERVAL must be positive"))
	}
	return errs
}
