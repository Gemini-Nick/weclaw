package messaging

import "testing"

func TestRememberAndLookupContextToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if got, ok := LookupContextToken("user@im.wechat"); ok || got != "" {
		t.Fatalf("expected empty lookup before save, got %q ok=%v", got, ok)
	}

	if err := RememberContextToken("user@im.wechat", "ctx-123"); err != nil {
		t.Fatalf("remember context token: %v", err)
	}

	got, ok := LookupContextToken("user@im.wechat")
	if !ok {
		t.Fatalf("expected persisted token to be found")
	}
	if got != "ctx-123" {
		t.Fatalf("expected token ctx-123, got %q", got)
	}
}
