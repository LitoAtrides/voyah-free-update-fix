package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestPromptExpireTimeUsesDefaults(t *testing.T) {
	t.Parallel()

	got, err := promptExpireTime(strings.NewReader("\n"), &bytes.Buffer{}, time.Date(2025, 7, 30, 10, 44, 0, 0, time.Local))
	if err != nil {
		t.Fatalf("promptExpireTime returned error: %v", err)
	}

	want := time.Date(2025, 7, 30, 10, 59, 0, 0, time.Local).Unix()
	if got != want {
		t.Fatalf("unexpected expire_time: got %d, want %d", got, want)
	}
}

func TestParseExpireDateTime(t *testing.T) {
	t.Parallel()

	got, err := parseExpireDateTime("30.07.2025 10:44", time.Local)
	if err != nil {
		t.Fatalf("parseExpireDateTime returned error: %v", err)
	}

	want := time.Date(2025, 7, 30, 10, 44, 0, 0, time.Local).Unix()
	if got != want {
		t.Fatalf("unexpected expire_time: got %d, want %d", got, want)
	}
}

func TestAskConfirmation(t *testing.T) {
	t.Parallel()

	if !askConfirmation(strings.NewReader("start\n"), &bytes.Buffer{}) {
		t.Fatal("expected confirmation to be accepted")
	}

	if askConfirmation(strings.NewReader("no\n"), &bytes.Buffer{}) {
		t.Fatal("expected confirmation to be rejected")
	}
}
