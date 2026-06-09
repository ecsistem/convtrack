// Package auth handles account registration, login and JWT issuance.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/ecsistem/convtrack/internal/models"
)

const (
	accessTokenTTL  = 15 * time.Minute
	refreshTokenTTL = 30 * 24 * time.Hour
	bcryptCost      = 12
)

// ─── Errors ───────────────────────────────────────────────────────────────────

var (
	ErrEmailTaken       = errors.New("email already registered")
	ErrInvalidCreds     = errors.New("invalid email or password")
	ErrInvalidToken     = errors.New("invalid or expired token")
)

// ─── Claims ───────────────────────────────────────────────────────────────────

type Claims struct {
	AccountID uuid.UUID `json:"sub"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	jwt.RegisteredClaims
}

// ─── Service ──────────────────────────────────────────────────────────────────

type Service struct {
	db        *pgxpool.Pool
	jwtSecret []byte
}

func New(db *pgxpool.Pool) *Service {
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		secret = "changeme-set-JWT_SECRET-in-production"
	}
	return &Service{db: db, jwtSecret: []byte(secret)}
}

// ─── Register ─────────────────────────────────────────────────────────────────

type RegisterResult struct {
	Account      *models.Account
	Project      *models.Project
	AccessToken  string
	RefreshToken string
}

// Register creates an account + first project and returns tokens.
func (s *Service) Register(ctx context.Context, name, email, password string) (*RegisterResult, error) {
	// Check if email is taken
	var exists bool
	_ = s.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM accounts WHERE lower(email) = lower($1))`, email,
	).Scan(&exists)
	if exists {
		return nil, ErrEmailTaken
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	accountID := uuid.New()
	var account models.Account
	err = s.db.QueryRow(ctx, `
		INSERT INTO accounts (id, name, email, plan, sessions_quota, password_hash)
		VALUES ($1, $2, $3, 'free', 10000, $4)
		RETURNING id, name, email, plan, sessions_quota, created_at`,
		accountID, name, email, string(hash),
	).Scan(&account.ID, &account.Name, &account.Email, &account.Plan, &account.SessionsQuota, &account.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create account: %w", err)
	}

	// Create first project automatically
	project, err := s.createProject(ctx, account.ID, name+" — Projeto 1", "")
	if err != nil {
		return nil, fmt.Errorf("create project: %w", err)
	}

	access, refresh, err := s.issueTokens(ctx, &account)
	if err != nil {
		return nil, err
	}

	return &RegisterResult{
		Account:      &account,
		Project:      project,
		AccessToken:  access,
		RefreshToken: refresh,
	}, nil
}

// ─── Login ────────────────────────────────────────────────────────────────────

type LoginResult struct {
	Account      *models.Account
	AccessToken  string
	RefreshToken string
}

// Login validates credentials and returns tokens.
func (s *Service) Login(ctx context.Context, email, password string) (*LoginResult, error) {
	var account models.Account
	err := s.db.QueryRow(ctx, `
		SELECT id, name, email, plan, sessions_quota, password_hash, created_at
		FROM accounts WHERE lower(email) = lower($1)`, email,
	).Scan(
		&account.ID, &account.Name, &account.Email, &account.Plan,
		&account.SessionsQuota, &account.PasswordHash, &account.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidCreds
	}
	if err != nil {
		return nil, fmt.Errorf("login query: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(account.PasswordHash), []byte(password)); err != nil {
		return nil, ErrInvalidCreds
	}

	access, refresh, err := s.issueTokens(ctx, &account)
	if err != nil {
		return nil, err
	}

	return &LoginResult{Account: &account, AccessToken: access, RefreshToken: refresh}, nil
}

// ─── Refresh ──────────────────────────────────────────────────────────────────

// Refresh validates a refresh token and issues a new access token.
// The old refresh token is rotated (deleted) and a new one is issued.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (accessToken, newRefreshToken string, err error) {
	tokenHash := hashToken(refreshToken)

	var accountID uuid.UUID
	var expiresAt time.Time
	var tokenID uuid.UUID
	err = s.db.QueryRow(ctx, `
		SELECT id, account_id, expires_at FROM auth_tokens WHERE token_hash = $1`, tokenHash,
	).Scan(&tokenID, &accountID, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrInvalidToken
	}
	if err != nil {
		return "", "", fmt.Errorf("refresh query: %w", err)
	}
	if time.Now().After(expiresAt) {
		_, _ = s.db.Exec(ctx, `DELETE FROM auth_tokens WHERE id = $1`, tokenID)
		return "", "", ErrInvalidToken
	}

	// Delete old token (rotation)
	_, _ = s.db.Exec(ctx, `DELETE FROM auth_tokens WHERE id = $1`, tokenID)

	var account models.Account
	err = s.db.QueryRow(ctx,
		`SELECT id, name, email, plan, sessions_quota, created_at FROM accounts WHERE id = $1`, accountID,
	).Scan(&account.ID, &account.Name, &account.Email, &account.Plan, &account.SessionsQuota, &account.CreatedAt)
	if err != nil {
		return "", "", fmt.Errorf("load account: %w", err)
	}

	accessToken, newRefreshToken, err = s.issueTokens(ctx, &account)
	return
}

// ─── Logout ───────────────────────────────────────────────────────────────────

// Logout invalidates the given refresh token.
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	tokenHash := hashToken(refreshToken)
	_, err := s.db.Exec(ctx, `DELETE FROM auth_tokens WHERE token_hash = $1`, tokenHash)
	return err
}

// ─── ValidateAccessToken ──────────────────────────────────────────────────────

// ValidateAccessToken parses and validates a JWT access token.
func (s *Service) ValidateAccessToken(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.jwtSecret, nil
	})
	if err != nil || !token.Valid {
		return nil, ErrInvalidToken
	}
	claims, ok := token.Claims.(*Claims)
	if !ok {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

// ─── Projects (used during register and projects API) ─────────────────────────

func (s *Service) createProject(ctx context.Context, accountID uuid.UUID, name, domain string) (*models.Project, error) {
	id := uuid.New()
	apiKey := newAPIKey()
	if domain == "" {
		domain = "localhost"
	}
	var p models.Project
	err := s.db.QueryRow(ctx, `
		INSERT INTO projects (id, account_id, name, domain, api_key)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, account_id, name, domain, api_key, clone_protection, created_at`,
		id, accountID, name, domain, apiKey,
	).Scan(&p.ID, &p.AccountID, &p.Name, &p.Domain, &p.APIKey, &p.CloneProtection, &p.CreatedAt)
	return &p, err
}

func (s *Service) ListProjects(ctx context.Context, accountID uuid.UUID) ([]models.Project, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, account_id, name, domain, api_key, clone_protection, created_at
		 FROM projects WHERE account_id = $1 ORDER BY created_at ASC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []models.Project
	for rows.Next() {
		var p models.Project
		if err := rows.Scan(&p.ID, &p.AccountID, &p.Name, &p.Domain, &p.APIKey, &p.CloneProtection, &p.CreatedAt); err != nil {
			continue
		}
		list = append(list, p)
	}
	return list, nil
}

func (s *Service) CreateProject(ctx context.Context, accountID uuid.UUID, name, domain string) (*models.Project, error) {
	return s.createProject(ctx, accountID, name, domain)
}

func (s *Service) GetProject(ctx context.Context, projectID, accountID uuid.UUID) (*models.Project, error) {
	var p models.Project
	err := s.db.QueryRow(ctx, `
		SELECT id, account_id, name, domain, api_key, clone_protection, created_at
		FROM projects WHERE id = $1 AND account_id = $2`, projectID, accountID,
	).Scan(&p.ID, &p.AccountID, &p.Name, &p.Domain, &p.APIKey, &p.CloneProtection, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("project not found")
	}
	return &p, err
}

func (s *Service) UpdateProject(ctx context.Context, projectID, accountID uuid.UUID, name, domain string) (*models.Project, error) {
	var p models.Project
	err := s.db.QueryRow(ctx, `
		UPDATE projects SET name = $3, domain = $4
		WHERE id = $1 AND account_id = $2
		RETURNING id, account_id, name, domain, api_key, clone_protection, created_at`,
		projectID, accountID, name, domain,
	).Scan(&p.ID, &p.AccountID, &p.Name, &p.Domain, &p.APIKey, &p.CloneProtection, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("project not found")
	}
	return &p, err
}

func (s *Service) DeleteProject(ctx context.Context, projectID, accountID uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM projects WHERE id = $1 AND account_id = $2`, projectID, accountID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("project not found")
	}
	return nil
}

// SetCloneProtection enables or disables the clone redirect feature for a project.
func (s *Service) SetCloneProtection(ctx context.Context, projectID, accountID uuid.UUID, enabled bool) (*models.Project, error) {
	var p models.Project
	err := s.db.QueryRow(ctx, `
		UPDATE projects SET clone_protection = $3
		WHERE id = $1 AND account_id = $2
		RETURNING id, account_id, name, domain, api_key, clone_protection, created_at`,
		projectID, accountID, enabled,
	).Scan(&p.ID, &p.AccountID, &p.Name, &p.Domain, &p.APIKey, &p.CloneProtection, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("project not found")
	}
	return &p, err
}

func (s *Service) RotateAPIKey(ctx context.Context, projectID, accountID uuid.UUID) (*models.Project, error) {
	newKey := newAPIKey()
	var p models.Project
	err := s.db.QueryRow(ctx, `
		UPDATE projects SET api_key = $3
		WHERE id = $1 AND account_id = $2
		RETURNING id, account_id, name, domain, api_key, clone_protection, created_at`,
		projectID, accountID, newKey,
	).Scan(&p.ID, &p.AccountID, &p.Name, &p.Domain, &p.APIKey, &p.CloneProtection, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("project not found")
	}
	return &p, err
}

// LoadProjectForAccount loads a project and verifies it belongs to the account.
func (s *Service) LoadProjectForAccount(ctx context.Context, projectID, accountID uuid.UUID) (*models.Project, error) {
	return s.GetProject(ctx, projectID, accountID)
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func (s *Service) issueTokens(ctx context.Context, account *models.Account) (accessToken, refreshToken string, err error) {
	// Access token (JWT)
	now := time.Now()
	claims := Claims{
		AccountID: account.ID,
		Email:     account.Email,
		Name:      account.Name,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(accessTokenTTL)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	accessToken, err = token.SignedString(s.jwtSecret)
	if err != nil {
		return "", "", fmt.Errorf("sign token: %w", err)
	}

	// Refresh token (random 32 bytes stored as hex)
	rawRefresh := make([]byte, 32)
	if _, err = rand.Read(rawRefresh); err != nil {
		return "", "", fmt.Errorf("gen refresh token: %w", err)
	}
	refreshToken = hex.EncodeToString(rawRefresh)
	tokenHash := hashToken(refreshToken)

	_, err = s.db.Exec(ctx, `
		INSERT INTO auth_tokens (account_id, token_hash, expires_at)
		VALUES ($1, $2, $3)`,
		account.ID, tokenHash, now.Add(refreshTokenTTL),
	)
	if err != nil {
		return "", "", fmt.Errorf("store refresh token: %w", err)
	}

	return accessToken, refreshToken, nil
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func newAPIKey() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return "ct_" + hex.EncodeToString(b)
}
