# Deploy ConvTrack — aaPanel (宝塔面板)

> Guia completo para subir o ConvTrack em produção usando aaPanel como gerenciador do servidor e Docker Compose para os serviços.

---

## Arquitetura em produção

```
Internet (80 / 443)
        │
        ▼
    Caddy (Docker)
    ├── api.convtrack.com      → api:8080  (Go)
    ├── dashboard.convtrack.com → web:3000  (Next.js)
    └── oferta.cliente.com     → api:8080  (On-Demand TLS automático)
        │
        ▼
  Docker Network interna
  ├── api:8080    (Go Fiber)
  ├── web:3000    (Next.js)
  ├── postgres:5432
  └── redis:6379

aaPanel → gerencia Docker, firewall, monitoramento, SSH
```

> **Importante:** o aaPanel **não cria sites Nginx** para o ConvTrack. O Caddy cuida de todo o SSL e proxy. O aaPanel é usado apenas como painel de controle do servidor (Docker, firewall, logs).

---

## Pré-requisitos

| Item | Mínimo recomendado |
|---|---|
| VPS | 2 vCPU, 2 GB RAM, 40 GB SSD |
| OS | Ubuntu 22.04 LTS ou Debian 12 |
| IP | 1 IP público fixo |
| Domínios | `api.seudominio.com` + `dashboard.seudominio.com` |

---

## 1. Instalar o aaPanel

Conecte via SSH como root:

```bash
ssh root@SEU_IP
```

Instalar aaPanel (Ubuntu/Debian):

```bash
wget -O install.sh https://www.aapanel.com/script/install-ubuntu_6.0_en.sh
bash install.sh
```

Ao final, o instalador mostra:

```
aaPanel Internet Address: http://SEU_IP:7800/xxxxxxxx
username: xxxxxxxxxxxx
password: xxxxxxxxxxxx
```

Acesse o painel e complete a instalação inicial. **Quando perguntar qual stack instalar, clique em "Cancel" — não precisa instalar LAMP/LNMP.**

---

## 2. Instalar Docker via aaPanel

No painel aaPanel:

1. Vá em **App Store** (ícone de loja)
2. Procure por **Docker**
3. Clique em **Install**
4. Aguarde a instalação completa

Verificar no terminal:

```bash
docker --version
docker compose version
```

---

## 3. Abrir portas no firewall

No aaPanel → **Security** → **Firewall**, libere as portas:

| Porta | Protocolo | Descrição |
|---|---|---|
| 80 | TCP | HTTP (Caddy + redirect) |
| 443 | TCP+UDP | HTTPS + HTTP/3 (Caddy) |
| 7800 | TCP | aaPanel (já aberta) |

> **Não abra** as portas 8080, 3000, 5432 ou 6379 — ficam apenas na rede interna Docker.

No terminal, confirme as regras:

```bash
ufw allow 80/tcp
ufw allow 443/tcp
ufw allow 443/udp
ufw reload
```

---

## 4. Configurar DNS

No seu provedor de DNS, crie os registros:

```
# Registro A — aponta o domínio principal para o IP do servidor
api.seudominio.com       A     SEU_IP_PUBLICO
dashboard.seudominio.com A     SEU_IP_PUBLICO

# Domínios de clientes — CNAME aponta para sua API
oferta.cliente.com       CNAME api.seudominio.com.
```

> Aguarde a propagação (geralmente 5–30 min). Verifique com:
> ```bash
> dig +short api.seudominio.com
> ```

---

## 5. Clonar o repositório

```bash
cd /opt
git clone https://github.com/SEU_USUARIO/convtrack.git
cd convtrack
```

---

## 6. Configurar variáveis de ambiente

```bash
cp .env.example .env   # se existir
nano .env
```

Preencha o arquivo `.env`:

```env
# ── Banco de dados ────────────────────────────────────────────────
POSTGRES_USER=convtrack
POSTGRES_PASSWORD=SenhaPostgresForte123!
POSTGRES_DB=convtrack

# ── Autenticação ──────────────────────────────────────────────────
# Gere com: openssl rand -base64 32
JWT_SECRET=GERE_AQUI_COM_OPENSSL
ENCRYPTION_KEY=GERE_AQUI_COM_OPENSSL

# ── URLs públicas ─────────────────────────────────────────────────
API_BASE_URL=https://api.seudominio.com
NEXT_PUBLIC_API_URL=https://api.seudominio.com
FRONTEND_ORIGIN=https://dashboard.seudominio.com

# ── Caddy ────────────────────────────────────────────────────────
CADDY_DOMAIN=api.seudominio.com
CADDY_EMAIL=seu@email.com

# ── S3 para session replay (opcional) ────────────────────────────
S3_ENDPOINT=
S3_REGION=us-east-1
S3_ACCESS_KEY_ID=
S3_SECRET_ACCESS_KEY=
S3_DEFAULT_BUCKET=convtrack-replays
```

Gere as chaves seguras:

```bash
# Rode duas vezes — uma para JWT_SECRET, outra para ENCRYPTION_KEY
openssl rand -base64 32
```

---

## 7. Ajustar o docker-compose.yml para produção

O arquivo já está configurado. Apenas confirme que a linha de porta da API está comentada (não expor 8080 diretamente):

```bash
nano docker-compose.yml
```

Procure o serviço `api` e comente a porta:

```yaml
api:
  # ...
  ports:
    # Em produção com Caddy: mantenha comentado
    # - "8080:8080"
```

Salve com `Ctrl+O`, saia com `Ctrl+X`.

---

## 8. Ajustar o Caddyfile para o dashboard

Abra o `Caddyfile` e adicione o site do dashboard:

```bash
nano Caddyfile
```

Adicione o bloco do dashboard logo após o bloco do domínio principal:

```caddy
{
  email {$CADDY_EMAIL:ssl@convtrack.com}

  on_demand_tls {
    ask http://api:8080/v1/shield/domain-ask
    interval 2m
    burst    5
  }
}

# API principal
{$CADDY_DOMAIN:localhost} {
  reverse_proxy api:8080
}

# Dashboard Next.js
dashboard.seudominio.com {
  reverse_proxy web:3000
}

# Catch-all — domínios de clientes CNAME
:443 {
  tls {
    on_demand
  }
  reverse_proxy api:8080
}

:80 {
  redir https://{host}{uri} 308
}
```

> Substitua `dashboard.seudominio.com` pelo seu domínio real.

---

## 9. Subir os serviços

```bash
cd /opt/convtrack

# Build e start em background
docker compose up -d --build

# Acompanhar os logs (aguarde a mensagem "migrations applied")
docker compose logs -f api
```

Verificar se todos os containers estão rodando:

```bash
docker compose ps
```

Saída esperada:

```
NAME                STATUS          PORTS
convtrack-api-1     Up (healthy)    
convtrack-caddy-1   Up              0.0.0.0:80->80/tcp, 0.0.0.0:443->443/tcp
convtrack-postgres-1 Up (healthy)   
convtrack-redis-1   Up (healthy)    
convtrack-web-1     Up              
```

---

## 10. Testar a instalação

```bash
# API respondendo
curl https://api.seudominio.com/health
# Esperado: {"ok":true}

# Dashboard acessível
curl -I https://dashboard.seudominio.com
# Esperado: HTTP/2 200
```

Acesse o dashboard no browser:

```
https://dashboard.seudominio.com
```

Crie sua conta e o primeiro projeto.

---

## 11. Adicionar domínio de cliente (fluxo completo)

1. **Cliente cria o CNAME**:
   ```
   oferta.cliente.com  CNAME  api.seudominio.com.
   ```

2. **No dashboard** → Shield → Domínios → Novo domínio:
   - Domain: `oferta.cliente.com`
   - Campanha: selecione a campanha
   - SSL: ✅ ativo

3. **Na primeira visita** a `https://oferta.cliente.com`:
   - Caddy detecta que não tem cert
   - Chama `GET http://api:8080/v1/shield/domain-ask?domain=oferta.cliente.com`
   - Go confirma: domínio cadastrado + ssl_enabled=true → 200 OK
   - Caddy solicita cert Let's Encrypt (automático, em segundos)
   - Visitante recebe a página cloakada com HTTPS ✅

4. **Verificar** no dashboard → Shield → Domínios → clique em 🔍:
   - CNAME ✅
   - TLS ✅

---

## 12. Atualizar o ConvTrack

Quando houver uma nova versão:

```bash
cd /opt/convtrack

# Baixar atualizações
git pull

# Rebuild e restart apenas dos serviços que mudaram
docker compose up -d --build api web

# Verificar
docker compose ps
docker compose logs -f api
```

As migrations são aplicadas automaticamente na inicialização do `api`.

---

## 13. Comandos úteis

```bash
# Ver logs em tempo real
docker compose logs -f

# Logs só da API
docker compose logs -f api

# Reiniciar um serviço específico
docker compose restart api

# Parar tudo
docker compose down

# Parar e apagar volumes (CUIDADO — apaga o banco)
docker compose down -v

# Entrar no container da API
docker compose exec api sh

# Entrar no Postgres
docker compose exec postgres psql -U convtrack -d convtrack

# Ver uso de recursos
docker stats

# Checar certs do Caddy
docker compose exec caddy caddy list-certs
```

---

## 14. Backup do banco de dados

Crie um script de backup diário:

```bash
nano /opt/convtrack/scripts/backup.sh
```

```bash
#!/bin/bash
DATE=$(date +%Y%m%d_%H%M%S)
BACKUP_DIR=/opt/backups/convtrack

mkdir -p "$BACKUP_DIR"

docker compose -f /opt/convtrack/docker-compose.yml exec -T postgres \
  pg_dump -U convtrack convtrack \
  | gzip > "$BACKUP_DIR/convtrack_$DATE.sql.gz"

# Manter apenas os últimos 7 backups
find "$BACKUP_DIR" -name "*.sql.gz" -mtime +7 -delete

echo "Backup concluído: convtrack_$DATE.sql.gz"
```

```bash
chmod +x /opt/convtrack/scripts/backup.sh

# Agendar via crontab (todo dia às 3h)
crontab -e
```

Adicione a linha:

```cron
0 3 * * * /opt/convtrack/scripts/backup.sh >> /var/log/convtrack-backup.log 2>&1
```

---

## 15. Monitoramento no aaPanel

No aaPanel você pode:

- **Monitorar CPU/RAM/Disco**: Menu → *System* → *Monitor*
- **Ver logs do servidor**: Menu → *Log*
- **Gerenciar Docker**: App Store → Docker → *Manager*
- **Alertas de uso**: *Settings* → *Alert* (configure limite de RAM/CPU)

---

## Troubleshooting

### Caddy não consegue emitir cert

```bash
docker compose logs caddy | grep -i "error\|acme\|tls"
```

Causas comuns:
- CNAME ainda não propagou (aguarde ou teste com `dig`)
- Porta 80 bloqueada no firewall do provedor (verifique o painel da VPS também)
- `ask` endpoint retornando 403 (domínio não cadastrado ou `ssl_enabled=false`)

### API não responde

```bash
docker compose logs api | tail -50
docker compose ps api  # checar se está "healthy"
```

### Banco não conecta

```bash
docker compose logs postgres | tail -20
# Verificar se o volume tem permissão
ls -la /var/lib/docker/volumes/ | grep pgdata
```

### Resetar e reinstalar do zero

```bash
cd /opt/convtrack
docker compose down -v    # APAGA TUDO incluindo o banco
docker compose up -d --build
```

---

## Referências

- [aaPanel Docs](https://www.aapanel.com/docs.html)
- [Caddy On-Demand TLS](https://caddyserver.com/docs/automatic-https#on-demand-tls)
- [ip-api.com](https://ip-api.com/docs/api:json) — geolocalização de IPs
- [cobe](https://github.com/shuding/cobe) — globe 3D do dashboard
