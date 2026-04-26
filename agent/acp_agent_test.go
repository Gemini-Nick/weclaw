package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCodexAppServerErrorNotificationReturnsRealError(t *testing.T) {
	a := NewACPAgent(ACPAgentConfig{
		Command: "codex",
		Args:    []string{"app-server", "--listen", "stdio://"},
	})
	a.started = true
	a.rpcCall = func(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
		switch method {
		case "thread/start":
			return json.RawMessage(`{"thread":{"id":"thread-1"}}`), nil
		case "turn/start":
			a.handleCodexError(json.RawMessage(`{"threadId":"thread-1","error":{"message":"{\"type\":\"error\",\"status\":400,\"error\":{\"type\":\"invalid_request_error\",\"message\":\"The 'gpt-5.5' model requires a newer version of Codex. Please upgrade.\"}}"}}`))
			a.handleCodexTurnEvent("turn/completed", json.RawMessage(`{"threadId":"thread-1","status":"completed"}`))
			return json.RawMessage(`{"turn":{"id":"turn-1"}}`), nil
		default:
			return nil, fmt.Errorf("unexpected rpc method %s", method)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	reply, err := a.Chat(ctx, "user@im.wechat", "hello")
	if err == nil {
		t.Fatal("expected codex error notification to return an error")
	}
	if reply != "" {
		t.Fatalf("expected empty reply on error, got %q", reply)
	}
	if !strings.Contains(err.Error(), "requires a newer version of Codex") {
		t.Fatalf("expected real codex error, got %v", err)
	}
	if strings.Contains(err.Error(), "agent returned empty response") {
		t.Fatalf("expected no empty-response masking, got %v", err)
	}
}
