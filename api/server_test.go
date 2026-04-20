package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/messaging"
)

func TestHandleAgentOSLaunchSubmitsIngressAndLaunch(t *testing.T) {
	var (
		ingressCount int
		launchCount  int
		launchBody   map[string]any
	)

	hermes := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent-os/adapters/weclaw/ingest":
			ingressCount++
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/agent-os/launches":
			launchCount++
			if err := json.NewDecoder(r.Body).Decode(&launchBody); err != nil {
				t.Fatalf("decode launch body: %v", err)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer hermes.Close()

	handler := messaging.NewHandler(nil, nil)
	handler.SetAgentOSEventSink(
		messaging.NewAgentOSEventSink(hermes.URL, "test-token", "canonical-user", "", ""),
	)
	server := NewServer(nil, "", handler)

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/agent-os/launch",
		strings.NewReader(`{"text":"@pack signals.review 请给我一份盘前摘要","from_user_id":"tester@im.wechat","context_token":"ctx-1","target_agents":["claude"]}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	server.handleAgentOSLaunch(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", response.Code, response.Body.String())
	}
	if ingressCount != 1 {
		t.Fatalf("expected 1 ingress call, got %d", ingressCount)
	}
	if launchCount != 1 {
		t.Fatalf("expected 1 launch call, got %d", launchCount)
	}
	if launchBody["source"] != "weclaw" {
		t.Fatalf("unexpected launch source: %#v", launchBody["source"])
	}
	if launchBody["work_mode"] != "weclaw_dispatch" {
		t.Fatalf("unexpected work_mode: %#v", launchBody["work_mode"])
	}
	if launchBody["launch_surface"] != "weclaw" {
		t.Fatalf("unexpected launch_surface: %#v", launchBody["launch_surface"])
	}
	if launchBody["workspace_target"] != "ctx-1" {
		t.Fatalf("unexpected workspace_target: %#v", launchBody["workspace_target"])
	}
	if launchBody["requested_outcome"] != "请给我一份盘前摘要" {
		t.Fatalf("unexpected requested_outcome: %#v", launchBody["requested_outcome"])
	}
}
