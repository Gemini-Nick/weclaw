package ilink

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionExpiredWarningIsRateLimited(t *testing.T) {
	var buf bytes.Buffer
	oldOutput := log.Writer()
	oldFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(oldOutput)
		log.SetFlags(oldFlags)
	}()

	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	m := &Monitor{}

	m.logSessionExpiredWarning(now)
	m.logSessionExpiredWarning(now.Add(time.Second))
	m.logSessionExpiredWarning(now.Add(2 * time.Second))

	out := buf.String()
	if got := strings.Count(out, "WeChat session expired"); got != 1 {
		t.Fatalf("expected one warning before interval, got %d: %s", got, out)
	}
	if m.sessionExpiredSuppressedLogs != 2 {
		t.Fatalf("expected 2 suppressed warnings, got %d", m.sessionExpiredSuppressedLogs)
	}

	m.logSessionExpiredWarning(now.Add(sessionExpiredWarningInterval + time.Second))
	out = buf.String()
	if got := strings.Count(out, "WeChat session expired"); got != 2 {
		t.Fatalf("expected second warning after interval, got %d: %s", got, out)
	}
	if !strings.Contains(out, "Suppressed 2 duplicate warnings") {
		t.Fatalf("expected suppressed warning count in log, got: %s", out)
	}
	if m.sessionExpiredSuppressedLogs != 0 {
		t.Fatalf("expected suppressed warning count to reset, got %d", m.sessionExpiredSuppressedLogs)
	}
}

func TestWriteSessionStatus(t *testing.T) {
	now := time.Date(2026, 4, 25, 12, 5, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "session_state", "bot.json")
	m := &Monitor{
		accountID:                    "bot",
		sessionStatePath:             path,
		sessionExpiredSuppressedLogs: 3,
	}

	m.writeSessionStatus("login_required", "re-login required", now, time.Minute)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session status: %v", err)
	}
	var got sessionStatus
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal session status: %v", err)
	}
	if got.AccountID != "bot" || got.Status != "login_required" {
		t.Fatalf("unexpected status: %#v", got)
	}
	if got.UpdatedAt != now.Format(time.RFC3339) {
		t.Fatalf("unexpected updated_at: %s", got.UpdatedAt)
	}
	if got.NextRetryAt != now.Add(time.Minute).Format(time.RFC3339) {
		t.Fatalf("unexpected next_retry_at: %s", got.NextRetryAt)
	}
	if got.SuppressedWarnings != 3 {
		t.Fatalf("unexpected suppressed_warnings: %d", got.SuppressedWarnings)
	}
}
