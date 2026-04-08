package obsidian

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
)

func TestSaveAndLoadSidecar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image.jpg.sidecar.md")
	want := Sidecar{
		ID:               "abc",
		Source:           "wechat",
		MediaType:        "image",
		CreatedAt:        "2026-04-06T18:00:00Z",
		RetentionDays:    7,
		ArchiveState:     ArchiveStateActive,
		Title:            "图片样例",
		OriginalFilename: "image.jpg",
		WechatUserID:     "user@im.wechat",
	}
	if err := SaveSidecar(path, want); err != nil {
		t.Fatalf("save sidecar: %v", err)
	}
	got, err := LoadSidecar(path)
	if err != nil {
		t.Fatalf("load sidecar: %v", err)
	}
	if got.Source != want.Source || got.MediaType != want.MediaType || got.ArchiveState != want.ArchiveState {
		t.Fatalf("unexpected sidecar: %#v", got)
	}
}

func TestSyncPendingAndCleanupWorkspace(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{
		ObsidianEnabled:   true,
		ObsidianVaultDir:  filepath.Join(tmp, "vault"),
		ObsidianVaultName: "LongclawVault",
		ObsidianNotesDir:  "Inbox/WeChat",
		ObsidianAssetsDir: "Assets/WeChat",
	}
	workspace := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := InitVault(cfg); err != nil {
		t.Fatalf("init vault: %v", err)
	}

	mediaPath := filepath.Join(workspace, "sample.jpg")
	if err := os.WriteFile(mediaPath, []byte("hello-image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveSidecar(mediaPath+".sidecar.md", Sidecar{
		ID:               "abc",
		Source:           "wechat",
		MediaType:        "image",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		RetentionDays:    7,
		ArchiveState:     ArchiveStatePendingObsidian,
		Title:            "Sample Image",
		OriginalFilename: "sample.jpg",
		WechatUserID:     "user@im.wechat",
	}); err != nil {
		t.Fatal(err)
	}

	syncResult, err := SyncPending(cfg, workspace)
	if err != nil {
		t.Fatalf("sync pending: %v", err)
	}
	if syncResult.Archived != 1 {
		t.Fatalf("expected one archived item, got %#v", syncResult)
	}

	cleanupResult, err := CleanupWorkspace(workspace, time.Now())
	if err != nil {
		t.Fatalf("cleanup workspace: %v", err)
	}
	if cleanupResult.DeletedArchived != 1 {
		t.Fatalf("expected archived cleanup, got %#v", cleanupResult)
	}
	if _, err := os.Stat(mediaPath); !os.IsNotExist(err) {
		t.Fatalf("expected media deleted, stat err=%v", err)
	}

	noteDir := filepath.Join(tmp, "vault", "Inbox", "WeChat")
	var notePath string
	_ = filepath.Walk(noteDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, ".md") {
			notePath = path
		}
		return nil
	})
	if notePath == "" {
		t.Fatal("expected one note")
	}
	noteData, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(noteData), "status: inbox") || !strings.Contains(string(noteData), "![[Assets/WeChat/") {
		t.Fatalf("expected obsidian embed, got %q", string(noteData))
	}
}

func TestMarkForObsidianBackfillsLegacySidecar(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{
		ObsidianEnabled:   true,
		ObsidianVaultDir:  filepath.Join(tmp, "vault"),
		ObsidianVaultName: "LongclawVault",
		ObsidianNotesDir:  "Inbox/WeChat",
		ObsidianAssetsDir: "Assets/WeChat",
	}
	mediaPath := filepath.Join(tmp, "legacy.jpg")
	if err := os.WriteFile(mediaPath, []byte("legacy-image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mediaPath+".sidecar.md", []byte("---\nid: legacy-1\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sidecar, err := MarkForObsidian(cfg, mediaPath, "Legacy Image", "compat path")
	if err != nil {
		t.Fatalf("mark for obsidian: %v", err)
	}
	if sidecar.Source != "wechat" || sidecar.MediaType != "image" || sidecar.ArchiveState != ArchiveStatePendingObsidian {
		t.Fatalf("unexpected normalized sidecar: %#v", sidecar)
	}
	loaded, err := LoadSidecar(mediaPath + ".sidecar.md")
	if err != nil {
		t.Fatalf("reload sidecar: %v", err)
	}
	if loaded.Source != "wechat" || loaded.MediaType != "image" || loaded.ArchiveState != ArchiveStatePendingObsidian {
		t.Fatalf("unexpected saved sidecar: %#v", loaded)
	}
}
