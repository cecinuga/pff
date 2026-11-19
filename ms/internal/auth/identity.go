// Package auth authenticates API callers and authorises them against the
// deny-by-default e-mail whitelist.
//
// Flow per request:
//  1. Verify the bearer token (Firebase ID token, or a dev token in development).
//  2. Require a verified e-mail signed in with Google.
//  3. Require the e-mail to be enabled in user_whitelist (deny by default).
//  4. Load or create the app_user row.
//  5. Work out whether the session satisfied a second factor: Firebase TOTP/SMS
//     (token claim) or the backend's e-mail one-time code (mfa_session row).
package auth

import (
	"context"
	"errors"
	"time"
)

// Identity is what a verified token says about the caller.
type Identity struct {
	UID           string
	Email         string
	EmailVerified bool
	Name          string
	// SignInProvider is the first-factor provider, e.g. "google.com".
	SignInProvider string
	// SecondFactor is the Firebase second factor used in this sign-in ("phone",
	// "totp"), or empty when none was used.
	SecondFactor string
	// AuthTime is when the user signed in; it identifies the sign-in session
	// across ID-token refreshes.
	AuthTime time.Time
}

// Verifier validates a bearer token.
type Verifier interface {
	Verify(ctx context.Context, token string) (Identity, error)
}

// ErrInvalidToken is returned for any token that fails verification.
var ErrInvalidToken = errors.New("invalid or expired token")
