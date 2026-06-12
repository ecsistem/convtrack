package adsync

import "testing"

func TestParseInt64(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"0", 0},
		{"1234567", 1234567},
		{"", 0},
		{"12.5", 0},   // não numérico puro
		{"-5", 0},     // sinal inválido
		{"abc", 0},
		{"1000000", 1000000},
	}
	for _, c := range cases {
		if got := parseInt64(c.in); got != c.want {
			t.Errorf("parseInt64(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello world", 5); got != "hello" {
		t.Errorf("truncate = %q, want %q", got, "hello")
	}
	if got := truncate("hi", 10); got != "hi" {
		t.Errorf("truncate = %q, want %q", got, "hi")
	}
}

func TestCostMicrosConversion(t *testing.T) {
	// cost_micros 1_500_000 deve virar 1.5
	micros := parseInt64("1500000")
	spend := float64(micros) / 1_000_000.0
	if spend != 1.5 {
		t.Errorf("spend = %v, want 1.5", spend)
	}
}
