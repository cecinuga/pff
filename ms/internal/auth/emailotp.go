package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cecinuga/pff/ms/internal/platform/mail"
	"github.com/cecinuga/pff/ms/internal/platform/secretbox"
)

// E-mail one-time-code policy.
const (
	otpDigits          = 6
	otpTTL             = 10 * time.Minute
	otpMaxAttempts     = 5
	otpMaxPerWindow    = 5
	otpRateLimitWindow = 15 * time.Minute
)

// Purposes of an e-mail challenge.
const (
	PurposeLogin  = "login"
	PurposeEnroll = "enroll"
)

// Errors returned by EmailOTP.
var (
	ErrOTPRateLimited   = errors.New("too many codes requested, try again later")
	ErrOTPInvalid       = errors.New("the code is wrong or has expired")
	ErrOTPNotEnrolled   = errors.New("e-mail second factor is not enabled")
	ErrOTPAlreadyActive = errors.New("e-mail second factor is already enabled")
	ErrOTPNeedsMFA      = errors.New("complete the second factor before changing it")
)

// EmailOTP implements e-mail as a second factor. Firebase supports only SMS and
// TOTP natively, so PFF issues and checks these codes itself. A verified code
// marks the current sign-in session (user, auth_time) as MFA-complete.
type EmailOTP struct {
	Pool   *pgxpool.Pool
	Box    *secretbox.Box
	Mailer mail.Sender
	Now    func() time.Time
}

// Challenge e-mails a fresh code and returns the challenge id and expiry.
func (o *EmailOTP) Challenge(ctx context.Context, p *Principal, purpose string) (string, time.Time, error) {
	switch purpose {
	case PurposeLogin:
		if !p.User.EmailMFAEnabled {
			return "", time.Time{}, ErrOTPNotEnrolled
		}
	case PurposeEnroll:
		if p.User.EmailMFAEnabled {
			return "", time.Time{}, ErrOTPAlreadyActive
		}
	default:
		return "", time.Time{}, fmt.Errorf("unknown purpose %q", purpose)
	}

	now := o.Now()
	var recent int
	if err := o.Pool.QueryRow(ctx,
		`SELECT count(*) FROM mfa_email_challenge WHERE user_id = $1 AND created_at > $2`,
		p.User.ID, now.Add(-otpRateLimitWindow)).Scan(&recent); err != nil {
		return "", time.Time{}, err
	}
	if recent >= otpMaxPerWindow {
		return "", time.Time{}, ErrOTPRateLimited
	}

	code, err := randomDigits(otpDigits)
	if err != nil {
		return "", time.Time{}, err
	}
	var id string
	expires := now.Add(otpTTL)
	// The id is generated first so the MAC can bind the code to it.
	if err := o.Pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		return "", time.Time{}, err
	}
	hash := o.Box.MAC([]byte(id), []byte(code))
	if _, err := o.Pool.Exec(ctx, `
		INSERT INTO mfa_email_challenge (id, user_id, purpose, code_hash, auth_time, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		id, p.User.ID, purpose, hash, p.Identity.AuthTime, expires, now); err != nil {
		return "", time.Time{}, err
	}
	body := fmt.Sprintf("Your PFF verification code is %s.\n\nIt expires in %d minutes. If you did not try to sign in, ignore this e-mail and consider changing your Google password.\n", code, int(otpTTL.Minutes()))
	if err := o.Mailer.Send(ctx, p.User.Email, "PFF verification code", body); err != nil {
		return "", time.Time{}, fmt.Errorf("send code: %w", err)
	}
	return id, expires, nil
}

// Verify checks a code. On success the current sign-in session is marked as
// MFA-complete and, for an enrolment, e-mail MFA is enabled for the user.
func (o *EmailOTP) Verify(ctx context.Context, p *Principal, challengeID, code string) error {
	now := o.Now()
	tx, err := o.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit

	var (
		purpose    string
		hash       []byte
		authTime   time.Time
		attempts   int
		expiresAt  time.Time
		consumedAt *time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT purpose, code_hash, auth_time, attempts, expires_at, consumed_at
		FROM mfa_email_challenge WHERE id::text = $1 AND user_id = $2 FOR UPDATE`,
		challengeID, p.User.ID).Scan(&purpose, &hash, &authTime, &attempts, &expiresAt, &consumedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrOTPInvalid
	}
	if err != nil {
		return err
	}
	if consumedAt != nil || now.After(expiresAt) || attempts >= otpMaxAttempts || !authTime.Equal(p.Identity.AuthTime) {
		return ErrOTPInvalid
	}
	if _, err := tx.Exec(ctx, `UPDATE mfa_email_challenge SET attempts = attempts + 1 WHERE id::text = $1`, challengeID); err != nil {
		return err
	}
	if !hmac.Equal(hash, o.Box.MAC([]byte(challengeID), []byte(code))) {
		// Persist the failed attempt so guesses are bounded, then fail.
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return ErrOTPInvalid
	}
	if _, err := tx.Exec(ctx, `UPDATE mfa_email_challenge SET consumed_at = $2 WHERE id::text = $1`, challengeID, now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO mfa_session (user_id, auth_time, method, verified_at) VALUES ($1, $2, 'email', $3)
		ON CONFLICT (user_id, auth_time) DO NOTHING`, p.User.ID, authTime, now); err != nil {
		return err
	}
	if purpose == PurposeEnroll {
		if _, err := tx.Exec(ctx, `UPDATE app_user SET email_mfa_enabled = TRUE WHERE id = $1`, p.User.ID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Disable turns e-mail MFA off. The caller must already have passed a second
// factor in this session, so a stolen first factor cannot remove it.
func (o *EmailOTP) Disable(ctx context.Context, p *Principal) error {
	if !p.User.EmailMFAEnabled {
		return ErrOTPNotEnrolled
	}
	if p.MFAState != MFAStateOK || p.MFAMethod == "" {
		return ErrOTPNeedsMFA
	}
	return pgx.BeginFunc(ctx, o.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE app_user SET email_mfa_enabled = FALSE WHERE id = $1`, p.User.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM mfa_session WHERE user_id = $1 AND method = 'email'`, p.User.ID)
		return err
	})
}

func randomDigits(n int) (string, error) {
	max := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
	v, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%0*d", n, v), nil
}
