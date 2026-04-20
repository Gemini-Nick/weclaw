package messaging

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fastclaw-ai/weclaw/ilink"
)

func TestParseLaunchMentions(t *testing.T) {
	mentions := parseLaunchMentions("请先 @pack signals.review 再用 @skill morning_brief 和 @plugin tushare_connector")
	if len(mentions) != 3 {
		t.Fatalf("expected 3 mentions, got %d", len(mentions))
	}
	if mentions[0].Kind != "pack" || mentions[0].Value != "signals.review" {
		t.Fatalf("unexpected first mention: %#v", mentions[0])
	}
	if mentions[1].Kind != "skill" || mentions[1].Value != "morning_brief" {
		t.Fatalf("unexpected second mention: %#v", mentions[1])
	}
	if mentions[2].Kind != "plugin" || mentions[2].Value != "tushare_connector" {
		t.Fatalf("unexpected third mention: %#v", mentions[2])
	}
}

func TestStripLaunchMentions(t *testing.T) {
	got := stripLaunchMentions("请先 @pack signals.review 再用 @skill morning_brief 和 @plugin tushare_connector")
	want := "请先 再用 和"
	if got != want {
		t.Fatalf("unexpected stripped text: got %q want %q", got, want)
	}
}

func TestSubmitLaunchPostsHermesPayload(t *testing.T) {
	var (
		gotPath    string
		gotPayload map[string]any
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	sink := NewAgentOSEventSink(server.URL, "test-token", "user-123", "", "")
	msg := ilink.WeixinMessage{
		FromUserID:   "wx-user",
		ToUserID:     "wx-bot",
		MessageID:    42,
		ContextToken: "ctx-1",
	}

	err := sink.SubmitLaunch(
		context.Background(),
		msg,
		"请执行 @pack signals.review 并套用 @skill morning_brief",
		"claude",
		[]string{"claude"},
	)
	if err != nil {
		t.Fatalf("submit launch: %v", err)
	}

	if gotPath != "/agent-os/launches" {
		t.Fatalf("unexpected path: %s", gotPath)
	}

	if gotPayload["source"] != "weclaw" {
		t.Fatalf("unexpected source: %#v", gotPayload["source"])
	}
	if gotPayload["work_mode"] != "weclaw_dispatch" {
		t.Fatalf("unexpected work_mode: %#v", gotPayload["work_mode"])
	}
	if gotPayload["launch_surface"] != "weclaw" {
		t.Fatalf("unexpected launch_surface: %#v", gotPayload["launch_surface"])
	}
	if gotPayload["interaction_surface"] != "weclaw" {
		t.Fatalf("unexpected interaction_surface: %#v", gotPayload["interaction_surface"])
	}
	if gotPayload["runtime_profile"] != "dev_local_acp_bridge" {
		t.Fatalf("unexpected runtime_profile: %#v", gotPayload["runtime_profile"])
	}
	if gotPayload["runtime_target"] != "local_runtime" {
		t.Fatalf("unexpected runtime_target: %#v", gotPayload["runtime_target"])
	}
	if gotPayload["model_plane"] != "cloud_provider" {
		t.Fatalf("unexpected model_plane: %#v", gotPayload["model_plane"])
	}
	if gotPayload["workspace_target"] != "ctx-1" {
		t.Fatalf("unexpected workspace_target: %#v", gotPayload["workspace_target"])
	}
	if gotPayload["requested_outcome"] != "请执行 并套用" {
		t.Fatalf("unexpected requested_outcome: %#v", gotPayload["requested_outcome"])
	}

	mentions, ok := gotPayload["mentions"].([]any)
	if !ok || len(mentions) != 2 {
		t.Fatalf("unexpected mentions payload: %#v", gotPayload["mentions"])
	}

	sessionContext, ok := gotPayload["session_context"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected session_context: %#v", gotPayload["session_context"])
	}
	if sessionContext["canonical_session_id"] != "session:user-123" {
		t.Fatalf("unexpected canonical session: %#v", sessionContext["canonical_session_id"])
	}

	metadata, ok := gotPayload["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected metadata: %#v", gotPayload["metadata"])
	}
	if metadata["work_mode"] != "weclaw_dispatch" {
		t.Fatalf("unexpected metadata work_mode: %#v", metadata["work_mode"])
	}
	if metadata["launch_surface"] != "weclaw" {
		t.Fatalf("unexpected metadata launch_surface: %#v", metadata["launch_surface"])
	}
	if metadata["interaction_surface"] != "weclaw" {
		t.Fatalf("unexpected metadata interaction_surface: %#v", metadata["interaction_surface"])
	}
	if metadata["runtime_profile"] != "dev_local_acp_bridge" {
		t.Fatalf("unexpected metadata runtime_profile: %#v", metadata["runtime_profile"])
	}
	if metadata["runtime_target"] != "local_runtime" {
		t.Fatalf("unexpected metadata runtime_target: %#v", metadata["runtime_target"])
	}
	if metadata["model_plane"] != "cloud_provider" {
		t.Fatalf("unexpected metadata model_plane: %#v", metadata["model_plane"])
	}
	if metadata["workspace_target"] != "ctx-1" {
		t.Fatalf("unexpected metadata workspace_target: %#v", metadata["workspace_target"])
	}
}

func TestSubmitLaunchUsesDefaultPackWithoutExplicitMention(t *testing.T) {
	var gotPayload map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	sink := NewAgentOSEventSink(server.URL, "test-token", "user-123", "signals", "review")
	msg := ilink.WeixinMessage{
		FromUserID:   "wx-user",
		ToUserID:     "wx-bot",
		MessageID:    99,
		ContextToken: "ctx-9",
	}

	err := sink.SubmitLaunch(
		context.Background(),
		msg,
		"给我一份今天的盘前重点",
		"claude",
		nil,
	)
	if err != nil {
		t.Fatalf("submit launch: %v", err)
	}

	metadata, ok := gotPayload["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected metadata: %#v", gotPayload["metadata"])
	}
	if metadata["pack_id"] != "signals" {
		t.Fatalf("unexpected default pack metadata: %#v", metadata["pack_id"])
	}
	if metadata["default_capability"] != "review" {
		t.Fatalf("unexpected default capability metadata: %#v", metadata["default_capability"])
	}
	if gotPayload["work_mode"] != "weclaw_dispatch" {
		t.Fatalf("unexpected work_mode: %#v", gotPayload["work_mode"])
	}
	if gotPayload["launch_surface"] != "weclaw" {
		t.Fatalf("unexpected launch_surface: %#v", gotPayload["launch_surface"])
	}
	if gotPayload["interaction_surface"] != "weclaw" {
		t.Fatalf("unexpected interaction_surface: %#v", gotPayload["interaction_surface"])
	}
	if gotPayload["runtime_profile"] != "dev_local_acp_bridge" {
		t.Fatalf("unexpected runtime_profile: %#v", gotPayload["runtime_profile"])
	}
	if gotPayload["runtime_target"] != "local_runtime" {
		t.Fatalf("unexpected runtime_target: %#v", gotPayload["runtime_target"])
	}
	if gotPayload["model_plane"] != "cloud_provider" {
		t.Fatalf("unexpected model_plane: %#v", gotPayload["model_plane"])
	}
	if gotPayload["workspace_target"] != "ctx-9" {
		t.Fatalf("unexpected workspace_target: %#v", gotPayload["workspace_target"])
	}
}
