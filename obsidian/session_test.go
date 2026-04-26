package obsidian

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
)

func TestQueueAndSyncSessionArchive(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{
		ObsidianEnabled:              true,
		ObsidianVaultDir:             filepath.Join(tmp, "vault"),
		ObsidianVaultName:            "LongclawVault",
		ObsidianNotesDir:             "Inbox/WeChat",
		ObsidianAssetsDir:            "Assets/WeChat",
		ObsidianAutoArchiveEnabled:   true,
		ObsidianAutoArchiveMode:      "hybrid",
		ObsidianArchiveWindowMinutes: 30,
	}
	workspace := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := InitVault(cfg); err != nil {
		t.Fatal(err)
	}

	mediaPath := filepath.Join(workspace, "sample.jpg")
	if err := os.WriteFile(mediaPath, []byte("image-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	sidecar := Sidecar{
		Source:           "wechat",
		MediaType:        "image",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		ArchiveState:     ArchiveStateActive,
		OriginalFilename: "sample.jpg",
		WechatUserID:     "user@im.wechat",
	}
	if err := SaveSidecar(mediaPath+".sidecar.md", sidecar); err != nil {
		t.Fatal(err)
	}
	if err := RecordMedia(workspace, "user@im.wechat", 10, sidecar, mediaPath); err != nil {
		t.Fatal(err)
	}
	if err := RecordUserMessage(workspace, "user@im.wechat", 11, "这是未来知识库的架构讨论"); err != nil {
		t.Fatal(err)
	}
	if err := RecordAgentReply(workspace, "user@im.wechat", 12, "codex", "建议整理成一篇 Obsidian note。"); err != nil {
		t.Fatal(err)
	}

	task, state, err := QueueSessionArchive(cfg, workspace, "user@im.wechat", "归档到知识库，这是未来知识库的架构", TriggerReasonExplicit)
	if err != nil {
		t.Fatalf("queue archive: %v", err)
	}
	if state != "queued" {
		t.Fatalf("unexpected queue state: %s", state)
	}
	if task.ArchiveState != ArchiveStatePendingObsidian || task.ArchiveScope != ArchiveScopeSession {
		t.Fatalf("unexpected task: %#v", task)
	}

	result, err := SyncPendingSessions(cfg, workspace)
	if err != nil {
		t.Fatalf("sync sessions: %v", err)
	}
	if result.SessionsArchived != 1 {
		t.Fatalf("unexpected session sync result: %#v", result)
	}

	noteRoot := filepath.Join(cfg.ObsidianVaultDir, "Inbox", "WeChat")
	var notePath string
	_ = filepath.Walk(noteRoot, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, ".md") {
			notePath = path
		}
		return nil
	})
	if notePath == "" {
		t.Fatal("expected archived note to exist")
	}
	data, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "status: inbox") || !strings.Contains(string(data), "## 附件") || !strings.Contains(string(data), "![[Assets/WeChat/") {
		t.Fatalf("unexpected note content: %s", string(data))
	}
}

func TestRecordSessionEntriesDeduplicateRepeatedWrites(t *testing.T) {
	workspace := t.TempDir()
	mediaPath := filepath.Join(workspace, "sample.jpg")
	if err := os.WriteFile(mediaPath, []byte("image-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	sidecar := Sidecar{
		Source:           "wechat",
		MediaType:        "image",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		ArchiveState:     ArchiveStateActive,
		OriginalFilename: "sample.jpg",
		WechatUserID:     "user@im.wechat",
	}

	if err := RecordUserMessage(workspace, "user@im.wechat", 21, "重复消息"); err != nil {
		t.Fatal(err)
	}
	if err := RecordUserMessage(workspace, "user@im.wechat", 21, "重复消息"); err != nil {
		t.Fatal(err)
	}
	if err := RecordAgentReply(workspace, "user@im.wechat", 21, "codex", "重复回复"); err != nil {
		t.Fatal(err)
	}
	if err := RecordAgentReply(workspace, "user@im.wechat", 21, "codex", "重复回复"); err != nil {
		t.Fatal(err)
	}
	if err := RecordMedia(workspace, "user@im.wechat", 21, sidecar, mediaPath); err != nil {
		t.Fatal(err)
	}
	if err := RecordMedia(workspace, "user@im.wechat", 21, sidecar, mediaPath); err != nil {
		t.Fatal(err)
	}

	window, _, err := loadSessionWindow(workspace, "user@im.wechat")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(window.Messages); got != 2 {
		t.Fatalf("expected 2 unique session messages, got %d (%#v)", got, window.Messages)
	}
	if got := len(window.Media); got != 1 {
		t.Fatalf("expected 1 unique media record, got %d (%#v)", got, window.Media)
	}
}

func TestUpdateSessionWindowMetadata(t *testing.T) {
	workspace := t.TempDir()
	if err := UpdateSessionWindowMetadata(
		workspace,
		"user@im.wechat",
		"session:wechat:user@im.wechat",
		"wechat:user@im.wechat",
		"ctx-123",
	); err != nil {
		t.Fatal(err)
	}
	if err := RecordUserMessage(workspace, "user@im.wechat", 30, "测试消息"); err != nil {
		t.Fatal(err)
	}

	window, _, err := loadSessionWindow(workspace, "user@im.wechat")
	if err != nil {
		t.Fatal(err)
	}
	if window.CanonicalSessionID != "session:wechat:user@im.wechat" {
		t.Fatalf("unexpected canonical session id: %#v", window.CanonicalSessionID)
	}
	if window.CanonicalUserID != "wechat:user@im.wechat" {
		t.Fatalf("unexpected canonical user id: %#v", window.CanonicalUserID)
	}
	if window.ContextToken != "ctx-123" {
		t.Fatalf("unexpected context token: %#v", window.ContextToken)
	}
}

func TestQueueAndSyncSessionArchive_TranscriptOnlyVoiceSkipsAudioAsset(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{
		ObsidianEnabled:              true,
		ObsidianVaultDir:             filepath.Join(tmp, "vault"),
		ObsidianVaultName:            "LongclawVault",
		ObsidianNotesDir:             "Inbox/WeChat",
		ObsidianAssetsDir:            "Assets/WeChat",
		ObsidianAutoArchiveEnabled:   true,
		ObsidianAutoArchiveMode:      "hybrid",
		ObsidianArchiveWindowMinutes: 30,
		ObsidianVoiceArchiveMode:     "transcript_only",
	}
	workspace := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := InitVault(cfg); err != nil {
		t.Fatal(err)
	}

	voicePath := filepath.Join(workspace, "sample.audio")
	if err := os.WriteFile(voicePath, []byte("voice-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	sidecar := Sidecar{
		Source:           "wechat",
		MediaType:        "voice",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		ArchiveState:     ArchiveStateActive,
		OriginalFilename: "sample.audio",
		WechatUserID:     "user@im.wechat",
		Remark:           "帮我记录一个命令",
	}
	if err := SaveSidecar(voicePath+".sidecar.md", sidecar); err != nil {
		t.Fatal(err)
	}
	if err := RecordMedia(workspace, "user@im.wechat", 10, sidecar, voicePath); err != nil {
		t.Fatal(err)
	}
	if err := RecordUserMessage(workspace, "user@im.wechat", 11, "归档到知识库"); err != nil {
		t.Fatal(err)
	}

	if _, _, err := QueueSessionArchive(cfg, workspace, "user@im.wechat", "归档到知识库", TriggerReasonExplicit); err != nil {
		t.Fatalf("queue archive: %v", err)
	}
	if _, err := SyncPendingSessions(cfg, workspace); err != nil {
		t.Fatalf("sync sessions: %v", err)
	}

	noteRoot := filepath.Join(cfg.ObsidianVaultDir, "Inbox", "WeChat")
	var notePath string
	_ = filepath.Walk(noteRoot, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, ".md") {
			notePath = path
		}
		return nil
	})
	if notePath == "" {
		t.Fatal("expected archived note to exist")
	}
	data, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if strings.Contains(content, "Assets/WeChat/") {
		t.Fatalf("expected no audio asset archive, got %s", content)
	}
	if !strings.Contains(content, "仅保留转写") {
		t.Fatalf("expected transcript-only marker, got %s", content)
	}
}
