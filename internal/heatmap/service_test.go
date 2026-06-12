package heatmap

import "testing"

func TestClamp01(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{-0.5, 0},
		{0, 0},
		{0.42, 0.42},
		{1, 1},
		{1.7, 1},
	}
	for _, c := range cases {
		if got := clamp01(c.in); got != c.want {
			t.Errorf("clamp01(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
