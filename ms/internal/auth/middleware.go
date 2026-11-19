package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cecinuga/pff/ms/internal/platform/httperr"
)

// User is an authorised PFF user.
type User struct {
	ID              string `json:"id"`
	Email           string `json:"email"`
	Name            string `json:"name"`
	EmailMFAEnabled bool   `json:"email_mfa_enabled"`
}

// MFA states reported to the client.
const (
	MFAStateOK                 = "ok"
	MFAStateRequired           = "mfa_required"            // a factor is enrolled but this session has not passed it
	MFAStateEnrollmentRequired = "mfa_enrollment_required" // policy requires a factor and none is enrolled
)

// Principal is the authenticated caller attached to each request.
type Principal struct {
	User      User
	Identity  Identity
	MFAState  string
	MFAMethod string // "totp", "phone", "email" or "" when no factor was used
}

// UserStore is the persistence the authenticator needs.
type UserStore interface {
	// Authorize resolves a verified identity: whether its e-mail is
	// whitelisted, the PFF user (created on first sign-in), and whether this
	// sign-in session already passed the e-mail second factor.
	Authorize(ctx context.Context, id Identity) (u User, allowed, emailMFADone bool, err error)
}

// Authenticator builds the gin middleware.
type Authenticator struct {
	Verifier   Verifier
	Store      UserStore
	RequireMFA bool
	Log        *slog.Logger
}

const principalKey = "pff.principal"

// PrincipalFrom returns the caller set by Middleware. It panics if called on a
// route without the middleware, which is a programming error.
func PrincipalFrom(c *gin.Context) *Principal {
	return c.MustGet(principalKey).(*Principal)
}

// Middleware authenticates and authorises every request. It does not enforce
// the second factor; mount RequireMFA on routes that expose financial data.
func (a *Authenticator) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		raw, ok := strings.CutPrefix(h, "Bearer ")
		if !ok || strings.TrimSpace(raw) == "" {
			httperr.Abort(c, http.StatusUnauthorized, "unauthenticated", "missing bearer token")
			return
		}
		ctx := c.Request.Context()
		id, err := a.Verifier.Verify(ctx, strings.TrimSpace(raw))
		if err != nil {
			httperr.Abort(c, http.StatusUnauthorized, "unauthenticated", "invalid or expired token")
			return
		}
		if id.SignInProvider != "google.com" {
			httperr.Abort(c, http.StatusForbidden, "provider_not_allowed", "sign in with Google")
			return
		}
		if id.Email == "" || !id.EmailVerified {
			httperr.Abort(c, http.StatusForbidden, "email_not_verified", "a verified e-mail address is required")
			return
		}
		id.Email = strings.ToLower(id.Email)

		user, allowed, emailMFADone, err := a.Store.Authorize(ctx, id)
		if err != nil {
			a.Log.Error("authorize user failed", "err", err)
			httperr.Abort(c, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		if !allowed {
			// Deny by default. Don't reveal anything else about the account.
			httperr.Abort(c, http.StatusForbidden, "not_whitelisted", "this account is not allowed to use PFF")
			return
		}

		p := &Principal{User: user, Identity: id, MFAState: MFAStateOK}
		switch {
		case id.SecondFactor != "":
			p.MFAMethod = id.SecondFactor
		case user.EmailMFAEnabled && emailMFADone:
			p.MFAMethod = "email"
		case user.EmailMFAEnabled:
			p.MFAState = MFAStateRequired
		case a.RequireMFA:
			p.MFAState = MFAStateEnrollmentRequired
		}
		c.Set(principalKey, p)
		c.Next()
	}
}

// RequireMFA rejects callers whose session has not satisfied the second-factor
// policy.
func RequireMFA() gin.HandlerFunc {
	return func(c *gin.Context) {
		p := PrincipalFrom(c)
		switch p.MFAState {
		case MFAStateRequired:
			httperr.Abort(c, http.StatusForbidden, MFAStateRequired, "complete the second authentication factor")
			return
		case MFAStateEnrollmentRequired:
			httperr.Abort(c, http.StatusForbidden, MFAStateEnrollmentRequired, "enrol a second authentication factor")
			return
		}
		c.Next()
	}
}

// ---- PostgreSQL store -----------------------------------------------------

// PGStore implements UserStore on PostgreSQL.
type PGStore struct {
	Pool *pgxpool.Pool
}

// lastSeenResolution bounds how often a request writes app_user.last_seen_at:
// the hot path is a single read; a write happens at most this often per user.
const lastSeenResolution = 10 * time.Minute

// Authorize implements UserStore with one read in the common case. Users are
// keyed by their (verified, whitelisted) e-mail, so a re-created Firebase
// account keeps its data.
func (s PGStore) Authorize(ctx context.Context, id Identity) (User, bool, bool, error) {
	u := User{Email: id.Email}
	var (
		userID   *string
		uid      string
		lastSeen *time.Time
		mfaDone  bool
	)
	err := s.Pool.QueryRow(ctx, `
		SELECT u.id::text, COALESCE(u.display_name, ''), COALESCE(u.email_mfa_enabled, FALSE),
		       COALESCE(u.firebase_uid, ''), u.last_seen_at,
		       EXISTS (SELECT 1 FROM mfa_session m WHERE m.user_id = u.id AND m.auth_time = $2)
		FROM user_whitelist w LEFT JOIN app_user u ON u.email = w.email
		WHERE w.email = $1 AND w.enabled`, id.Email, id.AuthTime).
		Scan(&userID, &u.Name, &u.EmailMFAEnabled, &uid, &lastSeen, &mfaDone)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, false, false, nil // not whitelisted
	}
	if err != nil {
		return User{}, false, false, err
	}

	if userID == nil { // first sign-in
		err = s.Pool.QueryRow(ctx, `
			INSERT INTO app_user (email, firebase_uid, display_name) VALUES ($1, $2, NULLIF($3, ''))
			ON CONFLICT (email) DO UPDATE SET firebase_uid = EXCLUDED.firebase_uid, last_seen_at = now()
			RETURNING id::text, COALESCE(display_name, ''), email_mfa_enabled`,
			id.Email, id.UID, id.Name).Scan(&u.ID, &u.Name, &u.EmailMFAEnabled)
		return u, true, false, err
	}
	u.ID = *userID
	if uid != id.UID || lastSeen == nil || time.Since(*lastSeen) > lastSeenResolution {
		if _, err := s.Pool.Exec(ctx, `
			UPDATE app_user SET firebase_uid = $2, display_name = COALESCE(NULLIF($3, ''), display_name), last_seen_at = now()
			WHERE id = $1`, u.ID, id.UID, id.Name); err != nil {
			return User{}, false, false, err
		}
	}
	return u, true, mfaDone, nil
}
