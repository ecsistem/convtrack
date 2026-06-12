// Package mailer envia emails transacionais (reset de senha, verificação).
// Usa SMTP configurado via env. Sem SMTP configurado, cai em modo "log":
// imprime o conteúdo no stdout (útil em desenvolvimento).
package mailer

import (
	"crypto/tls"
	"fmt"
	"net/smtp"
	"os"
	"strings"
)

type Mailer struct {
	host    string
	port    string
	user    string
	pass    string
	from    string
	appName string
}

// New lê a configuração SMTP do ambiente.
//
//	SMTP_HOST, SMTP_PORT, SMTP_USER, SMTP_PASS, SMTP_FROM
func New() *Mailer {
	from := os.Getenv("SMTP_FROM")
	if from == "" {
		from = os.Getenv("SMTP_USER")
	}
	return &Mailer{
		host:    os.Getenv("SMTP_HOST"),
		port:    orDefault(os.Getenv("SMTP_PORT"), "587"),
		user:    os.Getenv("SMTP_USER"),
		pass:    os.Getenv("SMTP_PASS"),
		from:    from,
		appName: orDefault(os.Getenv("APP_NAME"), "ConvTrack"),
	}
}

// Configured indica se o SMTP está pronto para envio real.
func (m *Mailer) Configured() bool {
	return m.host != "" && m.user != "" && m.pass != "" && m.from != ""
}

// Send envia um email HTML. Em modo log (SMTP não configurado), imprime e
// retorna nil para não quebrar o fluxo.
func (m *Mailer) Send(to, subject, htmlBody string) error {
	if !m.Configured() {
		fmt.Printf("\n📧 [mailer:log] para=%s assunto=%q\n%s\n\n", to, subject, stripHTML(htmlBody))
		return nil
	}

	addr := m.host + ":" + m.port
	msg := buildMessage(m.from, m.appName, to, subject, htmlBody)

	auth := smtp.PlainAuth("", m.user, m.pass, m.host)

	// Porta 465 → SMTPS (TLS implícito). Caso contrário, STARTTLS via smtp.SendMail.
	if m.port == "465" {
		return m.sendTLS(addr, auth, to, msg)
	}
	return smtp.SendMail(addr, auth, m.from, []string{to}, msg)
}

func (m *Mailer) sendTLS(addr string, auth smtp.Auth, to string, msg []byte) error {
	tlsCfg := &tls.Config{ServerName: m.host}
	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		return fmt.Errorf("smtp tls dial: %w", err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, m.host)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer client.Quit()

	if err := client.Auth(auth); err != nil {
		return fmt.Errorf("smtp auth: %w", err)
	}
	if err := client.Mail(m.from); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	return w.Close()
}

func buildMessage(from, fromName, to, subject, htmlBody string) []byte {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("From: %s <%s>\r\n", fromName, from))
	b.WriteString(fmt.Sprintf("To: %s\r\n", to))
	b.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
	b.WriteString("\r\n")
	b.WriteString(htmlBody)
	return []byte(b.String())
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func stripHTML(s string) string {
	// remoção tosca de tags só para o preview de log
	out := s
	for {
		i := strings.Index(out, "<")
		if i < 0 {
			break
		}
		j := strings.Index(out[i:], ">")
		if j < 0 {
			break
		}
		out = out[:i] + " " + out[i+j+1:]
	}
	return strings.TrimSpace(out)
}
