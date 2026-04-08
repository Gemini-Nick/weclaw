package messaging

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/ilink"
	obsidianarchive "github.com/fastclaw-ai/weclaw/obsidian"
)

func newTestHandler() *Handler {
	return &Handler{agents: make(map[string]agent.Agent)}
}

type fakeAgent struct {
	mu       sync.Mutex
	messages []string
	reply    string
}

func (f *fakeAgent) Chat(_ context.Context, _ string, message string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, message)
	return f.reply, nil
}

func (f *fakeAgent) ResetSession(context.Context, string) (string, error) { return "", nil }
func (f *fakeAgent) Info() agent.AgentInfo {
	return agent.AgentInfo{Name: "fake", Type: "test", Command: "fake"}
}
func (f *fakeAgent) SetCwd(string) {}

func (f *fakeAgent) lastMessage() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.messages) == 0 {
		return ""
	}
	return f.messages[len(f.messages)-1]
}

func newTestILinkServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	if durableSeenMessagesRootOverride == "" {
		durableSeenMessagesRootOverride = t.TempDir()
		t.Cleanup(func() { durableSeenMessagesRootOverride = "" })
	}

	var sent []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/getconfig":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ret":           0,
				"typing_ticket": "ticket",
			})
		case "/ilink/bot/sendtyping":
			_ = json.NewEncoder(w).Encode(map[string]any{"ret": 0})
		case "/ilink/bot/sendmessage":
			var req ilink.SendMessageRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode sendmessage: %v", err)
			}
			text := ""
			if len(req.Msg.ItemList) > 0 && req.Msg.ItemList[0].TextItem != nil {
				text = req.Msg.ItemList[0].TextItem.Text
			}
			mu.Lock()
			sent = append(sent, text)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"ret": 0})
		default:
			http.NotFound(w, r)
		}
	}))

	return srv, &sent
}

func TestParseCommand_NoPrefix(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("hello world")
	if len(names) != 0 {
		t.Errorf("expected nil names, got %v", names)
	}
	if msg != "hello world" {
		t.Errorf("expected full text, got %q", msg)
	}
}

func TestParseCommand_SlashWithAgent(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("/claude explain this code")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude], got %v", names)
	}
	if msg != "explain this code" {
		t.Errorf("expected 'explain this code', got %q", msg)
	}
}

func TestParseCommand_AtPrefix(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("@claude explain this code")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude], got %v", names)
	}
	if msg != "explain this code" {
		t.Errorf("expected 'explain this code', got %q", msg)
	}
}

func TestParseCommand_MultiAgent(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("@cc @cx hello")
	if len(names) != 2 || names[0] != "claude" || names[1] != "codex" {
		t.Errorf("expected [claude codex], got %v", names)
	}
	if msg != "hello" {
		t.Errorf("expected 'hello', got %q", msg)
	}
}

func TestParseCommand_MultiAgentDedup(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("@cc @cc hello")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude] (deduped), got %v", names)
	}
	if msg != "hello" {
		t.Errorf("expected 'hello', got %q", msg)
	}
}

func TestParseCommand_SwitchOnly(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("/claude")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude], got %v", names)
	}
	if msg != "" {
		t.Errorf("expected empty message, got %q", msg)
	}
}

func TestParseCommand_Alias(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("/cc write a function")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude] from /cc alias, got %v", names)
	}
	if msg != "write a function" {
		t.Errorf("expected 'write a function', got %q", msg)
	}
}

func TestParseCommand_CustomAlias(t *testing.T) {
	h := newTestHandler()
	h.customAliases = map[string]string{"ai": "claude", "c": "claude"}
	names, msg := h.parseCommand("/ai hello")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude] from custom alias, got %v", names)
	}
	if msg != "hello" {
		t.Errorf("expected 'hello', got %q", msg)
	}
}

func TestResolveAlias(t *testing.T) {
	h := newTestHandler()
	tests := map[string]string{
		"cc":  "claude",
		"cx":  "codex",
		"oc":  "openclaw",
		"cs":  "cursor",
		"km":  "kimi",
		"gm":  "gemini",
		"ocd": "opencode",
	}
	for alias, want := range tests {
		got := h.resolveAlias(alias)
		if got != want {
			t.Errorf("resolveAlias(%q) = %q, want %q", alias, got, want)
		}
	}
	if got := h.resolveAlias("unknown"); got != "unknown" {
		t.Errorf("resolveAlias(unknown) = %q, want %q", got, "unknown")
	}
	h.customAliases = map[string]string{"cc": "custom-claude"}
	if got := h.resolveAlias("cc"); got != "custom-claude" {
		t.Errorf("resolveAlias(cc) with custom = %q, want custom-claude", got)
	}
}

func TestBuildHelpText(t *testing.T) {
	text := buildHelpText()
	if text == "" {
		t.Error("help text is empty")
	}
	if !strings.Contains(text, "/info") {
		t.Error("help text should mention /info")
	}
	if !strings.Contains(text, "/help") {
		t.Error("help text should mention /help")
	}
}

func TestHandleMessage_ImageWithoutMatchingTypeSavesAndDispatches(t *testing.T) {
	srv, sent := newTestILinkServer(t)
	defer srv.Close()

	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x01})
	}))
	defer imgSrv.Close()

	tmpDir := t.TempDir()
	ag := &fakeAgent{reply: "processed image"}
	h := newTestHandler()
	h.SetSaveDir(tmpDir)
	h.SetDefaultAgent("codex", ag)

	client := ilink.NewClient(&ilink.Credentials{
		BaseURL:    srv.URL,
		BotToken:   "token",
		ILinkBotID: "bot@im.bot",
	})

	msg := ilink.WeixinMessage{
		MessageID:    1,
		FromUserID:   "user@im.wechat",
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ContextToken: "ctx",
		ItemList: []ilink.MessageItem{
			{Type: ilink.ItemTypeNone, ImageItem: &ilink.ImageItem{URL: imgSrv.URL}},
		},
	}

	h.HandleMessage(context.Background(), client, msg)

	files, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("read temp dir: %v", err)
	}
	var saved string
	for _, entry := range files {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".png") {
			saved = filepath.Join(tmpDir, entry.Name())
		}
	}
	if saved == "" {
		t.Fatalf("expected saved png in %s", tmpDir)
	}
	if _, err := os.Stat(saved + ".sidecar.md"); err != nil {
		t.Fatalf("expected sidecar for %s: %v", saved, err)
	}
	sidecarData, err := os.ReadFile(saved + ".sidecar.md")
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if !strings.Contains(string(sidecarData), "source: \"wechat\"") || !strings.Contains(string(sidecarData), "archive_state: \"active\"") {
		t.Fatalf("expected obsidian sidecar metadata, got %q", string(sidecarData))
	}
	if !strings.Contains(ag.lastMessage(), saved) {
		t.Fatalf("expected agent prompt to reference saved file, got %q", ag.lastMessage())
	}
	if !strings.Contains(ag.lastMessage(), "图片") {
		t.Fatalf("expected agent prompt to mention image, got %q", ag.lastMessage())
	}
	if got := len(*sent); got != 2 {
		t.Fatalf("expected 2 outgoing text messages, got %d (%v)", got, *sent)
	}
	if !strings.Contains((*sent)[0], "已收到图片") {
		t.Fatalf("expected save ack, got %q", (*sent)[0])
	}
	if !strings.Contains((*sent)[0], filepath.Base(saved)) {
		t.Fatalf("expected saved image filename in ack, got %q", (*sent)[0])
	}
	if !strings.Contains((*sent)[1], "processed image") {
		t.Fatalf("expected agent reply, got %q", (*sent)[1])
	}
}

func TestHandleMessage_DeduplicatesRepeatedMessage(t *testing.T) {
	durableSeenMessagesRootOverride = t.TempDir()
	t.Cleanup(func() { durableSeenMessagesRootOverride = "" })

	srv, sent := newTestILinkServer(t)
	defer srv.Close()

	ag := &fakeAgent{reply: "processed once"}
	h := newTestHandler()
	h.SetDefaultAgent("codex", ag)

	client := ilink.NewClient(&ilink.Credentials{
		BaseURL:    srv.URL,
		BotToken:   "token",
		ILinkBotID: "bot@im.bot",
	})

	msg := ilink.WeixinMessage{
		MessageID:    99,
		FromUserID:   "user@im.wechat",
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ContextToken: "ctx",
		ItemList: []ilink.MessageItem{
			{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "hello"}},
		},
	}

	h.HandleMessage(context.Background(), client, msg)
	h.HandleMessage(context.Background(), client, msg)

	if got := len(*sent); got != 1 {
		t.Fatalf("expected 1 outgoing text reply, got %d (%v)", got, *sent)
	}
	if got := len(ag.messages); got != 1 {
		t.Fatalf("expected agent to receive 1 message, got %d (%v)", got, ag.messages)
	}
}

func TestHandleMessage_DedupIsScopedPerUser(t *testing.T) {
	durableSeenMessagesRootOverride = t.TempDir()
	t.Cleanup(func() { durableSeenMessagesRootOverride = "" })

	srv, sent := newTestILinkServer(t)
	defer srv.Close()

	ag := &fakeAgent{reply: "processed"}
	h := newTestHandler()
	h.SetDefaultAgent("codex", ag)

	client := ilink.NewClient(&ilink.Credentials{
		BaseURL:    srv.URL,
		BotToken:   "token",
		ILinkBotID: "bot@im.bot",
	})

	msgA := ilink.WeixinMessage{
		MessageID:    100,
		FromUserID:   "user-a@im.wechat",
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ContextToken: "ctx-a",
		ItemList: []ilink.MessageItem{
			{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "hello from a"}},
		},
	}
	msgB := ilink.WeixinMessage{
		MessageID:    100,
		FromUserID:   "user-b@im.wechat",
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ContextToken: "ctx-b",
		ItemList: []ilink.MessageItem{
			{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "hello from b"}},
		},
	}

	h.HandleMessage(context.Background(), client, msgA)
	h.HandleMessage(context.Background(), client, msgB)

	if got := len(*sent); got != 2 {
		t.Fatalf("expected 2 outgoing text replies, got %d (%v)", got, *sent)
	}
	if got := len(ag.messages); got != 2 {
		t.Fatalf("expected agent to receive 2 messages, got %d (%v)", got, ag.messages)
	}
}

func TestHandleMessage_FileSavesAndDispatches(t *testing.T) {
	srv, sent := newTestILinkServer(t)
	defer srv.Close()

	oldCDNBaseURL := cdnBaseURL
	defer func() { cdnBaseURL = oldCDNBaseURL }()

	keyHex := "00112233445566778899aabbccddeeff"
	keyBytes, err := hex.DecodeString(keyHex)
	if err != nil {
		t.Fatalf("decode hex key: %v", err)
	}
	encrypted, err := encryptAESECB([]byte("hello-report"), keyBytes)
	if err != nil {
		t.Fatalf("encrypt test payload: %v", err)
	}

	cdnSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(encrypted)
	}))
	defer cdnSrv.Close()
	cdnBaseURL = cdnSrv.URL

	tmpDir := t.TempDir()
	ag := &fakeAgent{reply: "processed file"}
	h := newTestHandler()
	h.SetSaveDir(tmpDir)
	h.SetDefaultAgent("codex", ag)

	client := ilink.NewClient(&ilink.Credentials{
		BaseURL:    srv.URL,
		BotToken:   "token",
		ILinkBotID: "bot@im.bot",
	})

	msg := ilink.WeixinMessage{
		MessageID:    2,
		FromUserID:   "user@im.wechat",
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ContextToken: "ctx",
		ItemList: []ilink.MessageItem{
			{
				Type: ilink.ItemTypeNone,
				FileItem: &ilink.FileItem{
					FileName: "report.pdf",
					Media: &ilink.MediaInfo{
						EncryptQueryParam: "token",
						AESKey:            AESKeyToBase64(keyHex),
					},
				},
			},
		},
	}

	h.HandleMessage(context.Background(), client, msg)

	saved := filepath.Join(tmpDir, "report.pdf")
	if _, err := os.Stat(saved); err != nil {
		t.Fatalf("expected saved file %s: %v", saved, err)
	}
	if _, err := os.Stat(saved + ".sidecar.md"); err != nil {
		t.Fatalf("expected sidecar for %s: %v", saved, err)
	}
	sidecarData, err := os.ReadFile(saved + ".sidecar.md")
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if !strings.Contains(string(sidecarData), "media_type: \"file\"") || !strings.Contains(string(sidecarData), "archive_state: \"active\"") {
		t.Fatalf("expected obsidian sidecar metadata, got %q", string(sidecarData))
	}
	if !strings.Contains(ag.lastMessage(), saved) {
		t.Fatalf("expected agent prompt to reference saved file, got %q", ag.lastMessage())
	}
	if !strings.Contains(ag.lastMessage(), "文件") {
		t.Fatalf("expected agent prompt to mention file, got %q", ag.lastMessage())
	}
	if got := len(*sent); got != 2 {
		t.Fatalf("expected 2 outgoing text messages, got %d (%v)", got, *sent)
	}
	if !strings.Contains((*sent)[0], "已收到文件") || !strings.Contains((*sent)[0], "report.pdf") {
		t.Fatalf("expected file save ack, got %q", (*sent)[0])
	}
	if !strings.Contains((*sent)[1], "processed file") {
		t.Fatalf("expected agent reply, got %q", (*sent)[1])
	}
}

func TestHandleMessage_FileDownloadFailureDoesNotDispatch(t *testing.T) {
	srv, sent := newTestILinkServer(t)
	defer srv.Close()

	oldCDNBaseURL := cdnBaseURL
	defer func() { cdnBaseURL = oldCDNBaseURL }()
	cdnSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer cdnSrv.Close()
	cdnBaseURL = cdnSrv.URL

	tmpDir := t.TempDir()
	ag := &fakeAgent{reply: "processed file"}
	h := newTestHandler()
	h.SetSaveDir(tmpDir)
	h.SetDefaultAgent("codex", ag)

	client := ilink.NewClient(&ilink.Credentials{
		BaseURL:    srv.URL,
		BotToken:   "token",
		ILinkBotID: "bot@im.bot",
	})

	msg := ilink.WeixinMessage{
		MessageID:    3,
		FromUserID:   "user@im.wechat",
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ContextToken: "ctx",
		ItemList: []ilink.MessageItem{
			{
				Type: ilink.ItemTypeFile,
				FileItem: &ilink.FileItem{
					FileName: "report.pdf",
					Media: &ilink.MediaInfo{
						EncryptQueryParam: "token",
						AESKey:            AESKeyToBase64("00112233445566778899aabbccddeeff"),
					},
				},
			},
		},
	}

	h.HandleMessage(context.Background(), client, msg)

	if got := ag.lastMessage(); got != "" {
		t.Fatalf("expected no agent dispatch, got %q", got)
	}
	if got := len(*sent); got != 1 {
		t.Fatalf("expected only failure ack, got %d (%v)", got, *sent)
	}
	if !strings.Contains((*sent)[0], "Failed to save file") {
		t.Fatalf("expected failure ack, got %q", (*sent)[0])
	}
}

func TestHandleMessage_VoiceWithTranscriptUsesTranscriptFirstPrompt(t *testing.T) {
	srv, sent := newTestILinkServer(t)
	defer srv.Close()

	oldCDNBaseURL := cdnBaseURL
	defer func() { cdnBaseURL = oldCDNBaseURL }()

	keyHex := "00112233445566778899aabbccddeeff"
	keyBytes, err := hex.DecodeString(keyHex)
	if err != nil {
		t.Fatalf("decode hex key: %v", err)
	}
	encrypted, err := encryptAESECB([]byte("voice-bytes"), keyBytes)
	if err != nil {
		t.Fatalf("encrypt test payload: %v", err)
	}

	cdnSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(encrypted)
	}))
	defer cdnSrv.Close()
	cdnBaseURL = cdnSrv.URL

	tmpDir := t.TempDir()
	ag := &fakeAgent{reply: "processed voice"}
	h := newTestHandler()
	h.SetSaveDir(tmpDir)
	h.SetConfig(&config.Config{VoiceInputModeDefault: "transcript_first"})
	h.SetDefaultAgent("codex", ag)

	client := ilink.NewClient(&ilink.Credentials{
		BaseURL:    srv.URL,
		BotToken:   "token",
		ILinkBotID: "bot@im.bot",
	})

	msg := ilink.WeixinMessage{
		MessageID:    4,
		FromUserID:   "user@im.wechat",
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ContextToken: "ctx",
		ItemList: []ilink.MessageItem{
			{
				Type: ilink.ItemTypeVoice,
				VoiceItem: &ilink.VoiceItem{
					Text:       "帮我总结这个语音内容",
					EncodeType: 6,
					Media: &ilink.MediaInfo{
						EncryptQueryParam: "token",
						AESKey:            AESKeyToBase64(keyHex),
					},
				},
			},
		},
	}

	h.HandleMessage(context.Background(), client, msg)

	files, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("read temp dir: %v", err)
	}
	var saved string
	for _, entry := range files {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".silk") {
			saved = filepath.Join(tmpDir, entry.Name())
		}
	}
	if saved == "" {
		t.Fatalf("expected saved voice in %s", tmpDir)
	}
	if got := ag.lastMessage(); !strings.Contains(got, "以下内容来自微信语音转写") || !strings.Contains(got, "帮我总结这个语音内容") {
		t.Fatalf("expected transcript-first prompt, got %q", got)
	}
	if strings.Contains(ag.lastMessage(), saved) {
		t.Fatalf("expected transcript-first prompt to hide audio path, got %q", ag.lastMessage())
	}
	if got := len(*sent); got != 2 {
		t.Fatalf("expected 2 outgoing text messages, got %d (%v)", got, *sent)
	}
	if !strings.Contains((*sent)[0], "已收到语音") {
		t.Fatalf("expected voice save ack, got %q", (*sent)[0])
	}
	if !strings.Contains((*sent)[1], "processed voice") {
		t.Fatalf("expected agent reply, got %q", (*sent)[1])
	}
}

func TestHandleMessage_VoiceAudioAnalysisModeIncludesAudioPath(t *testing.T) {
	srv, sent := newTestILinkServer(t)
	defer srv.Close()

	oldCDNBaseURL := cdnBaseURL
	defer func() { cdnBaseURL = oldCDNBaseURL }()

	keyHex := "00112233445566778899aabbccddeeff"
	keyBytes, err := hex.DecodeString(keyHex)
	if err != nil {
		t.Fatalf("decode hex key: %v", err)
	}
	encrypted, err := encryptAESECB([]byte("voice-bytes"), keyBytes)
	if err != nil {
		t.Fatalf("encrypt test payload: %v", err)
	}

	cdnSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(encrypted)
	}))
	defer cdnSrv.Close()
	cdnBaseURL = cdnSrv.URL

	tmpDir := t.TempDir()
	ag := &fakeAgent{reply: "processed voice deeply"}
	h := newTestHandler()
	h.SetSaveDir(tmpDir)
	h.SetConfig(&config.Config{VoiceInputModeDefault: "audio_analysis_requested"})
	h.SetDefaultAgent("codex", ag)

	client := ilink.NewClient(&ilink.Credentials{
		BaseURL:    srv.URL,
		BotToken:   "token",
		ILinkBotID: "bot@im.bot",
	})

	msg := ilink.WeixinMessage{
		MessageID:    5,
		FromUserID:   "user@im.wechat",
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ContextToken: "ctx",
		ItemList: []ilink.MessageItem{
			{
				Type: ilink.ItemTypeVoice,
				VoiceItem: &ilink.VoiceItem{
					Text:       "请结合原音分析",
					EncodeType: 6,
					Media: &ilink.MediaInfo{
						EncryptQueryParam: "token",
						AESKey:            AESKeyToBase64(keyHex),
					},
				},
			},
		},
	}

	h.HandleMessage(context.Background(), client, msg)

	files, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("read temp dir: %v", err)
	}
	var saved string
	for _, entry := range files {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".silk") {
			saved = filepath.Join(tmpDir, entry.Name())
		}
	}
	if saved == "" {
		t.Fatalf("expected saved voice in %s", tmpDir)
	}
	if got := ag.lastMessage(); !strings.Contains(got, saved) || !strings.Contains(got, "原始音频路径") || !strings.Contains(got, "请结合原音分析") {
		t.Fatalf("expected audio analysis prompt, got %q", got)
	}
	if got := len(*sent); got != 2 {
		t.Fatalf("expected 2 outgoing text messages, got %d (%v)", got, *sent)
	}
	if !strings.Contains((*sent)[1], "processed voice deeply") {
		t.Fatalf("expected agent reply, got %q", (*sent)[1])
	}
}

func TestFormatBrandedReply_UsesBrandHeader(t *testing.T) {
	got := formatBrandedReply("codex", "修复已经完成", replyKindAgent)
	if !strings.HasPrefix(got, "OpenAI｜小德总汇报： ") {
		t.Fatalf("unexpected codex header: %q", got)
	}
	if !strings.Contains(got, "修复已经完成") {
		t.Fatalf("expected detail to remain, got %q", got)
	}

	got = formatBrandedReply("claude", "请直接执行下一步。", replyKindStatus)
	if !strings.HasPrefix(got, "Claude｜小克总状态播报： ") {
		t.Fatalf("unexpected claude header: %q", got)
	}
}

func TestSendReplyWithMedia_SendsSingleBrandedText(t *testing.T) {
	srv, sent := newTestILinkServer(t)
	defer srv.Close()

	h := newTestHandler()
	client := ilink.NewClient(&ilink.Credentials{
		BaseURL:    srv.URL,
		BotToken:   "token",
		ILinkBotID: "bot@im.bot",
	})

	msg := ilink.WeixinMessage{
		MessageID:    99,
		FromUserID:   "user@im.wechat",
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ContextToken: "ctx",
	}

	h.sendReplyWithMedia(context.Background(), client, msg, "codex", "Fixed the issue and outlined the next step.", "client-1")

	if got := len(*sent); got != 1 {
		t.Fatalf("expected 1 outgoing text message, got %d (%v)", got, *sent)
	}
	if !strings.Contains((*sent)[0], "OpenAI｜小德总汇报") {
		t.Fatalf("expected branded prefix, got %q", (*sent)[0])
	}
	if !strings.Contains((*sent)[0], "Fixed the issue") {
		t.Fatalf("expected detail text, got %q", (*sent)[0])
	}
}

func TestSaveIncomingMediaFile_AppendsSuffixOnCollision(t *testing.T) {
	tmpDir := t.TempDir()
	sidecar := obsidianarchive.Sidecar{
		Source:           "wechat",
		MediaType:        "image",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		RetentionDays:    7,
		ArchiveState:     obsidianarchive.ArchiveStateActive,
		Title:            "20260407-053903",
		OriginalFilename: "20260407-053903.jpg",
		WechatUserID:     "user@im.wechat",
	}

	first, err := saveIncomingMediaFile(tmpDir, "20260407-053903.jpg", []byte("first"), sidecar, 1)
	if err != nil {
		t.Fatalf("save first file: %v", err)
	}
	second, err := saveIncomingMediaFile(tmpDir, "20260407-053903.jpg", []byte("second"), sidecar, 2)
	if err != nil {
		t.Fatalf("save second file: %v", err)
	}
	third, err := saveIncomingMediaFile(tmpDir, "20260407-053903.jpg", []byte("third"), sidecar, 3)
	if err != nil {
		t.Fatalf("save third file: %v", err)
	}

	if filepath.Base(first) != "20260407-053903.jpg" {
		t.Fatalf("unexpected first filename: %s", first)
	}
	if filepath.Base(second) != "20260407-053903-2.jpg" {
		t.Fatalf("unexpected second filename: %s", second)
	}
	if filepath.Base(third) != "20260407-053903-3.jpg" {
		t.Fatalf("unexpected third filename: %s", third)
	}

	for _, path := range []string{first, second, third} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if len(data) == 0 {
			t.Fatalf("expected file content for %s", path)
		}
		if _, err := os.Stat(path + ".sidecar.md"); err != nil {
			t.Fatalf("expected sidecar for %s: %v", path, err)
		}
	}
}

func TestHandleMessage_AgentArchiveToolCreatesFormalNote(t *testing.T) {
	srv, sent := newTestILinkServer(t)
	defer srv.Close()

	tmpDir := t.TempDir()
	h := newTestHandler()
	h.SetSaveDir(tmpDir)
	h.SetConfig(&config.Config{
		ObsidianEnabled:              true,
		ArchiveToolEnabled:           true,
		ObsidianFormalWriteEnabled:   true,
		ObsidianVaultDir:             filepath.Join(tmpDir, "vault"),
		ObsidianVaultName:            "Obsidian Vault",
		ObsidianNotesDir:             "Inbox/WeChat",
		ObsidianAssetsDir:            "Assets/WeChat",
		ObsidianAutoArchiveEnabled:   true,
		ObsidianAutoArchiveMode:      "hybrid",
		ObsidianArchiveWindowMinutes: 30,
		ObsidianArchiveReplyEnabled:  true,
	})
	mediaPath := filepath.Join(tmpDir, "sample.jpg")
	if err := os.WriteFile(mediaPath, []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mediaPath+".sidecar.md", []byte("---\nid: legacy\nsource: \"wechat\"\nmedia_type: \"image\"\narchive_state: \"active\"\nwechat_user_id: \"user@im.wechat\"\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(tmpDir, ".obsidian", "sessions"), 0o755)
	if err := os.WriteFile(filepath.Join(tmpDir, ".obsidian", "sessions", "user_im.wechat.json"), []byte(`{"user_id":"user@im.wechat","messages":[{"message_id":1,"role":"user","kind":"text","text":"看看这张图","created_at":"`+time.Now().UTC().Format(time.RFC3339)+`"}],"media":[{"message_id":1,"media_type":"image","path":"`+mediaPath+`","created_at":"`+time.Now().UTC().Format(time.RFC3339)+`"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ag := &fakeAgent{reply: "我来整理进知识库。\n\n```weclaw-obsidian-tool\n{\"tool\":\"obsidian_archive\",\"mode\":\"formal\",\"conversation_id\":\"user@im.wechat\",\"title\":\"未来知识库架构\",\"summary\":\"这张图描述了未来知识库架构。\",\"message_ids\":[1,2],\"selected_media_paths\":[\"" + mediaPath + "\"],\"note_body\":\"## 结论\\n\\n这是未来知识库架构草图，应保留到正式知识库。\"}\n```"}
	h.SetDefaultAgent("codex", ag)

	client := ilink.NewClient(&ilink.Credentials{
		BaseURL:    srv.URL,
		BotToken:   "token",
		ILinkBotID: "bot@im.bot",
	})
	msg := ilink.WeixinMessage{
		MessageID:    2,
		FromUserID:   "user@im.wechat",
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ContextToken: "ctx",
		ItemList: []ilink.MessageItem{
			{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "帮我归档到 Obsidian"}},
		},
	}

	h.HandleMessage(context.Background(), client, msg)

	if got := len(*sent); got != 1 {
		t.Fatalf("expected one archive reply, got %d (%v)", got, *sent)
	}
	if !strings.Contains((*sent)[0], "已归档到 Obsidian") {
		t.Fatalf("expected archive success reply, got %q", (*sent)[0])
	}
	notePath := filepath.Join(tmpDir, "vault", "Inbox", "WeChat", time.Now().Format("2006-01"))
	var foundNote string
	_ = filepath.Walk(notePath, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, ".md") {
			foundNote = path
		}
		return nil
	})
	if foundNote == "" {
		t.Fatal("expected formal obsidian note")
	}
	data, err := os.ReadFile(foundNote)
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	if !strings.Contains(string(data), "trigger_reason: \"agent_tool\"") {
		t.Fatalf("expected agent tool audit fields, got %s", string(data))
	}
}
