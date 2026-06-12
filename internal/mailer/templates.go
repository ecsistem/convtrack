package mailer

import "fmt"

func layout(title, body string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="pt-BR"><head><meta charset="utf-8"></head>
<body style="margin:0;background:#0b0b12;font-family:-apple-system,Segoe UI,Roboto,sans-serif;color:#e5e5e5;padding:32px 0">
  <table role="presentation" width="100%%" cellpadding="0" cellspacing="0">
    <tr><td align="center">
      <table role="presentation" width="480" cellpadding="0" cellspacing="0" style="background:#13131d;border:1px solid rgba(255,255,255,.07);border-radius:16px;overflow:hidden">
        <tr><td style="padding:28px 32px 8px">
          <div style="font-size:18px;font-weight:700;color:#fff">⚡ ConvTrack</div>
        </td></tr>
        <tr><td style="padding:8px 32px 32px">
          <h1 style="font-size:18px;color:#fff;margin:16px 0 12px">%s</h1>
          %s
        </td></tr>
      </table>
      <p style="color:#52525b;font-size:11px;margin-top:16px">ConvTrack · Este é um email automático, não responda.</p>
    </td></tr>
  </table>
</body></html>`, title, body)
}

func button(url, label string) string {
	return fmt.Sprintf(`<a href="%s" style="display:inline-block;background:#7c3aed;color:#fff;text-decoration:none;font-weight:600;font-size:14px;padding:12px 24px;border-radius:10px;margin:8px 0">%s</a>`, url, label)
}

// PasswordResetEmail monta o assunto e corpo HTML do email de reset.
func PasswordResetEmail(resetURL string) (subject, html string) {
	subject = "Redefinição de senha — ConvTrack"
	body := fmt.Sprintf(`
		<p style="color:#a1a1aa;font-size:14px;line-height:1.6">
			Recebemos um pedido para redefinir a senha da sua conta. Clique no botão abaixo para criar uma nova senha. O link expira em <strong>1 hora</strong>.
		</p>
		<p style="margin:20px 0">%s</p>
		<p style="color:#71717a;font-size:12px;line-height:1.6">
			Se você não solicitou isso, pode ignorar este email — sua senha continua a mesma.<br>
			Se o botão não funcionar, copie e cole no navegador:<br>
			<span style="color:#7c3aed;word-break:break-all">%s</span>
		</p>`, button(resetURL, "Redefinir senha"), resetURL)
	return subject, layout("Redefinir sua senha", body)
}

// VerificationEmail monta o assunto e corpo HTML do email de verificação.
func VerificationEmail(verifyURL string) (subject, html string) {
	subject = "Confirme seu email — ConvTrack"
	body := fmt.Sprintf(`
		<p style="color:#a1a1aa;font-size:14px;line-height:1.6">
			Bem-vindo ao ConvTrack! Confirme seu endereço de email para ativar todos os recursos da sua conta.
		</p>
		<p style="margin:20px 0">%s</p>
		<p style="color:#71717a;font-size:12px;line-height:1.6">
			Se o botão não funcionar, copie e cole no navegador:<br>
			<span style="color:#7c3aed;word-break:break-all">%s</span>
		</p>`, button(verifyURL, "Confirmar email"), verifyURL)
	return subject, layout("Confirme seu email", body)
}
