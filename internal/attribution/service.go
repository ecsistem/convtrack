package attribution

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/ecsistem/convtrack/internal/models"
	"github.com/ecsistem/convtrack/internal/session"
	"github.com/google/uuid"
)

type Service struct {
	sessions *session.Service
}

func New(sessions *session.Service) *Service {
	return &Service{sessions: sessions}
}

// ResolveForConversion finds the best session attribution for a conversion.
// It tries sessionID first, then falls back to email hash lookup.
func (s *Service) ResolveForConversion(
	ctx context.Context,
	projectID uuid.UUID,
	sessionID *uuid.UUID,
	email, phone string,
) (*models.Attribution, *models.Session, error) {
	if sessionID != nil {
		sess, err := s.sessions.FindSessionByEmailHash(ctx, projectID, "")
		_ = sess
		// Try direct session attribution
		attr, err := s.sessions.GetAttribution(ctx, *sessionID)
		if err == nil {
			sess2, _ := s.sessions.UpsertSession(ctx, session.UpsertSessionInput{SessionID: *sessionID, ProjectID: projectID})
			return attr, sess2, nil
		}
	}

	// Fallback: find by email hash
	if email != "" {
		hash := HashIdentifier(email)
		sess, err := s.sessions.FindSessionByEmailHash(ctx, projectID, hash)
		if err == nil {
			attr, attrErr := s.sessions.GetAttribution(ctx, sess.ID)
			if attrErr != nil {
				// Session exists but no attribution data yet — return empty attribution
				return &models.Attribution{SessionID: sess.ID, ProjectID: projectID}, sess, nil
			}
			return attr, sess, nil
		}
	}

	// Fallback: find by phone hash
	if phone != "" {
		hash := HashIdentifier(phone)
		sess, err := s.sessions.FindSessionByEmailHash(ctx, projectID, hash)
		if err == nil {
			attr, _ := s.sessions.GetAttribution(ctx, sess.ID)
			return attr, sess, nil
		}
	}

	return nil, nil, fmt.Errorf("no session found for this conversion")
}

// HashIdentifier creates a SHA-256 hash of a normalized identifier (email or phone)
func HashIdentifier(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	h := sha256.Sum256([]byte(normalized))
	return fmt.Sprintf("%x", h)
}
