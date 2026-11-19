package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	fbauth "firebase.google.com/go/v4/auth"
)

// FirebaseVerifier verifies Firebase ID tokens and rejects revoked sessions and
// disabled users.
type FirebaseVerifier struct {
	Client *fbauth.Client
}

// Verify implements Verifier.
func (v FirebaseVerifier) Verify(ctx context.Context, raw string) (Identity, error) {
	tok, err := v.Client.VerifyIDTokenAndCheckRevoked(ctx, raw)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	id := Identity{
		UID:            tok.UID,
		SignInProvider: tok.Firebase.SignInProvider,
		AuthTime:       time.Unix(tok.AuthTime, 0).UTC(),
	}
	id.Email, _ = tok.Claims["email"].(string)
	id.EmailVerified, _ = tok.Claims["email_verified"].(bool)
	id.Name, _ = tok.Claims["name"].(string)
	if fb, ok := tok.Claims["firebase"].(map[string]any); ok {
		id.SecondFactor, _ = fb["sign_in_second_factor"].(string)
	}
	return id, nil
}

// DevVerifier accepts tokens of the form "dev:<email>" so the whole stack can
// run locally without a Firebase project. config.Load refuses it outside
// PFF_ENV=development.
type DevVerifier struct {
	// Started is used as the sign-in time of every dev session.
	Started time.Time
}

// Verify implements Verifier.
func (v DevVerifier) Verify(_ context.Context, raw string) (Identity, error) {
	email, ok := strings.CutPrefix(raw, "dev:")
	email = strings.ToLower(strings.TrimSpace(email))
	if !ok || !strings.Contains(email, "@") {
		return Identity{}, ErrInvalidToken
	}
	return Identity{
		UID:            "dev-" + email,
		Email:          email,
		EmailVerified:  true,
		Name:           strings.SplitN(email, "@", 2)[0],
		SignInProvider: "google.com",
		AuthTime:       v.Started.UTC().Truncate(time.Second),
	}, nil
}
