package ilink

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadAllCredentialsKeepsLatestPerWeChatUser(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".weclaw", "accounts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create accounts dir: %v", err)
	}

	writeCred := func(creds Credentials, modTime time.Time) {
		t.Helper()
		data, err := json.Marshal(creds)
		if err != nil {
			t.Fatalf("marshal creds: %v", err)
		}
		path := filepath.Join(dir, NormalizeAccountID(creds.ILinkBotID)+".json")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write creds: %v", err)
		}
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatalf("set mtime: %v", err)
		}
	}

	base := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	writeCred(Credentials{BotToken: "old", ILinkBotID: "old@im.bot", ILinkUserID: "same@im.wechat"}, base)
	writeCred(Credentials{BotToken: "new", ILinkBotID: "new@im.bot", ILinkUserID: "same@im.wechat"}, base.Add(time.Hour))
	writeCred(Credentials{BotToken: "other", ILinkBotID: "other@im.bot", ILinkUserID: "other@im.wechat"}, base.Add(2*time.Hour))
	if err := os.WriteFile(filepath.Join(dir, "new-im-bot.sync.json"), []byte(`{"get_updates_buf":"x"}`), 0o600); err != nil {
		t.Fatalf("write sync file: %v", err)
	}

	got, err := LoadAllCredentials()
	if err != nil {
		t.Fatalf("load credentials: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 credentials, got %d", len(got))
	}

	byUser := map[string]string{}
	for _, creds := range got {
		byUser[creds.ILinkUserID] = creds.ILinkBotID
	}
	if byUser["same@im.wechat"] != "new@im.bot" {
		t.Fatalf("expected latest credential for same user, got %q", byUser["same@im.wechat"])
	}
	if byUser["other@im.wechat"] != "other@im.bot" {
		t.Fatalf("expected distinct user to remain loaded, got %q", byUser["other@im.wechat"])
	}
}
