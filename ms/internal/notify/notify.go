// Package notify stores in-app notifications and delivers them as push
// notifications to the user's registered devices (Firebase Cloud Messaging).
package notify

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"firebase.google.com/go/v4/messaging"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Notification is a message for one user.
type Notification struct {
	Kind  string // budget_warning, budget_exceeded, consent_expiring, consent_expired
	Title string
	Body  string
	Link  string // path inside the web app, e.g. "/budgets"
}

// Pusher delivers a notification to device tokens and reports tokens that are
// no longer valid.
type Pusher interface {
	Push(ctx context.Context, tokens []string, n Notification) (invalid []string, err error)
}

// Service persists and delivers notifications.
type Service struct {
	Pool   *pgxpool.Pool
	Pusher Pusher
	Log    *slog.Logger
}

// Notify stores the notification and pushes it to every device of the user.
// Push failures are logged, never returned: the in-app copy is the record.
func (s *Service) Notify(ctx context.Context, userID string, n Notification) error {
	if _, err := s.Pool.Exec(ctx, `
		INSERT INTO notification (user_id, kind, title, body, link) VALUES ($1, $2, $3, $4, NULLIF($5, ''))`,
		userID, n.Kind, n.Title, n.Body, n.Link); err != nil {
		return err
	}
	rows, err := s.Pool.Query(ctx, `SELECT token FROM push_device WHERE user_id = $1`, userID)
	if err != nil {
		return err
	}
	tokens, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(tokens) == 0 {
		return err
	}
	invalid, err := s.Pusher.Push(ctx, tokens, n)
	if err != nil {
		s.Log.Warn("push notification", "err", err)
	}
	if len(invalid) > 0 {
		if _, err := s.Pool.Exec(ctx, `DELETE FROM push_device WHERE token = ANY($1)`, invalid); err != nil {
			s.Log.Error("remove invalid push tokens", "err", err)
		}
	}
	return nil
}

// Item is a stored notification.
type Item struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	Link      string     `json:"link"`
	CreatedAt time.Time  `json:"created_at"`
	ReadAt    *time.Time `json:"read_at"`
}

// List returns the user's most recent notifications and the unread count.
func (s *Service) List(ctx context.Context, userID string, limit int) ([]Item, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id::text, kind, title, body, COALESCE(link, ''), created_at, read_at
		FROM notification WHERE user_id = $1 ORDER BY created_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, 0, err
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Item, error) {
		var it Item
		err := r.Scan(&it.ID, &it.Kind, &it.Title, &it.Body, &it.Link, &it.CreatedAt, &it.ReadAt)
		return it, err
	})
	if err != nil {
		return nil, 0, err
	}
	if items == nil {
		items = []Item{}
	}
	var unread int
	err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM notification WHERE user_id = $1 AND read_at IS NULL`, userID).Scan(&unread)
	return items, unread, err
}

// MarkRead marks one notification (or all, when id is empty) as read.
func (s *Service) MarkRead(ctx context.Context, userID, id string) error {
	if id == "" {
		_, err := s.Pool.Exec(ctx, `UPDATE notification SET read_at = now() WHERE user_id = $1 AND read_at IS NULL`, userID)
		return err
	}
	_, err := s.Pool.Exec(ctx, `UPDATE notification SET read_at = now() WHERE user_id = $1 AND id::text = $2 AND read_at IS NULL`, userID, id)
	return err
}

// RegisterDevice stores an FCM registration token for the user. A token moves
// to the latest user that registers it (shared browsers).
func (s *Service) RegisterDevice(ctx context.Context, userID, token, userAgent string) error {
	if len(userAgent) > 300 {
		userAgent = userAgent[:300]
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO push_device (token, user_id, user_agent) VALUES ($1, $2, $3)
		ON CONFLICT (token) DO UPDATE SET user_id = EXCLUDED.user_id, user_agent = EXCLUDED.user_agent, last_seen_at = now()`,
		token, userID, userAgent)
	return err
}

// UnregisterDevice removes a token of the user.
func (s *Service) UnregisterDevice(ctx context.Context, userID, token string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM push_device WHERE token = $1 AND user_id = $2`, token, userID)
	return err
}

// ---- pushers -------------------------------------------------------------------

// FCMPusher sends web push notifications through Firebase Cloud Messaging.
type FCMPusher struct {
	Client *messaging.Client
	AppURL string // absolute origin of the web app, used for click-through links
}

// Push implements Pusher.
func (p FCMPusher) Push(ctx context.Context, tokens []string, n Notification) ([]string, error) {
	msg := &messaging.MulticastMessage{
		Tokens:       tokens,
		Notification: &messaging.Notification{Title: n.Title, Body: n.Body},
		Data:         map[string]string{"kind": n.Kind, "link": n.Link},
		Webpush: &messaging.WebpushConfig{
			Notification: &messaging.WebpushNotification{Icon: "/icons/icon-192.png", Tag: n.Kind},
		},
	}
	// FCM only accepts HTTPS click-through links.
	if strings.HasPrefix(p.AppURL, "https://") {
		msg.Webpush.FCMOptions = &messaging.WebpushFCMOptions{Link: p.AppURL + n.Link}
	}
	resp, err := p.Client.SendEachForMulticast(ctx, msg)
	if err != nil {
		return nil, err
	}
	var invalid []string
	for i, r := range resp.Responses {
		// Only drop tokens FCM reports as gone. INVALID_ARGUMENT can also mean a
		// bad payload, which must not wipe every device.
		if r.Error != nil && (messaging.IsRegistrationTokenNotRegistered(r.Error) || messaging.IsUnregistered(r.Error)) {
			invalid = append(invalid, tokens[i])
		}
	}
	return invalid, nil
}

// LogPusher logs notifications instead of pushing them (development).
type LogPusher struct {
	Log *slog.Logger
}

// Push implements Pusher.
func (p LogPusher) Push(_ context.Context, tokens []string, n Notification) ([]string, error) {
	p.Log.Info("push notification (log only)", "devices", len(tokens), "kind", n.Kind, "title", n.Title, "body", n.Body)
	return nil, nil
}
