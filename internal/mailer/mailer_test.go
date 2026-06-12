package mailer

import (
	"strings"
	"testing"
)

func TestStripHTML(t *testing.T) {
	got := stripHTML("<p>Olá <strong>mundo</strong></p>")
	if strings.Contains(got, "<") || strings.Contains(got, ">") {
		t.Errorf("stripHTML deixou tags: %q", got)
	}
	if !strings.Contains(got, "mundo") {
		t.Errorf("stripHTML removeu conteúdo: %q", got)
	}
}

func TestConfigured(t *testing.T) {
	m := &Mailer{}
	if m.Configured() {
		t.Error("mailer vazio não deveria estar configurado")
	}
	m2 := &Mailer{host: "smtp.x.com", user: "u", pass: "p", from: "a@x.com"}
	if !m2.Configured() {
		t.Error("mailer completo deveria estar configurado")
	}
}

func TestBuildMessage(t *testing.T) {
	msg := string(buildMessage("a@x.com", "ConvTrack", "b@y.com", "Assunto", "<p>oi</p>"))
	for _, want := range []string{"From: ConvTrack <a@x.com>", "To: b@y.com", "Subject: Assunto", "text/html"} {
		if !strings.Contains(msg, want) {
			t.Errorf("mensagem não contém %q\n%s", want, msg)
		}
	}
}

func TestPasswordResetEmail(t *testing.T) {
	subject, html := PasswordResetEmail("https://app/reset?token=abc")
	if subject == "" {
		t.Error("subject vazio")
	}
	if !strings.Contains(html, "https://app/reset?token=abc") {
		t.Error("html não contém a URL de reset")
	}
}

func TestOrDefault(t *testing.T) {
	if orDefault("", "x") != "x" {
		t.Error("orDefault vazio deveria retornar default")
	}
	if orDefault("y", "x") != "y" {
		t.Error("orDefault não vazio deveria retornar valor")
	}
}
