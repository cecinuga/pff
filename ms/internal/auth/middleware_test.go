package auth

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type fakeVerifier map[string]Identity

func (f fakeVerifier) Verify(_ context.Context, tok string) (Identity, error) {
	id, ok := f[tok]
	if !ok {
		return Identity{}, ErrInvalidToken
	}
	return id, nil
}

type fakeStore struct {
	whitelist   map[string]bool
	emailMFA    map[string]bool
	mfaSessions map[string]bool
}

func (s fakeStore) Authorize(_ context.Context, id Identity) (User, bool, bool, error) {
	if !s.whitelist[id.Email] {
		return User{}, false, false, nil
	}
	u := User{ID: "u-" + id.Email, Email: id.Email, EmailMFAEnabled: s.emailMFA[id.Email]}
	return u, true, s.mfaSessions[u.ID], nil
}

func newRouter(requireMFA bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	google := func(email string) Identity {
		return Identity{UID: email, Email: email, EmailVerified: true, SignInProvider: "google.com", AuthTime: time.Unix(1, 0)}
	}
	totp := google("totp@example.com")
	totp.SecondFactor = "totp"
	unverified := google("unverified@example.com")
	unverified.EmailVerified = false
	password := google("ok@example.com")
	password.SignInProvider = "password"

	a := &Authenticator{
		Verifier: fakeVerifier{
			"ok":         google("ok@example.com"),
			"upper":      google("OK@Example.com"),
			"intruder":   google("intruder@example.com"),
			"unverified": unverified,
			"password":   password,
			"totp":       totp,
			"email-mfa":  google("mail@example.com"),
			"email-done": google("done@example.com"),
		},
		Store: fakeStore{
			whitelist: map[string]bool{
				"ok@example.com": true, "totp@example.com": true, "unverified@example.com": true,
				"mail@example.com": true, "done@example.com": true,
			},
			emailMFA:    map[string]bool{"mail@example.com": true, "done@example.com": true},
			mfaSessions: map[string]bool{"u-done@example.com": true},
		},
		RequireMFA: requireMFA,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	r := gin.New()
	r.GET("/me", a.Middleware(), func(c *gin.Context) { c.String(200, PrincipalFrom(c).MFAState) })
	r.GET("/data", a.Middleware(), RequireMFA(), func(c *gin.Context) { c.String(200, "data") })
	return r
}

func call(r *gin.Engine, path, token string) (int, string) {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

func TestMiddleware(t *testing.T) {
	r := newRouter(false)
	cases := []struct {
		path, token string
		status      int
		contains    string
	}{
		{"/data", "", 401, "unauthenticated"},
		{"/data", "garbage", 401, "unauthenticated"},
		{"/data", "intruder", 403, "not_whitelisted"},
		{"/data", "unverified", 403, "email_not_verified"},
		{"/data", "password", 403, "provider_not_allowed"},
		{"/data", "ok", 200, "data"},
		{"/data", "upper", 200, "data"}, // e-mail comparison is case-insensitive
		{"/data", "totp", 200, "data"},
		{"/me", "email-mfa", 200, MFAStateRequired},
		{"/data", "email-mfa", 403, MFAStateRequired},
		{"/data", "email-done", 200, "data"},
	}
	for _, c := range cases {
		status, body := call(r, c.path, c.token)
		if status != c.status || !strings.Contains(body, c.contains) {
			t.Errorf("%s with %q: got %d %s, want %d containing %q", c.path, c.token, status, body, c.status, c.contains)
		}
	}
}

func TestRequireMFAPolicy(t *testing.T) {
	r := newRouter(true)
	if status, body := call(r, "/data", "ok"); status != 403 || !strings.Contains(body, MFAStateEnrollmentRequired) {
		t.Errorf("no factor with policy on: got %d %s", status, body)
	}
	if status, _ := call(r, "/me", "ok"); status != 200 {
		t.Errorf("/me must stay reachable to enrol a factor, got %d", status)
	}
	if status, _ := call(r, "/data", "totp"); status != 200 {
		t.Errorf("totp session rejected: %d", status)
	}
}

func TestParseAllowedEmails(t *testing.T) {
	got, err := ParseAllowedEmails(strings.NewReader("# team\nA@Example.com\n\n  b@example.com \na@example.com\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "a@example.com" || got[1] != "b@example.com" {
		t.Fatalf("got %v", got)
	}
	for _, bad := range []string{"no-at-sign\n", "@example.com\n", "a@b.com, c@d.com\n", "trailing@\n"} {
		if _, err := ParseAllowedEmails(strings.NewReader(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestDevVerifier(t *testing.T) {
	v := DevVerifier{Started: time.Now()}
	id, err := v.Verify(context.Background(), "dev:Alice@Example.com")
	if err != nil || id.Email != "alice@example.com" || !id.EmailVerified {
		t.Fatalf("id = %+v, %v", id, err)
	}
	for _, bad := range []string{"alice@example.com", "dev:", "dev:nobody"} {
		if _, err := v.Verify(context.Background(), bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestRandomDigits(t *testing.T) {
	for i := 0; i < 100; i++ {
		c, err := randomDigits(6)
		if err != nil || len(c) != 6 || strings.Trim(c, "0123456789") != "" {
			t.Fatalf("code %q, %v", c, err)
		}
	}
}
