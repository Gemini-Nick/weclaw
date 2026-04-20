package messaging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/ilink"
)

type AgentOSEventSink struct {
	baseURL           string
	apiKey            string
	canonicalUserID   string
	defaultPackID     string
	defaultCapability string
	httpClient        *http.Client
}

type launchMention struct {
	Kind     string         `json:"kind"`
	Value    string         `json:"value"`
	Label    string         `json:"label,omitempty"`
	Metadata map[string]any `json:"metadata"`
}

func NewAgentOSEventSink(baseURL, apiKey, canonicalUserID, defaultPackID, defaultCapability string) *AgentOSEventSink {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil
	}
	return &AgentOSEventSink{
		baseURL:           baseURL,
		apiKey:            strings.TrimSpace(apiKey),
		canonicalUserID:   strings.TrimSpace(canonicalUserID),
		defaultPackID:     strings.TrimSpace(defaultPackID),
		defaultCapability: strings.TrimSpace(defaultCapability),
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

func (s *AgentOSEventSink) ReportIngress(ctx context.Context, msg ilink.WeixinMessage, text, defaultAgent string) error {
	if s == nil {
		return nil
	}

	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}

	eventID := fmt.Sprintf("wechat:%s:%d", msg.FromUserID, msg.MessageID)
	if msg.MessageID == 0 {
		eventID = fmt.Sprintf("wechat:%s:%d", msg.FromUserID, time.Now().UnixNano())
	}

	metadata := map[string]any{
		"message_id":         msg.MessageID,
		"to_user_id":         msg.ToUserID,
		"context_token":      msg.ContextToken,
		"default_agent":      defaultAgent,
		"delivery_guarantee": "windowed_proactive",
	}
	if s.canonicalUserID != "" {
		metadata["canonical_user_id"] = s.canonicalUserID
	}

	attachments := make([]map[string]any, 0, len(msg.ItemList))
	for _, item := range msg.ItemList {
		attachments = append(attachments, map[string]any{
			"kind": messageItemKind(item.Type),
		})
	}

	payload := map[string]any{
		"event_id":        eventID,
		"channel":         "wechat",
		"channel_user_id": msg.FromUserID,
		"text":            trimmed,
		"occurred_at":     time.Now().UTC().Format(time.RFC3339),
		"attachments":     attachments,
		"metadata":        metadata,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal agent-os ingress payload: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		s.baseURL+"/agent-os/adapters/weclaw/ingest",
		bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("create agent-os ingress request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.apiKey)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("post agent-os ingress: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("agent-os ingress status %d", resp.StatusCode)
	}
	return nil
}

func (s *AgentOSEventSink) SubmitLaunch(
	ctx context.Context,
	msg ilink.WeixinMessage,
	text,
	defaultAgent string,
	targetAgents []string,
) error {
	if s == nil {
		return nil
	}

	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}

	mentions := parseLaunchMentions(trimmed)
	if len(mentions) == 0 && s.defaultPackID == "" {
		return nil
	}

	canonicalUserID := s.canonicalUserID
	if canonicalUserID == "" {
		canonicalUserID = fmt.Sprintf("wechat:%s", msg.FromUserID)
	}

	requestedOutcome := stripLaunchMentions(trimmed)
	if requestedOutcome == "" {
		requestedOutcome = trimmed
	}
	workspaceTarget := strings.TrimSpace(msg.ContextToken)
	if workspaceTarget == "" {
		workspaceTarget = canonicalUserID
	}

	metadata := map[string]any{
		"message_id":          msg.MessageID,
		"to_user_id":          msg.ToUserID,
		"context_token":       msg.ContextToken,
		"default_agent":       defaultAgent,
		"delivery_guarantee":  "windowed_proactive",
		"canonical_user_id":   canonicalUserID,
		"work_mode":           "weclaw_dispatch",
		"launch_surface":      "weclaw",
		"interaction_surface": "weclaw",
		"runtime_profile":     runtimeProfile(),
		"runtime_target":      "local_runtime",
		"model_plane":         "cloud_provider",
		"local_runtime_seat":  localRuntimeSeat(),
		"workspace_target":    workspaceTarget,
	}
	if len(targetAgents) > 0 {
		metadata["target_agents"] = targetAgents
	}
	if packID := firstPackID(mentions); packID != "" {
		metadata["pack_id"] = packID
	} else if s.defaultPackID != "" {
		metadata["pack_id"] = s.defaultPackID
	}
	if s.defaultCapability != "" {
		metadata["default_capability"] = s.defaultCapability
	}

	payload := map[string]any{
		"source":              "weclaw",
		"raw_text":            trimmed,
		"mentions":            mentions,
		"requested_outcome":   requestedOutcome,
		"work_mode":           "weclaw_dispatch",
		"launch_surface":      "weclaw",
		"interaction_surface": "weclaw",
		"runtime_profile":     runtimeProfile(),
		"runtime_target":      "local_runtime",
		"model_plane":         "cloud_provider",
		"local_runtime_seat":  localRuntimeSeat(),
		"workspace_target":    workspaceTarget,
		"session_context": map[string]any{
			"channel":              "wechat",
			"user_id":              canonicalUserID,
			"canonical_id":         "user:" + canonicalUserID,
			"canonical_session_id": "session:" + canonicalUserID,
			"channel_user_id":      msg.FromUserID,
			"context_token":        msg.ContextToken,
		},
		"delivery_preference": map[string]any{
			"policy_id":          "weclaw_front_door",
			"live_reply_channel": "wechat",
			"preferred_channels": []string{"wechat"},
			"fallback_channels":  []string{"desktop"},
			"windowed_proactive": true,
			"desktop_fallback":   true,
			"requires_approval":  false,
			"metadata": map[string]any{
				"adapter": "weclaw",
			},
		},
		"metadata": metadata,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal agent-os launch payload: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		s.baseURL+"/agent-os/launches",
		bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("create agent-os launch request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.apiKey)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("post agent-os launch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("agent-os launch status %d", resp.StatusCode)
	}
	return nil
}

func parseLaunchMentions(text string) []launchMention {
	fields := strings.Fields(text)
	mentions := make([]launchMention, 0, 3)
	for i := 0; i < len(fields); i++ {
		token := strings.ToLower(fields[i])
		if token != "@pack" && token != "@skill" && token != "@plugin" {
			continue
		}
		if i+1 >= len(fields) {
			continue
		}
		value := strings.TrimSpace(fields[i+1])
		if value == "" {
			continue
		}
		mentions = append(mentions, launchMention{
			Kind:     strings.TrimPrefix(token, "@"),
			Value:    value,
			Metadata: map[string]any{},
		})
		i++
	}
	return mentions
}

func runtimeProfile() string {
	value := strings.TrimSpace(os.Getenv("LONGCLAW_RUNTIME_PROFILE"))
	if value == "" {
		if localRuntimeSeat() == "local_runtime_api" {
			return "packaged_local_runtime"
		}
		return "dev_local_acp_bridge"
	}
	return value
}

func localRuntimeSeat() string {
	if value := strings.TrimSpace(os.Getenv("LONGCLAW_LOCAL_ACP_SCRIPT")); value != "" {
		if _, err := os.Stat(value); err == nil {
			return "acp_bridge"
		}
	}

	codexBridge := filepath.Join(os.Getenv("HOME"), ".weclaw", "codex-acp.sh")
	if _, err := os.Stat(codexBridge); err == nil {
		return "acp_bridge"
	}
	claudeBridge := filepath.Join(os.Getenv("HOME"), ".weclaw", "claude-acp.sh")
	if _, err := os.Stat(claudeBridge); err == nil {
		return "acp_bridge"
	}

	if value := strings.TrimSpace(os.Getenv("LONGCLAW_LOCAL_RUNTIME_API_URL")); value != "" {
		return "local_runtime_api"
	}
	return "unavailable"
}

func stripLaunchMentions(text string) string {
	fields := strings.Fields(text)
	out := make([]string, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		token := strings.ToLower(fields[i])
		if (token == "@pack" || token == "@skill" || token == "@plugin") && i+1 < len(fields) {
			i++
			continue
		}
		out = append(out, fields[i])
	}
	return strings.TrimSpace(strings.Join(out, " "))
}

func firstPackID(mentions []launchMention) string {
	for _, mention := range mentions {
		if mention.Kind != "pack" {
			continue
		}
		value := strings.TrimSpace(mention.Value)
		if value == "" {
			continue
		}
		if strings.Contains(value, ".") {
			return strings.SplitN(value, ".", 2)[0]
		}
		if strings.Contains(value, ":") {
			return strings.SplitN(value, ":", 2)[0]
		}
		return value
	}
	return ""
}

func messageItemKind(itemType int) string {
	switch itemType {
	case ilink.ItemTypeText:
		return "text"
	case ilink.ItemTypeImage:
		return "image"
	case ilink.ItemTypeVoice:
		return "voice"
	case ilink.ItemTypeFile:
		return "file"
	case ilink.ItemTypeVideo:
		return "video"
	default:
		return "unknown"
	}
}
