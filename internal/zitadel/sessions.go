package zitadel

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	sessionV2 "github.com/zitadel/zitadel-go/v3/pkg/client/zitadel/session/v2"
)

// Session is the Limen-shaped view of a Zitadel session.
type Session struct {
	ID        string
	Token     string
	ExpiresAt time.Time
	// UserID is the Zitadel user `sub` checked into the session, if any.
	UserID string
}

// CreateSession creates a new Zitadel session pre-authenticated for userID.
// lifetime is the absolute session TTL; pass 0 to omit and use Zitadel's
// instance default. The returned Session.Token must be stored alongside the
// ID to perform subsequent GetSession lookups.
func (c *Client) CreateSession(ctx context.Context, userID string, lifetime time.Duration) (*Session, error) {
	req := &sessionV2.CreateSessionRequest{
		Checks: &sessionV2.Checks{
			User: &sessionV2.CheckUser{
				Search: &sessionV2.CheckUser_UserId{UserId: userID},
			},
		},
	}
	if lifetime > 0 {
		req.Lifetime = durationpb.New(lifetime)
	}

	resp, err := c.api.SessionServiceV2().CreateSession(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("zitadel: create session for user %q: %w", userID, err)
	}
	return &Session{
		ID:     resp.GetSessionId(),
		Token:  resp.GetSessionToken(),
		UserID: userID,
	}, nil
}

// GetSession fetches a session by id. The token bound at creation time is
// required to authorize the read (Zitadel rejects token-less reads unless
// the calling principal has session.read on the user's org).
func (c *Client) GetSession(ctx context.Context, id, token string) (*Session, error) {
	req := &sessionV2.GetSessionRequest{SessionId: id}
	if token != "" {
		req.SessionToken = &token
	}
	resp, err := c.api.SessionServiceV2().GetSession(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("zitadel: get session %q: %w", id, err)
	}
	s := resp.GetSession()
	if s == nil {
		return nil, fmt.Errorf("zitadel: get session %q: empty response", id)
	}
	out := &Session{ID: s.GetId(), Token: token}
	if exp := s.GetExpirationDate(); exp != nil {
		out.ExpiresAt = exp.AsTime()
	}
	if f := s.GetFactors(); f != nil && f.GetUser() != nil {
		out.UserID = f.GetUser().GetId()
	}
	return out, nil
}

// DeleteSession terminates a Zitadel session. Idempotent — deleting an
// already-deleted session returns no error.
func (c *Client) DeleteSession(ctx context.Context, id, token string) error {
	req := &sessionV2.DeleteSessionRequest{SessionId: id}
	if token != "" {
		req.SessionToken = &token
	}
	if _, err := c.api.SessionServiceV2().DeleteSession(ctx, req); err != nil {
		return fmt.Errorf("zitadel: delete session %q: %w", id, err)
	}
	return nil
}
