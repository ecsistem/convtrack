package handlers

import "testing"

func TestValidEmail(t *testing.T) {
	valid := []string{
		"user@example.com",
		"a.b+tag@sub.domain.co",
		"edson.13.10.2004@gmail.com",
	}
	for _, e := range valid {
		if !validEmail(e) {
			t.Errorf("esperava válido: %q", e)
		}
	}

	invalid := []string{
		"",
		"semarroba",
		"a@",
		"@b.com",
		"a b@c.com",
		"Nome <a@b.com>", // display name não bate addr==input
		"a@b.com extra",
	}
	for _, e := range invalid {
		if validEmail(e) {
			t.Errorf("esperava inválido: %q", e)
		}
	}
}
