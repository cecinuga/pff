package auth

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ParseAllowedEmails reads one e-mail per line. Blank lines and lines starting
// with '#' are ignored; addresses are lower-cased.
func ParseAllowedEmails(r io.Reader) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	line := 0
	for sc.Scan() {
		line++
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		s = strings.ToLower(s)
		at := strings.Index(s, "@")
		if at < 1 || at == len(s)-1 || strings.ContainsAny(s, " \t,;") {
			return nil, fmt.Errorf("allowed e-mails line %d: %q is not an e-mail address", line, s)
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, sc.Err()
}

// SyncWhitelistFile makes the 'file' rows of user_whitelist match the file:
// listed addresses are enabled, file rows no longer listed are disabled.
// Rows with source 'manual' are left alone. It returns the number of enabled
// addresses from the file.
func SyncWhitelistFile(ctx context.Context, pool *pgxpool.Pool, path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open allowed e-mails file: %w", err)
	}
	defer f.Close()
	emails, err := ParseAllowedEmails(f)
	if err != nil {
		return 0, err
	}
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		for _, e := range emails {
			if _, err := tx.Exec(ctx, `
				INSERT INTO user_whitelist (email, source, enabled) VALUES ($1, 'file', TRUE)
				ON CONFLICT (email) DO UPDATE SET enabled = TRUE, updated_at = now()
				WHERE user_whitelist.source = 'file' AND NOT user_whitelist.enabled`, e); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `
			UPDATE user_whitelist SET enabled = FALSE, updated_at = now()
			WHERE source = 'file' AND enabled AND NOT (email = ANY($1))`, emails)
		return err
	})
	return len(emails), err
}
