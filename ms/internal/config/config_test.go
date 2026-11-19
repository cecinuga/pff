package config

import (
	"strings"
	"testing"
)

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func devEnv() map[string]string {
	return map[string]string{
		"PFF_ENV":                 "development",
		"PFF_DATABASE_URL":        "postgres://x",
		"PFF_SECRET_KEY":          "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		"PFF_ALLOWED_EMAILS_FILE": "/tmp/allowed",
		"PFF_AUTH_MODE":           "dev",
		"PFF_BANK_PROVIDER":       "mock",
		"PFF_PUSH_PROVIDER":       "log",
		"PFF_MAIL_PROVIDER":       "log",
	}
}

func TestLoadDevelopment(t *testing.T) {
	setEnv(t, devEnv())
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Location.String() != "Europe/Rome" || c.ReportingCurrency != "EUR" {
		t.Errorf("defaults: %s %s", c.Location, c.ReportingCurrency)
	}
	if c.Bank.RedirectURL != "http://localhost:5173/connections/callback" {
		t.Errorf("redirect url = %s", c.Bank.RedirectURL)
	}
}

func TestProductionRejectsDevShortcuts(t *testing.T) {
	env := devEnv()
	env["PFF_ENV"] = "production"
	setEnv(t, env)
	_, err := Load()
	if err == nil {
		t.Fatal("dev auth, mock bank and log mail accepted in production")
	}
	for _, want := range []string{"PFF_AUTH_MODE=dev", "PFF_MAIL_PROVIDER=log", "PFF_BANK_PROVIDER=mock"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}

func TestParseBanks(t *testing.T) {
	got, err := ParseBanks(" it:Intesa Sanpaolo , IT:Banca Fineco,IT:Intesa Sanpaolo,")
	if err != nil {
		t.Fatal(err)
	}
	want := []BankRef{{"IT", "Intesa Sanpaolo"}, {"IT", "Banca Fineco"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, bad := range []string{"Intesa Sanpaolo", "ITA:Intesa", "IT:", ":Intesa"} {
		if _, err := ParseBanks(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestMockDefaultsToDemoBank(t *testing.T) {
	setEnv(t, devEnv())
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Bank.Banks) != 1 || c.Bank.Banks[0].Name != "PFF Demo Bank" {
		t.Fatalf("banks = %v", c.Bank.Banks)
	}
}

func TestEnableBankingNeedsCredentials(t *testing.T) {
	env := devEnv()
	env["PFF_BANK_PROVIDER"] = "enablebanking"
	setEnv(t, env)
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "PFF_EB_APP_ID") || !strings.Contains(err.Error(), "PFF_BANKS") {
		t.Fatalf("missing Enable Banking settings not reported: %v", err)
	}
}
