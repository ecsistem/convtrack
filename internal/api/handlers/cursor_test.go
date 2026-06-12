package handlers

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCursorRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Nanosecond)
	id := uuid.New()

	token := encodeCursor(now, id)
	if token == "" {
		t.Fatal("token vazio")
	}

	gotTime, gotID, err := decodeCursor(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if gotTime == nil || !gotTime.Equal(now) {
		t.Errorf("time = %v, want %v", gotTime, now)
	}
	if gotID != id {
		t.Errorf("id = %v, want %v", gotID, id)
	}
}

func TestDecodeCursorEmpty(t *testing.T) {
	tm, id, err := decodeCursor("")
	if err != nil {
		t.Fatalf("empty cursor não deveria dar erro: %v", err)
	}
	if tm != nil || id != uuid.Nil {
		t.Errorf("empty cursor deveria retornar (nil, Nil), got (%v, %v)", tm, id)
	}
}

func TestDecodeCursorInvalid(t *testing.T) {
	for _, bad := range []string{"!!!notbase64!!!", "Zm9vYmFy", "MjAyNHwx"} {
		if _, _, err := decodeCursor(bad); err == nil {
			t.Errorf("esperava erro para cursor inválido %q", bad)
		}
	}
}
