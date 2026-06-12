package adsync

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ecsistem/convtrack/internal/crypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// secretFields espelha os campos cifrados em repouso pelo handler de integrações.
var secretFields = map[string]bool{
	"access_token":   true,
	"api_secret":     true,
	"developer_token": true,
	"client_secret":  true,
	"refresh_token":  true,
}

// LoadConfig lê a config de uma integração ativa e decifra os campos secretos.
// Retorna erro se a integração não existir ou estiver desabilitada.
func LoadConfig(ctx context.Context, db *pgxpool.Pool, projectID uuid.UUID, platform string) (map[string]string, error) {
	var raw []byte
	err := db.QueryRow(ctx,
		`SELECT config FROM integration_settings
		 WHERE project_id = $1 AND platform = $2 AND enabled = true`,
		projectID, platform,
	).Scan(&raw)
	if err != nil {
		return nil, fmt.Errorf("%s integration not configured or disabled", platform)
	}

	var rawCfg map[string]interface{}
	if err := json.Unmarshal(raw, &rawCfg); err != nil {
		return nil, err
	}

	cfg := make(map[string]string, len(rawCfg))
	for k, v := range rawCfg {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if secretFields[k] {
			if plain, derr := crypto.DecryptString(s); derr == nil {
				s = plain
			}
		}
		cfg[k] = s
	}
	return cfg, nil
}
