package handlers

import (
	"github.com/ecsistem/convtrack/internal/auth"
	"github.com/ecsistem/convtrack/internal/models"
	"github.com/ecsistem/convtrack/internal/videojobs"
	fiber "github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AdminHandler struct {
	db        *pgxpool.Pool
	authSvc   *auth.Service
	videoJobs *videojobs.Queue // nil se a fila de vídeo estiver desativada
}

func NewAdmin(db *pgxpool.Pool, authSvc *auth.Service, videoJobs *videojobs.Queue) *AdminHandler {
	return &AdminHandler{db: db, authSvc: authSvc, videoJobs: videoJobs}
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
	limit := c.QueryInt("limit", 50)
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

	// Gerentes não podem alterar NADA de uma conta de administrador —
	// nem status, plano, quota ou qualquer outro campo. Só admins tocam em admins.
	if !callerIsAdmin {
		var targetIsAdmin bool
		_ = h.db.QueryRow(c.Context(), `SELECT is_admin FROM accounts WHERE id=$1`, id).Scan(&targetIsAdmin)
		if targetIsAdmin {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "gerentes não podem alterar contas de administrador"})
		}
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

// Impersonate  POST /v1/admin/accounts/:id/impersonate
// Gera um token de sessão para a conta alvo, permitindo o admin acessar o
// painel do cliente como se tivesse logado. Somente admins — gerentes não
// podem impersonar ninguém (acesso a dados de qualquer conta é sensível
// demais para o escopo do cargo de gerente).
func (h *AdminHandler) Impersonate(c *fiber.Ctx) error {
	callerIsAdmin, _ := c.Locals("caller_is_admin").(bool)
	if !callerIsAdmin {
		return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "apenas admins podem acessar o painel de outra conta"})
	}

	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "id inválido"})
	}

	adminAccountID, _ := c.Locals("account_id").(uuid.UUID)

	result, err := h.authSvc.ImpersonateAccount(c.Context(), id)
	if err != nil {
		return c.Status(404).JSON(fiber.Map{"error": err.Error()})
	}

	if adminAccountID != uuid.Nil {
		_, _ = h.db.Exec(c.Context(), `
			INSERT INTO admin_impersonations (admin_account_id, target_account_id)
			VALUES ($1, $2)`, adminAccountID, id)
	}

	return c.JSON(fiber.Map{
		"account":       result.Account,
		"access_token":  result.AccessToken,
		"refresh_token": result.RefreshToken,
	})
}

// Creatives  GET /v1/admin/creatives?type=image|video&page=&limit=
// Lista criativos camuflados (imagem e/ou vídeo) de TODAS as contas, com
// nome da conta/projeto, para o admin auditar o que foi gerado na plataforma.
func (h *AdminHandler) Creatives(c *fiber.Ctx) error {
	kind := c.Query("type", "")
	limit := c.QueryInt("limit", 50)
	offset := c.QueryInt("offset", 0)
	if limit > 200 {
		limit = 200
	}

	type creative struct {
		ID          string `json:"id"`
		Kind        string `json:"kind"` // "image" | "video"
		Filename    string `json:"filename"`
		Technique   string `json:"technique,omitempty"`
		ProjectID   string `json:"project_id"`
		ProjectName string `json:"project_name"`
		AccountName string `json:"account_name"`
		AccountID   string `json:"account_id"`
		SizeBytes   int64  `json:"size_bytes"`
		Status      string `json:"status,omitempty"`
		CreatedAt   string `json:"created_at"`
	}
	var out []creative

	if kind == "" || kind == "image" {
		rows, err := h.db.Query(c.Context(), `
			SELECT il.id, il.filename, il.technique, il.size_bytes, il.created_at,
			       p.id, p.name, a.id, a.name
			FROM imgcamo_log il
			JOIN projects p ON p.id = il.project_id
			JOIN accounts a ON a.id = p.account_id
			ORDER BY il.created_at DESC
			LIMIT $1 OFFSET $2`, limit, offset)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var cr creative
				if err := rows.Scan(&cr.ID, &cr.Filename, &cr.Technique, &cr.SizeBytes, &cr.CreatedAt,
					&cr.ProjectID, &cr.ProjectName, &cr.AccountID, &cr.AccountName); err == nil {
					cr.Kind = "image"
					out = append(out, cr)
				}
			}
		}
	}

	if (kind == "" || kind == "video") && h.videoJobs != nil {
		jobs := h.videoJobs.ListAll()
		// Pagina a fila em memória do mesmo jeito que a query SQL (offset/limit).
		// Para a visão combinada (kind=""), evita duplicar vídeos em toda página.
		videoOffset, videoLimit := offset, limit
		if kind == "" {
			if offset > 0 {
				videoOffset = len(jobs) // pula vídeos nas páginas seguintes — só aparecem na 1ª
			} else {
				videoLimit = limit
			}
		}
		if videoOffset < len(jobs) {
			end := videoOffset + videoLimit
			if end > len(jobs) {
				end = len(jobs)
			}
			jobs = jobs[videoOffset:end]
		} else {
			jobs = nil
		}
		if len(jobs) > 0 {
			projectIDs := make([]string, 0, len(jobs))
			seen := map[string]bool{}
			for _, j := range jobs {
				if !seen[j.ProjectID] {
					seen[j.ProjectID] = true
					projectIDs = append(projectIDs, j.ProjectID)
				}
			}
			names := map[string][2]string{} // projectID -> [projectName, accountName+accountID combo handled below]
			rows, err := h.db.Query(c.Context(), `
				SELECT p.id, p.name, a.id, a.name
				FROM projects p JOIN accounts a ON a.id = p.account_id
				WHERE p.id = ANY($1)`, projectIDs)
			accNames := map[string]string{}
			accIDs := map[string]string{}
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var pid, pname, aid, aname string
					if rows.Scan(&pid, &pname, &aid, &aname) == nil {
						names[pid] = [2]string{pname, aname}
						accNames[pid] = aname
						accIDs[pid] = aid
					}
				}
			}
			for _, j := range jobs {
				n := names[j.ProjectID]
				out = append(out, creative{
					ID: j.ID, Kind: "video", Filename: j.Filename, Technique: j.Preset,
					ProjectID: j.ProjectID, ProjectName: n[0], AccountName: n[1], AccountID: accIDs[j.ProjectID],
					SizeBytes: j.Size, Status: string(j.Status), CreatedAt: j.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
				})
			}
		}
	}

	return c.JSON(fiber.Map{"data": out})
}

// DownloadCreative  GET /v1/admin/creatives/:id/download?type=image|video
func (h *AdminHandler) DownloadCreative(c *fiber.Ctx) error {
	kind := c.Query("type", "image")
	id := c.Params("id")

	if kind == "video" {
		if h.videoJobs == nil {
			return c.Status(404).JSON(fiber.Map{"error": "fila de vídeo desativada"})
		}
		path, filename, ok := h.videoJobs.ResultPathAny(id)
		if !ok {
			return c.Status(404).JSON(fiber.Map{"error": "resultado não disponível"})
		}
		c.Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		return c.SendFile(path)
	}

	var storagePath, filename string
	err := h.db.QueryRow(c.Context(),
		`SELECT storage_path, filename FROM imgcamo_log WHERE id=$1`, id,
	).Scan(&storagePath, &filename)
	if err != nil || storagePath == "" {
		return c.Status(404).JSON(fiber.Map{"error": "criativo não encontrado"})
	}
	c.Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	return c.SendFile(storagePath)
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
