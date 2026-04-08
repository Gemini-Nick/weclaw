package obsidian

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
)

func TestApplyAgentArchiveTool_FormalArchive(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{
		ObsidianEnabled:              true,
		ArchiveToolEnabled:           true,
		ObsidianFormalWriteEnabled:   true,
		ObsidianVaultDir:             filepath.Join(tmp, "vault"),
		ObsidianVaultName:            "Obsidian Vault",
		ObsidianNotesDir:             "Inbox/WeChat",
		ObsidianAssetsDir:            "Assets/WeChat",
		ObsidianAutoArchiveEnabled:   true,
		ObsidianArchiveWindowMinutes: 30,
	}
	workspace := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	mediaPath := filepath.Join(workspace, "sample.jpg")
	if err := os.WriteFile(mediaPath, []byte("image"), 0o644); err != nil {
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
	if err := RecordUserMessage(workspace, "user@im.wechat", 101, "把这个归档到知识库"); err != nil {
		t.Fatal(err)
	}
	if err := RecordMedia(workspace, "user@im.wechat", 102, sidecar, mediaPath); err != nil {
		t.Fatal(err)
	}
	rawReply := strings.TrimSpace(fmt.Sprintf(
		"先整理成正式知识库条目。\n\n```weclaw-obsidian-tool\n"+
			"{\"tool\":\"obsidian_archive\",\"mode\":\"formal\",\"conversation_id\":\"user@im.wechat\",\"title\":\"未来知识库架构\",\"summary\":\"这张图片展示了未来知识库的结构草图。\",\"message_ids\":[101,102],\"selected_media_paths\":[%q],\"note_body\":\"## 结论\\n\\n这张图是未来知识库架构的核心草图，适合纳入正式知识库。\"}\n"+
			"```",
		mediaPath,
	))

	result, err := ApplyAgentArchiveTool(cfg, workspace, "user@im.wechat", rawReply)
	if err != nil {
		t.Fatalf("apply archive tool: %v", err)
	}
	if !result.Invoked || result.NotePath == "" {
		t.Fatalf("expected archive invocation result, got %#v", result)
	}
	if !strings.Contains(result.Reply, "已归档到 Obsidian") || !strings.Contains(result.Reply, result.NotePath) {
		t.Fatalf("unexpected reply: %q", result.Reply)
	}

	notePath := filepath.Join(cfg.ObsidianVaultDir, filepath.FromSlash(result.NotePath))
	data, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "status: inbox") || !strings.Contains(content, "trigger_reason: \"agent_tool\"") || !strings.Contains(content, "source_message_ids: \"101,102\"") {
		t.Fatalf("expected audit frontmatter, got %s", content)
	}
	if !strings.Contains(content, "## 结论") || !strings.Contains(content, "![[Assets/WeChat/") {
		t.Fatalf("expected note body and attachment embed, got %s", content)
	}
}

func TestApplyAgentArchiveTool_TranscriptOnlyVoiceSkipsAudioAsset(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{
		ObsidianEnabled:              true,
		ArchiveToolEnabled:           true,
		ObsidianFormalWriteEnabled:   true,
		ObsidianVaultDir:             filepath.Join(tmp, "vault"),
		ObsidianVaultName:            "Obsidian Vault",
		ObsidianNotesDir:             "Inbox/WeChat",
		ObsidianAssetsDir:            "Assets/WeChat",
		ObsidianAutoArchiveEnabled:   true,
		ObsidianArchiveWindowMinutes: 30,
		ObsidianVoiceArchiveMode:     "transcript_only",
	}
	workspace := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	mediaPath := filepath.Join(workspace, "sample.audio")
	if err := os.WriteFile(mediaPath, []byte("voice"), 0o644); err != nil {
		t.Fatal(err)
	}
	sidecar := Sidecar{
		Source:           "wechat",
		MediaType:        "voice",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		ArchiveState:     ArchiveStateActive,
		OriginalFilename: "sample.audio",
		WechatUserID:     "user@im.wechat",
		Remark:           "把这条命令记一下",
	}
	if err := SaveSidecar(mediaPath+".sidecar.md", sidecar); err != nil {
		t.Fatal(err)
	}
	if err := RecordUserMessage(workspace, "user@im.wechat", 101, "归档这条命令"); err != nil {
		t.Fatal(err)
	}
	if err := RecordMedia(workspace, "user@im.wechat", 102, sidecar, mediaPath); err != nil {
		t.Fatal(err)
	}
	rawReply := strings.TrimSpace(fmt.Sprintf(
		"我来整理。\n\n```weclaw-obsidian-tool\n"+
			"{\"tool\":\"obsidian_archive\",\"mode\":\"formal\",\"conversation_id\":\"user@im.wechat\",\"title\":\"语音命令归档\",\"summary\":\"保留命令转写。\",\"message_ids\":[101,102],\"selected_media_paths\":[%q],\"note_body\":\"## 结论\\n\\n只保留这条命令的转写，不归档原始音频。\"}\n"+
			"```",
		mediaPath,
	))

	result, err := ApplyAgentArchiveTool(cfg, workspace, "user@im.wechat", rawReply)
	if err != nil {
		t.Fatalf("apply archive tool: %v", err)
	}
	notePath := filepath.Join(cfg.ObsidianVaultDir, filepath.FromSlash(result.NotePath))
	data, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	content := string(data)
	if strings.Contains(content, "Assets/WeChat/") {
		t.Fatalf("expected no archived audio asset, got %s", content)
	}
}

func TestBuildAgentArchivePrompt_ContainsToolContract(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{
		ObsidianEnabled:              true,
		ArchiveToolEnabled:           true,
		ObsidianAutoArchiveEnabled:   true,
		ObsidianArchiveWindowMinutes: 30,
	}
	if err := RecordUserMessage(tmp, "user@im.wechat", 1, "这张图要沉淀"); err != nil {
		t.Fatal(err)
	}

	prompt := BuildAgentArchivePrompt(cfg, tmp, "user@im.wechat", "帮我归档到 Obsidian")
	if !strings.Contains(prompt, "weclaw-obsidian-tool") {
		t.Fatalf("expected tool fence in prompt, got %q", prompt)
	}
	if !strings.Contains(prompt, "conversation_id: user@im.wechat") {
		t.Fatalf("expected conversation context, got %q", prompt)
	}
}

func TestApplyAgentArchiveTool_RespectsFormalWritePolicy(t *testing.T) {
	tmp := t.TempDir()
	cfg := &config.Config{
		ObsidianEnabled:            true,
		ArchiveToolEnabled:         true,
		ObsidianFormalWriteEnabled: false,
	}
	rawReply := "```weclaw-obsidian-tool\n{\"tool\":\"obsidian_archive\",\"mode\":\"formal\",\"conversation_id\":\"user@im.wechat\"}\n```"
	_, err := ApplyAgentArchiveTool(cfg, tmp, "user@im.wechat", rawReply)
	if err == nil || !strings.Contains(err.Error(), "formal obsidian writes are disabled") {
		t.Fatalf("expected formal-write error, got %v", err)
	}
}
