package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/ecsistem/convtrack/internal/mailer"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const (
	passwordResetTTL = 1 * time.Hour
	emailVerifyTTL   = 24 * time.Hour
)

func newToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ─── Password reset ───────────────────────────────────────────────────────────

// RequestPasswordReset gera um token e envia o email de reset. Para não vazar
// quais emails existem, sempre retorna nil mesmo se a conta não existir.
func (s *Service) RequestPasswordReset(ctx context.Context, email string) error {
	var accountID uuid.UUID
	err := s.db.QueryRow(ctx,
		`SELECT id FROM accounts WHERE lower(email) = lower($1)`, email,
	).Scan(&accountID)
	if err != nil {
		// conta não existe — silenciosamente OK (anti-enumeration)
		return nil
	}

	token := newToken()
	_, err = s.db.Exec(ctx, `
		INSERT INTO password_reset_tokens (token_hash, account_id, expires_at)
		VALUES ($1, $2, $3)`,
		hashToken(token), accountID, time.Now().Add(passwordResetTTL),
	)
	if err != nil {
		return fmt.Errorf("create reset token: %w", err)
	}

	resetURL := fmt.Sprintf("%s/reset-password?token=%s", s.appURL, token)
	subject, html := mailer.PasswordResetEmail(resetURL)
	if mErr := s.mailer.Send(email, subject, html); mErr != nil {
		return fmt.Errorf("send reset email: %w", mErr)
	}
	return nil
}

// ResetPassword valida o token e troca a senha. Invalida o token e todos os
// refresh tokens da conta.
func (s *Service) ResetPassword(ctx context.Context, token, newPassword string) error {
	if len(newPassword) < 8 {
		return fmt.Errorf("a senha deve ter ao menos 8 caracteres")
	}

	var accountID uuid.UUID
	var expiresAt time.Time
	var usedAt *time.Time
	err := s.db.QueryRow(ctx, `
		SELECT account_id, expires_at, used_at
		FROM password_reset_tokens WHERE token_hash = $1`,
		hashToken(token),
	).Scan(&accountID, &expiresAt, &usedAt)
	if err != nil {
		return ErrInvalidToken
	}
	if usedAt != nil || time.Now().After(expiresAt) {
		return ErrInvalidToken
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcryptCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`UPDATE accounts SET password_hash = $1 WHERE id = $2`, string(hash), accountID,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE password_reset_tokens SET used_at = NOW() WHERE token_hash = $1`, hashToken(token),
	); err != nil {
		return err
	}
	// invalida sessões existentes
	if _, err := tx.Exec(ctx,
		`DELETE FROM auth_tokens WHERE account_id = $1`, accountID,
	); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ─── Email verification ───────────────────────────────────────────────────────

// SendVerificationEmail gera um token e envia o email de confirmação.
func (s *Service) SendVerificationEmail(ctx context.Context, accountID uuid.UUID, email string) error {
	token := newToken()
	_, err := s.db.Exec(ctx, `
		INSERT INTO email_verification_tokens (token_hash, account_id, expires_at)
		VALUES ($1, $2, $3)`,
		hashToken(token), accountID, time.Now().Add(emailVerifyTTL),
	)
	if err != nil {
		return fmt.Errorf("create verify token: %w", err)
	}

	verifyURL := fmt.Sprintf("%s/verify-email?token=%s", s.appURL, token)
	subject, html := mailer.VerificationEmail(verifyURL)
	return s.mailer.Send(email, subject, html)
}

// VerifyEmail valida o token e marca a conta como verificada.
func (s *Service) VerifyEmail(ctx context.Context, token string) error {
	var accountID uuid.UUID
	var expiresAt time.Time
	var usedAt *time.Time
	err := s.db.QueryRow(ctx, `
		SELECT account_id, expires_at, used_at
		FROM email_verification_tokens WHERE token_hash = $1`,
		hashToken(token),
	).Scan(&accountID, &expiresAt, &usedAt)
	if err != nil {
		return ErrInvalidToken
	}
	if usedAt != nil || time.Now().After(expiresAt) {
		return ErrInvalidToken
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`UPDATE accounts SET email_verified = TRUE WHERE id = $1`, accountID,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE email_verification_tokens SET used_at = NOW() WHERE token_hash = $1`, hashToken(token),
	); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
