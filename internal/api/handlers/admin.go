package handlers

import (
	"github.com/ecsistem/convtrack/internal/models"
	fiber "github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AdminHandler struct {
	db *pgxpool.Pool
}

func NewAdmin(db *pgxpool.Pool) *AdminHandler {
	return &AdminHandler{db: db}
}

// AdminAccount extends Account with extra admin-only info.
type AdminAccount struct {
	models.Account
	ProjectCount int `json:"project_count"`
}

// ListAccounts  GET /v1/admin/accounts?status=&search=&page=&limit=
func (h *AdminHandler) ListAccounts(c *fiber.Ctx) error {
	status := c.Query("status", "")
	search := c.Query("search", "")
	limit  := c.QueryInt("limit", 50)
	offset := c.QueryInt("offset", 0)
	if limit > 200 {
		limit = 200
	}

	args := []any{}
	where := "WHERE 1=1"
	i := 1
	if status != "" {
		where += " AND a.status = $" + itoa(i)
		args = append(args, status)
		i++
	}
	if search != "" {
		where += " AND (lower(a.name) LIKE lower($" + itoa(i) + ") OR lower(a.email) LIKE lower($" + itoa(i) + "))"
		args = append(args, "%"+search+"%")
		i++
	}
	args = append(args, limit, offset)

	rows, err := h.db.Query(c.Context(), `
		SELECT a.id, a.name, a.email, a.plan, a.sessions_quota,
		       a.is_admin, a.is_manager, a.is_affiliate, a.status, a.email_verified, a.created_at,
		       COUNT(p.id) AS project_count
		FROM accounts a
		LEFT JOIN projects p ON p.account_id = a.id
		`+where+`
		GROUP BY a.id
		ORDER BY a.created_at DESC
		LIMIT $`+itoa(i)+` OFFSET $`+itoa(i+1),
		args...,
	)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()

	list := []AdminAccount{}
	for rows.Next() {
		var acc AdminAccount
		if err := rows.Scan(
			&acc.ID, &acc.Name, &acc.Email, &acc.Plan, &acc.SessionsQuota,
			&acc.IsAdmin, &acc.IsManager, &acc.IsAffiliate, &acc.Status, &acc.EmailVerified, &acc.CreatedAt,
			&acc.ProjectCount,
		); err != nil {
			continue
		}
		list = append(list, acc)
	}

	// total count
	countArgs := args[:len(args)-2]
	var total int
	_ = h.db.QueryRow(c.Context(),
		`SELECT COUNT(*) FROM accounts a `+where, countArgs...,
	).Scan(&total)

	return c.JSON(fiber.Map{"data": list, "total": total})
}

// GetAccount  GET /v1/admin/accounts/:id
func (h *AdminHandler) GetAccount(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "id inválido"})
	}
	var acc AdminAccount
	err = h.db.QueryRow(c.Context(), `
		SELECT a.id, a.name, a.email, a.plan, a.sessions_quota,
		       a.is_admin, a.is_manager, a.is_affiliate, a.status, a.email_verified, a.created_at,
		       COUNT(p.id) AS project_count
		FROM accounts a
		LEFT JOIN projects p ON p.account_id = a.id
		WHERE a.id = $1
		GROUP BY a.id`, id,
	).Scan(
		&acc.ID, &acc.Name, &acc.Email, &acc.Plan, &acc.SessionsQuota,
		&acc.IsAdmin, &acc.IsManager, &acc.IsAffiliate, &acc.Status, &acc.EmailVerified, &acc.CreatedAt,
		&acc.ProjectCount,
	)
	if err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "conta não encontrada"})
	}
	return c.JSON(acc)
}

// UpdateAccount  PUT /v1/admin/accounts/:id
// Body: { status?, plan?, sessions_quota?, is_admin?, is_manager? }
func (h *AdminHandler) UpdateAccount(c *fiber.Ctx) error {
	callerIsAdmin, _ := c.Locals("caller_is_admin").(bool)

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "id inválido"})
	}
	var body struct {
		Status        *string `json:"status"`
		Plan          *string `json:"plan"`
		SessionsQuota *int    `json:"sessions_quota"`
		IsAdmin       *bool   `json:"is_admin"`
		IsManager     *bool   `json:"is_manager"`
	}
	if err := c.BodyParser(&body); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "json inválido"})
	}

	// Managers cannot touch the is_admin flag.
	if body.IsAdmin != nil && !callerIsAdmin {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "apenas admins podem alterar o cargo de admin"})
	}

	if body.Status != nil {
		switch *body.Status {
		case "approved", "pending", "suspended":
		default:
			return c.Status(400).JSON(fiber.Map{"error": "status inválido"})
		}
		if _, err := h.db.Exec(c.Context(), `UPDATE accounts SET status=$1 WHERE id=$2`, *body.Status, id); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		// Revoke all sessions immediately when suspending or moving back to pending.
		if *body.Status == "suspended" || *body.Status == "pending" {
			_, _ = h.db.Exec(c.Context(), `DELETE FROM auth_tokens WHERE account_id=$1`, id)
		}
	}
	if body.Plan != nil {
		if _, err := h.db.Exec(c.Context(), `UPDATE accounts SET plan=$1 WHERE id=$2`, *body.Plan, id); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
	}
	if body.SessionsQuota != nil {
		if _, err := h.db.Exec(c.Context(), `UPDATE accounts SET sessions_quota=$1 WHERE id=$2`, *body.SessionsQuota, id); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
	}
	if body.IsAdmin != nil {
		if _, err := h.db.Exec(c.Context(), `UPDATE accounts SET is_admin=$1 WHERE id=$2`, *body.IsAdmin, id); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
	}
	if body.IsManager != nil {
		if _, err := h.db.Exec(c.Context(), `UPDATE accounts SET is_manager=$1 WHERE id=$2`, *body.IsManager, id); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
	}
	return c.JSON(fiber.Map{"ok": true})
}

// DeleteAccount  DELETE /v1/admin/accounts/:id
func (h *AdminHandler) DeleteAccount(c *fiber.Ctx) error {
	callerIsAdmin, _ := c.Locals("caller_is_admin").(bool)

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "id inválido"})
	}

	// Managers cannot delete admin accounts.
	if !callerIsAdmin {
		var targetIsAdmin bool
		_ = h.db.QueryRow(c.Context(), `SELECT is_admin FROM accounts WHERE id=$1`, id).Scan(&targetIsAdmin)
		if targetIsAdmin {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "gerentes não podem deletar contas de admin"})
		}
	}

	if _, err := h.db.Exec(c.Context(), `DELETE FROM accounts WHERE id=$1`, id); err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"ok": true})
}

// Stats  GET /v1/admin/stats
func (h *AdminHandler) Stats(c *fiber.Ctx) error {
	var total, pending, approved, suspended int
	_ = h.db.QueryRow(c.Context(), `SELECT COUNT(*) FROM accounts`).Scan(&total)
	_ = h.db.QueryRow(c.Context(), `SELECT COUNT(*) FROM accounts WHERE status='pending'`).Scan(&pending)
	_ = h.db.QueryRow(c.Context(), `SELECT COUNT(*) FROM accounts WHERE status='approved'`).Scan(&approved)
	_ = h.db.QueryRow(c.Context(), `SELECT COUNT(*) FROM accounts WHERE status='suspended'`).Scan(&suspended)
	return c.JSON(fiber.Map{
		"total": total, "pending": pending,
		"approved": approved, "suspended": suspended,
	})
}

func itoa(n int) string {
	const digits = "0123456789"
	if n < 10 {
		return string(digits[n])
	}
	return string(digits[n/10]) + string(digits[n%10])
}
