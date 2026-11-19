// Package enablebanking implements bank.Provider on top of the Enable Banking
// aggregator REST API (https://enablebanking.com/docs/api/reference/).
//
// Authentication: every request carries a short-lived RS256 JWT signed with the
// application's private key (kid = application id). The JWT identifies the
// application only; access to a user's data is bound to the session created
// after the user completes SCA at their bank.
package enablebanking

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cecinuga/pff/ms/internal/bank"
)

// DefaultBaseURL is the production and sandbox API host (the application's
// environment is fixed at registration, not by URL).
const DefaultBaseURL = "https://api.enablebanking.com"

const (
	jwtTTL          = time.Hour
	jwtRenewBefore  = 5 * time.Minute
	maxResponseSize = 16 << 20
	maxPages        = 1000
)

// Config configures the client.
type Config struct {
	BaseURL     string
	AppID       string
	PrivateKey  *rsa.PrivateKey
	HTTPClient  *http.Client
	MaxAttempts int // total attempts for transient failures (default 3)
	BaseBackoff time.Duration

	// Test hooks.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
}

// Client is a bank.Provider backed by Enable Banking. It is safe for
// concurrent use.
type Client struct {
	cfg Config

	mu        sync.Mutex
	jwt       string
	jwtExpiry time.Time
}

var _ bank.Provider = (*Client)(nil)

// New validates cfg and returns a client.
func New(cfg Config) (*Client, error) {
	if cfg.AppID == "" {
		return nil, errors.New("enablebanking: application id is required")
	}
	if cfg.PrivateKey == nil {
		return nil, errors.New("enablebanking: private key is required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	if cfg.BaseBackoff <= 0 {
		cfg.BaseBackoff = 500 * time.Millisecond
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepCtx
	}
	return &Client{cfg: cfg}, nil
}

// LoadPrivateKey reads an RSA private key in PKCS#1 or PKCS#8 PEM form.
func LoadPrivateKey(path string) (*rsa.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	return ParsePrivateKey(raw)
}

// ParsePrivateKey parses an RSA private key in PKCS#1 or PKCS#8 PEM form.
func ParsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("private key: no PEM block found")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("private key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key: not an RSA key")
	}
	return rk, nil
}

// Name implements bank.Provider.
func (c *Client) Name() string { return "enablebanking" }

// ---- JWT ------------------------------------------------------------------

func (c *Client) token() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.cfg.Now()
	if c.jwt != "" && now.Before(c.jwtExpiry.Add(-jwtRenewBefore)) {
		return c.jwt, nil
	}
	exp := now.Add(jwtTTL)
	tok, err := signJWT(c.cfg.PrivateKey, c.cfg.AppID, now, exp)
	if err != nil {
		return "", err
	}
	c.jwt, c.jwtExpiry = tok, exp
	return tok, nil
}

func signJWT(key *rsa.PrivateKey, appID string, iat, exp time.Time) (string, error) {
	header, _ := json.Marshal(map[string]string{"typ": "JWT", "alg": "RS256", "kid": appID})
	claims, _ := json.Marshal(map[string]any{
		"iss": "enablebanking.com",
		"aud": "api.enablebanking.com",
		"iat": iat.Unix(),
		"exp": exp.Unix(),
	})
	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}
	return signingInput + "." + enc.EncodeToString(sig), nil
}

// ---- HTTP -----------------------------------------------------------------

type apiError struct {
	Message string          `json:"message"`
	Code    int             `json:"code"`
	Error   string          `json:"error"`
	Detail  json.RawMessage `json:"detail"`
}

// do sends one API call with retries for transient failures. Rate-limit
// responses are never retried here: the caller must back off for much longer
// than a request lifetime (the bank's limits are per day).
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, psu *bank.PSU, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
	}
	u := c.cfg.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var lastErr error
	for attempt := 0; attempt < c.cfg.MaxAttempts; attempt++ {
		if attempt > 0 {
			if err := c.cfg.Sleep(ctx, backoff(c.cfg.BaseBackoff, attempt)); err != nil {
				return err
			}
		}
		retry, err := c.once(ctx, method, u, payload, psu, out)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retry {
			return err
		}
	}
	return lastErr
}

func (c *Client) once(ctx context.Context, method, u string, payload []byte, psu *bank.PSU, out any) (retry bool, err error) {
	tok, err := c.token()
	if err != nil {
		return false, err
	}
	var rdr io.Reader
	if payload != nil {
		rdr = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	setPSUHeaders(req.Header, psu)

	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return true, &bank.Error{Status: 0, Code: "NETWORK", Message: err.Error(), Sentinel: bank.ErrUnavailable}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return true, &bank.Error{Status: resp.StatusCode, Code: "NETWORK", Message: err.Error(), Sentinel: bank.ErrUnavailable}
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out == nil || len(data) == 0 {
			return false, nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return false, fmt.Errorf("decode %s response: %w", req.URL.Path, err)
		}
		return false, nil
	}

	var ae apiError
	_ = json.Unmarshal(data, &ae)
	if ae.Message == "" {
		ae.Message = http.StatusText(resp.StatusCode)
	}
	be := &bank.Error{Status: resp.StatusCode, Code: ae.Error, Message: ae.Message, Sentinel: classify(resp.StatusCode, ae.Error)}
	return be.Sentinel == bank.ErrUnavailable, be
}

// classify maps an Enable Banking error to a bank sentinel error.
func classify(status int, code string) error {
	switch code {
	case "EXPIRED_SESSION", "REVOKED_SESSION", "CLOSED_SESSION", "SESSION_DOES_NOT_EXIST", "WRONG_SESSION_STATUS":
		return bank.ErrSessionExpired
	case "ASPSP_RATE_LIMIT_EXCEEDED":
		return bank.ErrRateLimited
	case "WRONG_TRANSACTIONS_PERIOD", "DATE_FROM_IN_FUTURE", "WRONG_DATE_INTERVAL":
		return bank.ErrPeriodUnavailable
	case "EXPIRED_AUTHORIZATION_CODE", "WRONG_AUTHORIZATION_CODE", "ALREADY_AUTHORIZED":
		return bank.ErrInvalidCode
	case "ASPSP_ERROR", "ASPSP_TIMEOUT":
		return bank.ErrUnavailable
	}
	switch {
	case status == http.StatusTooManyRequests:
		return bank.ErrRateLimited
	case status == http.StatusRequestTimeout || status >= 500:
		return bank.ErrUnavailable
	}
	return nil
}

func setPSUHeaders(h http.Header, psu *bank.PSU) {
	if psu == nil {
		return
	}
	set := func(k, v string) {
		if v != "" {
			h.Set(k, v)
		}
	}
	set("Psu-Ip-Address", psu.IPAddress)
	set("Psu-User-Agent", psu.UserAgent)
	set("Psu-Referer", psu.Referer)
	set("Psu-Accept", psu.Accept)
	set("Psu-Accept-Charset", psu.AcceptCharset)
	set("Psu-Accept-Encoding", psu.AcceptEncoding)
	set("Psu-Accept-Language", psu.AcceptLanguage)
}

// backoff returns an exponential delay with full jitter, capped at 30s.
func backoff(base time.Duration, attempt int) time.Duration {
	ceiling := base << uint(attempt)
	if ceiling > 30*time.Second || ceiling <= 0 {
		ceiling = 30 * time.Second
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(ceiling)))
	if err != nil {
		return ceiling
	}
	// Never retry sooner than the base delay.
	return base + time.Duration(n.Int64())
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
