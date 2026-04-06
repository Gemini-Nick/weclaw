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

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/ilink"
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
	if !strings.Contains(ag.lastMessage(), saved) {
		t.Fatalf("expected agent prompt to reference saved file, got %q", ag.lastMessage())
	}
	if !strings.Contains(ag.lastMessage(), "图片") {
		t.Fatalf("expected agent prompt to mention image, got %q", ag.lastMessage())
	}
	if got := len(*sent); got != 2 {
		t.Fatalf("expected 2 outgoing text messages, got %d (%v)", got, *sent)
	}
	if !strings.Contains((*sent)[0], "Saved:") {
		t.Fatalf("expected save ack, got %q", (*sent)[0])
	}
	if !strings.Contains((*sent)[1], "processed image") {
		t.Fatalf("expected agent reply, got %q", (*sent)[1])
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
	if !strings.Contains(ag.lastMessage(), saved) {
		t.Fatalf("expected agent prompt to reference saved file, got %q", ag.lastMessage())
	}
	if !strings.Contains(ag.lastMessage(), "文件") {
		t.Fatalf("expected agent prompt to mention file, got %q", ag.lastMessage())
	}
	if got := len(*sent); got != 2 {
		t.Fatalf("expected 2 outgoing text messages, got %d (%v)", got, *sent)
	}
	if !strings.Contains((*sent)[0], "Saved: report.pdf") {
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
