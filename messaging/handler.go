package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/ilink"
	obsidianarchive "github.com/fastclaw-ai/weclaw/obsidian"
	"github.com/google/uuid"
)

// AgentFactory creates an agent by config name. Returns nil if the name is unknown.
type AgentFactory func(ctx context.Context, name string) agent.Agent

// SaveDefaultFunc persists the default agent name to config file.
type SaveDefaultFunc func(name string) error

// AgentMeta holds static config info about an agent (for /status display).
type AgentMeta struct {
	Name    string
	Type    string // "acp", "cli", "http"
	Command string // binary path or endpoint
	Model   string
}

// Handler processes incoming WeChat messages and dispatches replies.
type Handler struct {
	mu               sync.RWMutex
	defaultName      string
	agents           map[string]agent.Agent // name -> running agent
	agentMetas       []AgentMeta            // all configured agents (for /status)
	agentWorkDirs    map[string]string      // agent name -> configured/runtime cwd
	customAliases    map[string]string      // custom alias -> agent name (from config)
	factory          AgentFactory
	saveDefault      SaveDefaultFunc
	contextTokens    sync.Map // map[userID]contextToken
	saveDir          string   // directory to save images/files to
	personaDir       string   // directory with persona assets + manifest
	cfg              *config.Config
	agentOSEventSink *AgentOSEventSink
	seenMsgs         sync.Map // map[string]time.Time — dedup by user_id + message_id
}

const (
	voiceModeTranscriptFirst      = "transcript_first"
	voiceModeTranscriptPlusAudio  = "transcript_plus_audio_context"
	voiceModeAudioAnalysisRequest = "audio_analysis_requested"
)

// NewHandler creates a new message handler.
func NewHandler(factory AgentFactory, saveDefault SaveDefaultFunc) *Handler {
	return &Handler{
		agents:        make(map[string]agent.Agent),
		agentWorkDirs: make(map[string]string),
		factory:       factory,
		saveDefault:   saveDefault,
	}
}

// SetSaveDir sets the directory for saving images and files.
func (h *Handler) SetSaveDir(dir string) {
	h.saveDir = dir
}

// SetPersonaDir sets the directory for persona assets and manifest.
func (h *Handler) SetPersonaDir(dir string) {
	h.personaDir = dir
}

func (h *Handler) SetConfig(cfg *config.Config) {
	h.cfg = cfg
}

func (h *Handler) SetAgentOSEventSink(sink *AgentOSEventSink) {
	h.agentOSEventSink = sink
}

// cleanSeenMsgs removes entries older than 5 minutes from the dedup cache.
func (h *Handler) cleanSeenMsgs() {
	cutoff := time.Now().Add(-5 * time.Minute)
	h.seenMsgs.Range(func(key, value any) bool {
		if t, ok := value.(time.Time); ok && t.Before(cutoff) {
			h.seenMsgs.Delete(key)
		}
		return true
	})
}

func (h *Handler) claimIncomingMessage(userID string, messageID int64) bool {
	if messageID == 0 {
		return true
	}

	now := time.Now()
	key := fmt.Sprintf("%s:%d", userID, messageID)
	if _, loaded := h.seenMsgs.LoadOrStore(key, now); loaded {
		return false
	}

	claimed, err := claimDurableSeenMessage(userID, messageID, now)
	if err != nil {
		log.Printf("[handler] durable message dedup unavailable for %s/%d: %v", userID, messageID, err)
		go h.cleanSeenMsgs()
		return true
	}
	if !claimed {
		h.seenMsgs.Delete(key)
		return false
	}

	go h.cleanSeenMsgs()
	go cleanupDurableSeenMessages(now)
	return true
}

// SetCustomAliases sets custom alias mappings from config.
func (h *Handler) SetCustomAliases(aliases map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.customAliases = aliases
}

// SetAgentMetas sets the list of all configured agents (for /status).
func (h *Handler) SetAgentMetas(metas []AgentMeta) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.agentMetas = metas
}

// SetAgentWorkDirs sets the configured working directory for each agent.
func (h *Handler) SetAgentWorkDirs(workDirs map[string]string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.agentWorkDirs = make(map[string]string, len(workDirs))
	for name, dir := range workDirs {
		h.agentWorkDirs[name] = dir
	}
}

// SetDefaultAgent sets the default agent (already started).
func (h *Handler) SetDefaultAgent(name string, ag agent.Agent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.defaultName = name
	h.agents[name] = ag
	log.Printf("[handler] default agent ready: %s (%s)", name, ag.Info())
}

// getAgent returns a running agent by name, or starts it on demand via factory.
func (h *Handler) getAgent(ctx context.Context, name string) (agent.Agent, error) {
	// Fast path: already running
	h.mu.RLock()
	ag, ok := h.agents[name]
	h.mu.RUnlock()
	if ok {
		return ag, nil
	}

	// Slow path: create on demand
	if h.factory == nil {
		return nil, fmt.Errorf("agent %q not found and no factory configured", name)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// Double-check after acquiring write lock
	if ag, ok := h.agents[name]; ok {
		return ag, nil
	}

	log.Printf("[handler] starting agent %q on demand...", name)
	ag = h.factory(ctx, name)
	if ag == nil {
		return nil, fmt.Errorf("agent %q not available", name)
	}

	h.agents[name] = ag
	log.Printf("[handler] agent started on demand: %s (%s)", name, ag.Info())
	return ag, nil
}

// getDefaultAgent returns the default agent (may be nil if not ready yet).
func (h *Handler) getDefaultAgent() agent.Agent {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.defaultName == "" {
		return nil
	}
	return h.agents[h.defaultName]
}

func (h *Handler) getDefaultAgentName() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.defaultName
}

// isKnownAgent checks if a name corresponds to a configured agent.
func (h *Handler) isKnownAgent(name string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	// Check running agents
	if _, ok := h.agents[name]; ok {
		return true
	}
	// Check configured agents (metas)
	for _, meta := range h.agentMetas {
		if meta.Name == name {
			return true
		}
	}
	return false
}

// agentAliases maps short aliases to agent config names.
var agentAliases = map[string]string{
	"cc":  "claude",
	"cx":  "codex",
	"oc":  "openclaw",
	"cs":  "cursor",
	"km":  "kimi",
	"gm":  "gemini",
	"ocd": "opencode",
	"pi":  "pi",
	"cp":  "copilot",
	"dr":  "droid",
	"if":  "iflow",
	"kr":  "kiro",
	"qw":  "qwen",
}

// resolveAlias returns the full agent name for an alias, or the original name if no alias matches.
// Checks custom aliases (from config) first, then built-in aliases.
func (h *Handler) resolveAlias(name string) string {
	h.mu.RLock()
	custom := h.customAliases
	h.mu.RUnlock()
	if custom != nil {
		if full, ok := custom[name]; ok {
			return full
		}
	}
	if full, ok := agentAliases[name]; ok {
		return full
	}
	return name
}

// parseCommand checks if text starts with "/" or "@" followed by agent name(s).
// Supports multiple agents: "@cc @cx hello" returns (["claude","codex"], "hello").
// Returns (agentNames, actualMessage). Aliases are resolved automatically.
// If no command prefix, returns (nil, originalText).
func (h *Handler) parseCommand(text string) ([]string, string) {
	if !strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "@") {
		return nil, text
	}

	// Parse consecutive @name or /name tokens from the start
	var names []string
	rest := text
	for {
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, "/") && !strings.HasPrefix(rest, "@") {
			break
		}

		// Strip prefix
		after := rest[1:]
		idx := strings.IndexAny(after, " /@")
		var token string
		if idx < 0 {
			// Rest is just the name, no message
			token = after
			rest = ""
		} else if after[idx] == '/' || after[idx] == '@' {
			// Next token is another @name or /name
			token = after[:idx]
			rest = after[idx:]
		} else {
			// Space — name ends here
			token = after[:idx]
			rest = strings.TrimSpace(after[idx+1:])
		}

		if token != "" {
			names = append(names, h.resolveAlias(token))
		}

		if rest == "" {
			break
		}
	}

	// Deduplicate names preserving order
	seen := make(map[string]bool)
	unique := names[:0]
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			unique = append(unique, n)
		}
	}

	return unique, rest
}

// HandleMessage processes a single incoming message.
func (h *Handler) HandleMessage(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage) {
	// Only process user messages that are finished
	if msg.MessageType != ilink.MessageTypeUser {
		return
	}
	if msg.MessageState != ilink.MessageStateFinish {
		return
	}

	// Deduplicate by message_id to avoid processing the same message multiple times
	// (voice messages may trigger multiple finish-state updates)
	if !h.claimIncomingMessage(msg.FromUserID, msg.MessageID) {
		return
	}

	log.Printf("[handler] incoming message id=%d from=%s items=%s", msg.MessageID, msg.FromUserID, describeMessageItems(msg))
	h.contextTokens.Store(msg.FromUserID, msg.ContextToken)
	if err := RememberContextToken(msg.FromUserID, msg.ContextToken); err != nil {
		log.Printf("[handler] failed to persist context token for %s: %v", msg.FromUserID, err)
	}

	var savedVoicePath string
	var savedVoiceTranscript string
	if voice := extractVoice(msg); voice != nil && h.saveDir != "" {
		path, transcript, ok := h.handleVoiceSave(ctx, client, msg, voice)
		if ok {
			savedVoicePath = path
			savedVoiceTranscript = transcript
		}
	}
	if video := extractVideo(msg); video != nil && h.saveDir != "" {
		if h.handleVideoSave(ctx, client, msg, video) {
			return
		}
	}

	// Extract text from item list (text message or voice transcription)
	text := extractText(msg)
	if text == "" {
		if voiceText := extractVoiceText(msg); voiceText != "" {
			text = voiceText
			log.Printf("[handler] voice transcription from %s: %q", msg.FromUserID, truncate(text, 80))
		}
	}
	voiceMode := resolveVoiceInputMode(h.cfg, savedVoiceTranscript, savedVoicePath != "")
	if text != "" && savedVoicePath != "" {
		text = buildVoiceAgentInput(strings.TrimSpace(text), savedVoicePath, voiceMode)
		log.Printf("[handler] voice_mode=%s agent_audio_exposed=%t from=%s", voiceMode, voiceModeExposesAudio(voiceMode), msg.FromUserID)
	}
	if text == "" {
		if img := extractImage(msg); img != nil && h.saveDir != "" {
			h.handleImageSave(ctx, client, msg, img)
			return
		}
		if file := extractFile(msg); file != nil && h.saveDir != "" {
			h.handleFileSave(ctx, client, msg, file)
			return
		}
		if savedVoicePath != "" {
			prompt := buildVoiceAgentInput(savedVoiceTranscript, savedVoicePath, voiceMode)
			log.Printf("[handler] voice_mode=%s agent_audio_exposed=%t from=%s", voiceMode, voiceModeExposesAudio(voiceMode), msg.FromUserID)
			h.dispatchSavedMediaPrompt(ctx, client, msg, "语音", prompt)
			return
		}
		log.Printf("[handler] received non-text message from %s, skipping", msg.FromUserID)
		return
	}

	log.Printf("[handler] received from %s: %q", msg.FromUserID, truncate(text, 80))
	h.reportAgentOSIngress(msg, text)

	// Generate a clientID for this reply (used to correlate typing → finish)
	clientID := NewClientID()

	// Intercept URLs: save to Linkhoard directly without AI agent
	trimmed := strings.TrimSpace(text)
	if h.cfg != nil && h.saveDir != "" {
		_ = obsidianarchive.RecordUserMessage(h.saveDir, msg.FromUserID, msg.MessageID, trimmed)
	}
	if h.saveDir != "" && IsURL(trimmed) {
		rawURL := ExtractURL(trimmed)
		if rawURL != "" {
			log.Printf("[handler] saving URL to linkhoard: %s", rawURL)
			title, err := SaveLinkToLinkhoard(ctx, h.saveDir, rawURL)
			var reply string
			if err != nil {
				log.Printf("[handler] link save failed: %v", err)
				reply = fmt.Sprintf("保存失败: %v", err)
			} else {
				reply = fmt.Sprintf("已保存: %s", title)
			}
			h.sendStructuredTextReply(ctx, client, msg, h.getDefaultAgentName(), reply, clientID, replyKindLinkhoard)
			return
		}
	}

	// Built-in commands (no typing needed)
	if trimmed == "/info" {
		reply := h.buildStatus()
		h.sendStructuredTextReply(ctx, client, msg, h.getDefaultAgentName(), reply, clientID, replyKindStatus)
		return
	} else if trimmed == "/runtime" {
		reply := buildRuntimeStatusReply()
		h.sendStructuredTextReply(ctx, client, msg, h.getDefaultAgentName(), reply, clientID, replyKindStatus)
		return
	} else if trimmed == "/help" {
		reply := buildHelpText()
		h.sendStructuredTextReply(ctx, client, msg, h.getDefaultAgentName(), reply, clientID, replyKindHelp)
		return
	} else if trimmed == "/new" || trimmed == "/clear" {
		reply := h.resetDefaultSession(ctx, msg.FromUserID)
		h.sendStructuredTextReply(ctx, client, msg, h.getDefaultAgentName(), reply, clientID, replyKindStatus)
		return
	} else if strings.HasPrefix(trimmed, "/cwd") {
		reply := h.handleCwd(trimmed)
		h.sendStructuredTextReply(ctx, client, msg, h.getDefaultAgentName(), reply, clientID, replyKindStatus)
		return
	}
	if taskText, ok := ExtractQueuedTask(trimmed); ok {
		task, err := EnqueueQueuedTask(msg, trimmed, taskText)
		if err != nil {
			log.Printf("[handler] failed to enqueue queued task from %s: %v", msg.FromUserID, err)
			h.sendStructuredTextReply(ctx, client, msg, h.getDefaultAgentName(), fmt.Sprintf("任务排队失败：%v", err), clientID, replyKindError)
			return
		}
		reply := fmt.Sprintf("任务已排队\nid: %s\n将在当前轮结束后执行", task.ID)
		h.sendStructuredTextReply(ctx, client, msg, h.getDefaultAgentName(), reply, clientID, replyKindStatus)
		return
	}

	// Route: "/agentname message" or "@agent1 @agent2 message" -> specific agent(s)
	agentNames, message := h.parseCommand(text)

	// No command prefix -> send to default agent
	if len(agentNames) == 0 {
		h.reportAgentOSLaunch(msg, text, nil)
		h.sendToDefaultAgent(ctx, client, msg, text, clientID)
		return
	}

	// No message -> switch default agent (only first name)
	if message == "" {
		if len(agentNames) == 1 && h.isKnownAgent(agentNames[0]) {
			reply := h.switchDefault(ctx, agentNames[0])
			h.sendStructuredTextReply(ctx, client, msg, agentNames[0], reply, clientID, replyKindSwitch)
		} else if len(agentNames) == 1 && !h.isKnownAgent(agentNames[0]) {
			// Unknown agent -> forward to default
			h.sendToDefaultAgent(ctx, client, msg, text, clientID)
		} else {
			reply := "Usage: specify one agent to switch, or add a message to broadcast"
			h.sendStructuredTextReply(ctx, client, msg, h.getDefaultAgentName(), reply, clientID, replyKindUsage)
		}
		return
	}

	// Filter to known agents; if single unknown agent -> forward to default
	var knownNames []string
	for _, name := range agentNames {
		if h.isKnownAgent(name) {
			knownNames = append(knownNames, name)
		}
	}
	if len(knownNames) == 0 {
		// No known agents -> forward entire text to default agent
		h.reportAgentOSLaunch(msg, text, nil)
		h.sendToDefaultAgent(ctx, client, msg, text, clientID)
		return
	}

	h.reportAgentOSLaunch(msg, message, knownNames)

	// Send typing indicator
	go func() {
		if typingErr := SendTypingState(ctx, client, msg.FromUserID, msg.ContextToken); typingErr != nil {
			log.Printf("[handler] failed to send typing state: %v", typingErr)
		}
	}()

	if len(knownNames) == 1 {
		// Single agent
		h.sendToNamedAgent(ctx, client, msg, knownNames[0], message, clientID)
	} else {
		// Multi-agent broadcast: parallel dispatch, send replies as they arrive
		h.broadcastToAgents(ctx, client, msg, knownNames, message)
	}
}

type DispatchTaskResult struct {
	Mode         string
	Agent        string
	ReplyPreview string
}

func (h *Handler) DispatchQueuedTask(ctx context.Context, client *ilink.Client, userID, text string) (*DispatchTaskResult, error) {
	contextToken, _ := LookupContextToken(userID)
	msg := ilink.WeixinMessage{
		FromUserID:   userID,
		MessageType:  ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ContextToken: contextToken,
	}
	clientID := NewClientID()

	go func() {
		if typingErr := SendTypingState(ctx, client, userID, contextToken); typingErr != nil {
			log.Printf("[handler] failed to send queued-task typing state: %v", typingErr)
		}
	}()

	agentNames, message := h.parseCommand(text)
	if len(agentNames) == 0 {
		defaultName := h.getDefaultAgentName()
		ag := h.getDefaultAgent()
		if ag == nil {
			reply := "[echo] " + text
			h.sendReplyWithMedia(ctx, client, msg, defaultName, reply, clientID)
			return &DispatchTaskResult{
				Mode:         "default_agent",
				Agent:        defaultName,
				ReplyPreview: truncate(reply, 120),
			}, nil
		}
		reply, err := h.chatWithAgentWithTools(ctx, ag, msg, defaultName, text)
		if err != nil {
			reply = fmt.Sprintf("Error: %v", err)
			h.sendReplyWithMedia(ctx, client, msg, defaultName, reply, clientID)
			return &DispatchTaskResult{
				Mode:         "default_agent",
				Agent:        defaultName,
				ReplyPreview: truncate(reply, 120),
			}, err
		}
		h.sendReplyWithMedia(ctx, client, msg, defaultName, reply, clientID)
		return &DispatchTaskResult{
			Mode:         "default_agent",
			Agent:        defaultName,
			ReplyPreview: truncate(reply, 120),
		}, nil
	}

	if strings.TrimSpace(message) == "" {
		return nil, fmt.Errorf("queued task missing message body after agent prefix")
	}

	var knownNames []string
	for _, name := range agentNames {
		if h.isKnownAgent(name) {
			knownNames = append(knownNames, name)
		}
	}
	if len(knownNames) == 0 {
		defaultName := h.getDefaultAgentName()
		ag := h.getDefaultAgent()
		if ag == nil {
			reply := "[echo] " + text
			h.sendReplyWithMedia(ctx, client, msg, defaultName, reply, clientID)
			return &DispatchTaskResult{
				Mode:         "default_agent",
				Agent:        defaultName,
				ReplyPreview: truncate(reply, 120),
			}, nil
		}
		reply, err := h.chatWithAgentWithTools(ctx, ag, msg, defaultName, text)
		if err != nil {
			reply = fmt.Sprintf("Error: %v", err)
			h.sendReplyWithMedia(ctx, client, msg, defaultName, reply, clientID)
			return &DispatchTaskResult{
				Mode:         "default_agent",
				Agent:        defaultName,
				ReplyPreview: truncate(reply, 120),
			}, err
		}
		h.sendReplyWithMedia(ctx, client, msg, defaultName, reply, clientID)
		return &DispatchTaskResult{
			Mode:         "default_agent",
			Agent:        defaultName,
			ReplyPreview: truncate(reply, 120),
		}, nil
	}

	if len(knownNames) == 1 {
		name := knownNames[0]
		ag, err := h.getAgent(ctx, name)
		if err != nil {
			reply := fmt.Sprintf("Agent %q is not available: %v", name, err)
			h.sendStructuredTextReply(ctx, client, msg, name, reply, clientID, replyKindError)
			return &DispatchTaskResult{
				Mode:         "named_agent",
				Agent:        name,
				ReplyPreview: truncate(reply, 120),
			}, err
		}
		reply, err := h.chatWithAgentWithTools(ctx, ag, msg, name, message)
		if err != nil {
			reply = fmt.Sprintf("Error: %v", err)
			h.sendReplyWithMedia(ctx, client, msg, name, reply, clientID)
			return &DispatchTaskResult{
				Mode:         "named_agent",
				Agent:        name,
				ReplyPreview: truncate(reply, 120),
			}, err
		}
		h.sendReplyWithMedia(ctx, client, msg, name, reply, clientID)
		return &DispatchTaskResult{
			Mode:         "named_agent",
			Agent:        name,
			ReplyPreview: truncate(reply, 120),
		}, nil
	}

	h.broadcastToAgents(ctx, client, msg, knownNames, message)
	return &DispatchTaskResult{
		Mode:         "multi_agent_broadcast",
		Agent:        strings.Join(knownNames, ","),
		ReplyPreview: fmt.Sprintf("broadcast sent to %s", strings.Join(knownNames, ",")),
	}, nil
}

func (h *Handler) reportAgentOSIngress(msg ilink.WeixinMessage, text string) {
	go func() {
		reqCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := h.submitAgentOSIngress(reqCtx, msg, text); err != nil {
			log.Printf("[handler] agent-os ingress failed message_id=%d from=%s: %v", msg.MessageID, msg.FromUserID, err)
		}
	}()
}

func (h *Handler) reportAgentOSLaunch(msg ilink.WeixinMessage, text string, targetAgents []string) {
	go func() {
		reqCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := h.submitAgentOSLaunch(reqCtx, msg, text, targetAgents); err != nil {
			log.Printf("[handler] agent-os launch failed message_id=%d from=%s: %v", msg.MessageID, msg.FromUserID, err)
		}
	}()
}

func (h *Handler) submitAgentOSIngress(ctx context.Context, msg ilink.WeixinMessage, text string) error {
	sink := h.agentOSEventSink
	if sink == nil {
		return fmt.Errorf("agent-os sink not configured")
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return sink.ReportIngress(ctx, msg, text, h.getDefaultAgentName())
}

func (h *Handler) submitAgentOSLaunch(
	ctx context.Context,
	msg ilink.WeixinMessage,
	text string,
	targetAgents []string,
) error {
	sink := h.agentOSEventSink
	if sink == nil {
		return fmt.Errorf("agent-os sink not configured")
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return sink.SubmitLaunch(ctx, msg, text, h.getDefaultAgentName(), targetAgents)
}

func (h *Handler) SubmitAgentOSFrontDoor(
	ctx context.Context,
	msg ilink.WeixinMessage,
	text string,
	targetAgents []string,
) error {
	if err := h.submitAgentOSIngress(ctx, msg, text); err != nil {
		return err
	}
	return h.submitAgentOSLaunch(ctx, msg, text, targetAgents)
}

// sendToDefaultAgent sends the message to the default agent and replies.
func (h *Handler) sendToDefaultAgent(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, text, clientID string) {
	go func() {
		if typingErr := SendTypingState(ctx, client, msg.FromUserID, msg.ContextToken); typingErr != nil {
			log.Printf("[handler] failed to send typing state: %v", typingErr)
		}
	}()

	h.mu.RLock()
	defaultName := h.defaultName
	h.mu.RUnlock()

	ag := h.getDefaultAgent()
	var reply string
	if ag != nil {
		var err error
		reply, err = h.chatWithAgentWithTools(ctx, ag, msg, defaultName, text)
		if err != nil {
			reply = fmt.Sprintf("Error: %v", err)
		}
	} else {
		log.Printf("[handler] agent not ready, using echo mode for %s", msg.FromUserID)
		reply = "[echo] " + text
	}

	h.sendReplyWithMedia(ctx, client, msg, defaultName, reply, clientID)
}

// sendToNamedAgent sends the message to a specific agent and replies.
func (h *Handler) sendToNamedAgent(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, name, message, clientID string) {
	ag, agErr := h.getAgent(ctx, name)
	if agErr != nil {
		log.Printf("[handler] agent %q not available: %v", name, agErr)
		reply := fmt.Sprintf("Agent %q is not available: %v", name, agErr)
		h.sendStructuredTextReply(ctx, client, msg, name, reply, clientID, replyKindError)
		return
	}

	reply, err := h.chatWithAgentWithTools(ctx, ag, msg, name, message)
	if err != nil {
		reply = fmt.Sprintf("Error: %v", err)
	}
	h.sendReplyWithMedia(ctx, client, msg, name, reply, clientID)
}

// broadcastToAgents sends the message to multiple agents in parallel.
// Each reply is sent as a separate message with the agent name prefix.
func (h *Handler) broadcastToAgents(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, names []string, message string) {
	type result struct {
		name  string
		reply string
	}

	ch := make(chan result, len(names))

	for _, name := range names {
		go func(n string) {
			ag, err := h.getAgent(ctx, n)
			if err != nil {
				ch <- result{name: n, reply: fmt.Sprintf("Error: %v", err)}
				return
			}
			reply, err := h.chatWithAgentWithTools(ctx, ag, msg, n, message)
			if err != nil {
				ch <- result{name: n, reply: fmt.Sprintf("Error: %v", err)}
				return
			}
			ch <- result{name: n, reply: reply}
		}(name)
	}

	// Send replies as they arrive
	for range names {
		r := <-ch
		reply := fmt.Sprintf("[%s] %s", r.name, r.reply)
		clientID := NewClientID()
		h.sendReplyWithMedia(ctx, client, msg, r.name, reply, clientID)
	}
}

// sendReplyWithMedia sends a text reply and any extracted image URLs.
func (h *Handler) sendReplyWithMedia(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, agentName, reply, clientID string) {
	imageURLs := ExtractImageURLs(reply)
	attachmentPaths := extractLocalAttachmentPaths(reply)
	allowedRoots := h.allowedAttachmentRoots(agentName)

	detail := rewriteReplyForDeferredAttachments(reply, attachmentPaths)

	var failedPaths []string
	for _, attachmentPath := range attachmentPaths {
		if !isAllowedAttachmentPath(attachmentPath, allowedRoots) {
			log.Printf("[handler] rejected attachment outside allowed roots for agent %q: %s", agentName, attachmentPath)
			failedPaths = append(failedPaths, attachmentPath)
		}
	}

	h.sendStructuredTextReply(ctx, client, msg, agentName, detail, clientID, replyKindAgent)

	for _, attachmentPath := range attachmentPaths {
		if !isAllowedAttachmentPath(attachmentPath, allowedRoots) {
			continue
		}
		if err := SendMediaFromPath(ctx, client, msg.FromUserID, attachmentPath, msg.ContextToken); err != nil {
			log.Printf("[handler] failed to send attachment to %s: %v", msg.FromUserID, err)
			failedPaths = append(failedPaths, attachmentPath)
		}
	}

	if len(failedPaths) > 0 {
		failureReply := rewriteReplyWithAttachmentResults("", nil, failedPaths)
		h.sendStructuredTextReply(ctx, client, msg, agentName, failureReply, NewClientID(), replyKindError)
	}

	for _, imgURL := range imageURLs {
		if err := SendMediaFromURL(ctx, client, msg.FromUserID, imgURL, msg.ContextToken); err != nil {
			log.Printf("[handler] failed to send image to %s: %v", msg.FromUserID, err)
		}
	}

	if h.saveDir != "" {
		_ = obsidianarchive.RecordAgentReply(h.saveDir, msg.FromUserID, msg.MessageID, agentName, detail)
	}

	log.Printf("[handler] delivered reply to %s (agent=%s, text_chars=%d, image_urls=%d, attachments=%d, failed_attachments=%d)", msg.FromUserID, agentName, len([]rune(strings.TrimSpace(detail))), len(imageURLs), len(attachmentPaths), len(failedPaths))
}

func (h *Handler) sendStructuredTextReply(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, agentName, detail, clientID string, kind replyKind) {
	if strings.TrimSpace(detail) == "" {
		return
	}

	if agentName == "" {
		agentName = h.getDefaultAgentName()
	}

	reply := formatBrandedReply(agentName, detail, kind)
	if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
		log.Printf("[handler] failed to send reply to %s: %v", msg.FromUserID, err)
	}
}

func (h *Handler) allowedAttachmentRoots(agentName string) []string {
	roots := []string{defaultAttachmentWorkspace()}

	h.mu.RLock()
	agentDir := h.agentWorkDirs[agentName]
	h.mu.RUnlock()

	if agentDir != "" {
		roots = append(roots, agentDir)
	}

	return roots
}

// chatWithAgent sends a message to an agent and returns the reply, with logging.
func (h *Handler) chatWithAgent(ctx context.Context, ag agent.Agent, userID, message string) (string, error) {
	info := ag.Info()
	log.Printf("[handler] dispatching to agent (%s) for %s", info, userID)

	start := time.Now()
	reply, err := ag.Chat(ctx, userID, message)
	elapsed := time.Since(start)

	if err != nil {
		log.Printf("[handler] agent error (%s, elapsed=%s): %v", info, elapsed, err)
		return "", err
	}

	imageURLs := ExtractImageURLs(reply)
	attachmentPaths := extractLocalAttachmentPaths(reply)
	log.Printf("[handler] agent completed (%s, elapsed=%s, chars=%d, image_urls=%d, attachments=%d)", info, elapsed, len([]rune(strings.TrimSpace(reply))), len(imageURLs), len(attachmentPaths))
	log.Printf("[handler] agent replied (%s, elapsed=%s): %q", info, elapsed, truncate(reply, 100))
	return reply, nil
}

func (h *Handler) chatWithAgentWithTools(ctx context.Context, ag agent.Agent, msg ilink.WeixinMessage, agentName, message string) (string, error) {
	agentMessage := message
	if h.cfg != nil && h.saveDir != "" {
		agentMessage = obsidianarchive.BuildAgentArchivePrompt(h.cfg, h.saveDir, msg.FromUserID, message)
	}

	reply, err := h.chatWithAgent(ctx, ag, msg.FromUserID, agentMessage)
	if err != nil {
		return "", err
	}

	if h.cfg == nil || h.saveDir == "" || !h.cfg.ObsidianEnabled {
		return reply, nil
	}

	result, err := obsidianarchive.ApplyAgentArchiveTool(h.cfg, h.saveDir, msg.FromUserID, reply)
	if err != nil {
		return fmt.Sprintf("知识库归档失败：%v", err), nil
	}
	if result.Invoked {
		return result.Reply, nil
	}
	if obsidianarchive.LikelyArchiveIntent(h.cfg, message) {
		log.Printf("[obsidian] archive_intent_unhandled conversation_id=%s agent=%s", msg.FromUserID, agentName)
	}
	return result.Reply, nil
}

// switchDefault switches the default agent. Starts it on demand if needed.
// The change is persisted to config file.
func (h *Handler) switchDefault(ctx context.Context, name string) string {
	ag, err := h.getAgent(ctx, name)
	if err != nil {
		log.Printf("[handler] failed to switch default to %q: %v", name, err)
		return fmt.Sprintf("Failed to switch to %q: %v", name, err)
	}

	h.mu.Lock()
	old := h.defaultName
	h.defaultName = name
	h.agents[name] = ag
	h.mu.Unlock()

	// Persist to config file
	if h.saveDefault != nil {
		if err := h.saveDefault(name); err != nil {
			log.Printf("[handler] failed to save default agent to config: %v", err)
		} else {
			log.Printf("[handler] saved default agent %q to config", name)
		}
	}

	info := ag.Info()
	log.Printf("[handler] switched default agent: %s -> %s (%s)", old, name, info)
	return fmt.Sprintf("switch to %s", name)
}

// resetDefaultSession resets the session for the given userID on the default agent.
func (h *Handler) resetDefaultSession(ctx context.Context, userID string) string {
	ag := h.getDefaultAgent()
	if ag == nil {
		return "No agent running."
	}
	name := ag.Info().Name
	sessionID, err := ag.ResetSession(ctx, userID)
	if err != nil {
		log.Printf("[handler] reset session failed for %s: %v", userID, err)
		return fmt.Sprintf("Failed to reset session: %v", err)
	}
	if sessionID != "" {
		return fmt.Sprintf("已创建新的%s会话\n%s", name, sessionID)
	}
	return fmt.Sprintf("已创建新的%s会话", name)
}

// handleCwd handles the /cwd command. It updates the working directory for all running agents.
func (h *Handler) handleCwd(trimmed string) string {
	arg := strings.TrimSpace(strings.TrimPrefix(trimmed, "/cwd"))
	if arg == "" {
		// No path provided — show current cwd of default agent
		ag := h.getDefaultAgent()
		if ag == nil {
			return "No agent running."
		}
		info := ag.Info()
		return fmt.Sprintf("cwd: (check agent config)\nagent: %s", info.Name)
	}

	// Expand ~ to home directory
	if arg == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			arg = home
		}
	} else if strings.HasPrefix(arg, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			arg = filepath.Join(home, arg[2:])
		}
	}

	// Resolve to absolute path
	absPath, err := filepath.Abs(arg)
	if err != nil {
		return fmt.Sprintf("Invalid path: %v", err)
	}

	// Verify directory exists
	info, err := os.Stat(absPath)
	if err != nil {
		return fmt.Sprintf("Path not found: %s", absPath)
	}
	if !info.IsDir() {
		return fmt.Sprintf("Not a directory: %s", absPath)
	}

	// Update cwd on all running agents
	h.mu.RLock()
	agents := make(map[string]agent.Agent, len(h.agents))
	for name, ag := range h.agents {
		agents[name] = ag
	}
	h.mu.RUnlock()

	for name, ag := range agents {
		ag.SetCwd(absPath)
		log.Printf("[handler] updated cwd for agent %s: %s", name, absPath)
	}

	h.mu.Lock()
	for name := range agents {
		h.agentWorkDirs[name] = absPath
	}
	h.mu.Unlock()

	return fmt.Sprintf("cwd: %s", absPath)
}

// buildStatus returns a short status string showing the current default agent.
func (h *Handler) buildStatus() string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.defaultName == "" {
		return "agent: none (echo mode)"
	}

	ag, ok := h.agents[h.defaultName]
	if !ok {
		return fmt.Sprintf("agent: %s (not started)", h.defaultName)
	}

	info := ag.Info()
	return fmt.Sprintf("agent: %s\ntype: %s\nmodel: %s", h.defaultName, info.Type, info.Model)
}

func runtimeQueuePath() string {
	if override := strings.TrimSpace(os.Getenv("LONGCLAW_ROADMAP_QUEUE_FILE")); override != "" {
		return override
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".longclaw", "runtime-v2", "state", "roadmap-queue.json")
}

func nestedMap(data map[string]any, key string) map[string]any {
	value, ok := data[key]
	if !ok {
		return map[string]any{}
	}
	if mapped, ok := value.(map[string]any); ok {
		return mapped
	}
	return map[string]any{}
}

func stringValue(data map[string]any, key string, fallback string) string {
	value, ok := data[key]
	if !ok || value == nil {
		return fallback
	}
	if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
		return text
	}
	return fallback
}

func intValue(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	default:
		return 0
	}
}

func buildRuntimeStatusReply() string {
	path := runtimeQueuePath()
	if path == "" {
		return "runtime 状态暂不可用：无法定位 roadmap queue"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("runtime 状态暂不可用：%v", err)
	}

	var queue map[string]any
	if err := json.Unmarshal(data, &queue); err != nil {
		return fmt.Sprintf("runtime 状态暂不可用：queue 解析失败: %v", err)
	}

	routing := nestedMap(queue, "routing")
	taskQueue := nestedMap(queue, "task_queue")
	taskCounts := nestedMap(taskQueue, "counts")
	delivery := nestedMap(queue, "delivery_policy")
	summaryDelivery := nestedMap(delivery, "summary_delivery_result")

	lines := []string{
		"Longclaw Runtime",
		fmt.Sprintf("generated_at: %s", stringValue(queue, "generated_at", "unknown")),
		fmt.Sprintf("effective_agent: %s", stringValue(routing, "effective_agent", "unknown")),
		fmt.Sprintf(
			"primary/backup: %s / %s",
			stringValue(routing, "preferred_primary", "unknown"),
			stringValue(routing, "preferred_backup", "unknown"),
		),
		fmt.Sprintf("most_worth_watching: %s", stringValue(queue, "most_worth_watching", "unknown")),
		fmt.Sprintf("wechat_delivery_mode: %s", stringValue(delivery, "wechat_delivery_mode", "unknown")),
		fmt.Sprintf("summary_delivery_status: %s", stringValue(summaryDelivery, "status", "unknown")),
		fmt.Sprintf("pending_tasks: %d", intValue(taskCounts["pending"])),
		fmt.Sprintf("blocked_items: %d", len(asStringSlice(queue["blocked_items"]))),
		fmt.Sprintf("pending_reviews: %d", len(asStringSlice(queue["pending_reviews"]))),
	}
	return strings.Join(lines, "\n")
}

func asStringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
			result = append(result, text)
		}
	}
	return result
}

func buildHelpText() string {
	return `Available commands:
@agent or /agent - Switch default agent
@agent msg or /agent msg - Send to a specific agent
@a @b msg - Broadcast to multiple agents
任务: xxx - Queue a runtime task after the current self-repair round
/runtime - Show the latest Longclaw runtime status
/new or /clear - Start a new session
/cwd /path - Switch workspace directory
/info - Show current agent info
/help - Show this help message

Aliases: /cc(claude) /cx(codex) /cs(cursor) /km(kimi) /gm(gemini) /oc(openclaw) /ocd(opencode) /pi(pi) /cp(copilot) /dr(droid) /if(iflow) /kr(kiro) /qw(qwen)`
}

func extractText(msg ilink.WeixinMessage) string {
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeText && item.TextItem != nil {
			return item.TextItem.Text
		}
	}
	return ""
}

func extractImage(msg ilink.WeixinMessage) *ilink.ImageItem {
	for _, item := range msg.ItemList {
		if item.ImageItem != nil {
			return item.ImageItem
		}
	}
	return nil
}

func extractFile(msg ilink.WeixinMessage) *ilink.FileItem {
	for _, item := range msg.ItemList {
		if item.FileItem != nil {
			return item.FileItem
		}
	}
	return nil
}

func extractVoice(msg ilink.WeixinMessage) *ilink.VoiceItem {
	for _, item := range msg.ItemList {
		if item.VoiceItem != nil {
			return item.VoiceItem
		}
	}
	return nil
}

func extractVideo(msg ilink.WeixinMessage) *ilink.VideoItem {
	for _, item := range msg.ItemList {
		if item.VideoItem != nil {
			return item.VideoItem
		}
	}
	return nil
}

func extractVoiceText(msg ilink.WeixinMessage) string {
	for _, item := range msg.ItemList {
		if item.Type == ilink.ItemTypeVoice && item.VoiceItem != nil && item.VoiceItem.Text != "" {
			return item.VoiceItem.Text
		}
	}
	return ""
}

func (h *Handler) handleImageSave(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, img *ilink.ImageItem) {
	clientID := NewClientID()
	log.Printf("[handler] received image from %s, saving to %s", msg.FromUserID, h.saveDir)

	// Download image data
	var data []byte
	var err error

	if img.URL != "" {
		// Direct URL download
		data, _, err = downloadFile(ctx, img.URL)
	} else if img.Media != nil && img.Media.EncryptQueryParam != "" {
		// CDN encrypted download
		data, err = DownloadFileFromCDN(ctx, img.Media.EncryptQueryParam, img.Media.AESKey)
	} else {
		log.Printf("[handler] image has no URL or media info from %s", msg.FromUserID)
		return
	}

	if err != nil {
		log.Printf("[handler] failed to download image from %s: %v", msg.FromUserID, err)
		reply := fmt.Sprintf("Failed to save image: %v", err)
		h.sendStructuredTextReply(ctx, client, msg, h.getDefaultAgentName(), reply, clientID, replyKindError)
		return
	}

	// Detect extension from content
	ext := detectImageExt(data)

	// Generate a high-resolution base name; final collision avoidance happens at write time.
	ts := mediaTimestamp()
	fileName := fmt.Sprintf("%s%s", ts, ext)
	filePath, err := saveIncomingMediaFile(h.saveDir, fileName, data, obsidianarchive.Sidecar{
		Source:           "wechat",
		MediaType:        "image",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		RetentionDays:    7,
		ArchiveState:     obsidianarchive.ArchiveStateActive,
		Title:            strings.TrimSuffix(fileName, filepath.Ext(fileName)),
		OriginalFilename: fileName,
		WechatUserID:     msg.FromUserID,
	}, msg.MessageID)
	if err != nil {
		log.Printf("[handler] failed to persist image: %v", err)
		reply := fmt.Sprintf("Failed to save image: %v", err)
		h.sendStructuredTextReply(ctx, client, msg, h.getDefaultAgentName(), reply, clientID, replyKindError)
		return
	}

	log.Printf("[handler] saved image to %s (%d bytes)", filePath, len(data))
	reply := buildSavedMediaReply("图片", filepath.Base(filePath))
	h.sendStructuredTextReply(ctx, client, msg, h.getDefaultAgentName(), reply, clientID, replyKindSave)
	h.dispatchSavedMediaToDefaultAgent(ctx, client, msg, "图片", filePath)
}

func (h *Handler) handleFileSave(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, file *ilink.FileItem) {
	clientID := NewClientID()
	log.Printf("[handler] received file from %s, saving to %s", msg.FromUserID, h.saveDir)

	if file.Media == nil || file.Media.EncryptQueryParam == "" {
		log.Printf("[handler] file has no media info from %s", msg.FromUserID)
		h.sendStructuredTextReply(ctx, client, msg, h.getDefaultAgentName(), "Failed to save file: missing media info", clientID, replyKindError)
		return
	}

	data, err := DownloadFileFromCDN(ctx, file.Media.EncryptQueryParam, file.Media.AESKey)
	if err != nil {
		log.Printf("[handler] failed to download file from %s: %v", msg.FromUserID, err)
		h.sendStructuredTextReply(ctx, client, msg, h.getDefaultAgentName(), fmt.Sprintf("Failed to save file: %v", err), clientID, replyKindError)
		return
	}

	fileName := sanitizeIncomingFileName(file.FileName)
	filePath, err := saveIncomingMediaFile(h.saveDir, fileName, data, obsidianarchive.Sidecar{
		Source:           "wechat",
		MediaType:        "file",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		RetentionDays:    7,
		ArchiveState:     obsidianarchive.ArchiveStateActive,
		Title:            strings.TrimSuffix(filepath.Base(fileName), filepath.Ext(fileName)),
		OriginalFilename: fileName,
		WechatUserID:     msg.FromUserID,
	}, msg.MessageID)
	if err != nil {
		log.Printf("[handler] failed to persist file: %v", err)
		h.sendStructuredTextReply(ctx, client, msg, h.getDefaultAgentName(), fmt.Sprintf("Failed to save file: %v", err), clientID, replyKindError)
		return
	}

	log.Printf("[handler] saved file to %s (%d bytes)", filePath, len(data))
	h.sendStructuredTextReply(ctx, client, msg, h.getDefaultAgentName(), buildSavedMediaReply("文件", filepath.Base(filePath)), clientID, replyKindSave)
	h.dispatchSavedMediaToDefaultAgent(ctx, client, msg, "文件", filePath)
}

func (h *Handler) handleVoiceSave(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, voice *ilink.VoiceItem) (string, string, bool) {
	clientID := NewClientID()
	log.Printf("[handler] received voice from %s, saving to %s", msg.FromUserID, h.saveDir)
	if voice.Media == nil || voice.Media.EncryptQueryParam == "" {
		log.Printf("[handler] voice has no media info from %s", msg.FromUserID)
		return "", "", false
	}
	data, err := DownloadFileFromCDN(ctx, voice.Media.EncryptQueryParam, voice.Media.AESKey)
	if err != nil {
		log.Printf("[handler] failed to download voice from %s: %v", msg.FromUserID, err)
		h.sendStructuredTextReply(ctx, client, msg, h.getDefaultAgentName(), fmt.Sprintf("Failed to save voice: %v", err), clientID, replyKindError)
		return "", "", false
	}
	fileName := mediaTimestamp() + detectVoiceExt(voice)
	sidecar := obsidianarchive.Sidecar{
		Source:           "wechat",
		MediaType:        "voice",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		RetentionDays:    7,
		ArchiveState:     obsidianarchive.ArchiveStateActive,
		Title:            strings.TrimSuffix(fileName, filepath.Ext(fileName)),
		OriginalFilename: fileName,
		WechatUserID:     msg.FromUserID,
		Remark:           strings.TrimSpace(voice.Text),
	}
	filePath, err := saveIncomingMediaFile(h.saveDir, fileName, data, sidecar, msg.MessageID)
	if err != nil {
		log.Printf("[handler] failed to persist voice: %v", err)
		h.sendStructuredTextReply(ctx, client, msg, h.getDefaultAgentName(), fmt.Sprintf("Failed to save voice: %v", err), clientID, replyKindError)
		return "", "", false
	}
	log.Printf("[handler] saved voice to %s (%d bytes)", filePath, len(data))
	h.sendStructuredTextReply(ctx, client, msg, h.getDefaultAgentName(), buildSavedMediaReply("语音", filepath.Base(filePath)), clientID, replyKindSave)
	return filePath, strings.TrimSpace(voice.Text), true
}

func (h *Handler) handleVideoSave(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, video *ilink.VideoItem) bool {
	clientID := NewClientID()
	log.Printf("[handler] received video from %s, saving to %s", msg.FromUserID, h.saveDir)
	if video.Media == nil || video.Media.EncryptQueryParam == "" {
		log.Printf("[handler] video has no media info from %s", msg.FromUserID)
		return false
	}
	data, err := DownloadFileFromCDN(ctx, video.Media.EncryptQueryParam, video.Media.AESKey)
	if err != nil {
		log.Printf("[handler] failed to download video from %s: %v", msg.FromUserID, err)
		h.sendStructuredTextReply(ctx, client, msg, h.getDefaultAgentName(), fmt.Sprintf("Failed to save video: %v", err), clientID, replyKindError)
		return false
	}
	fileName := mediaTimestamp() + ".mp4"
	filePath, err := saveIncomingMediaFile(h.saveDir, fileName, data, obsidianarchive.Sidecar{
		Source:           "wechat",
		MediaType:        "video",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		RetentionDays:    7,
		ArchiveState:     obsidianarchive.ArchiveStateActive,
		Title:            strings.TrimSuffix(fileName, filepath.Ext(fileName)),
		OriginalFilename: fileName,
		WechatUserID:     msg.FromUserID,
	}, msg.MessageID)
	if err != nil {
		log.Printf("[handler] failed to persist video: %v", err)
		h.sendStructuredTextReply(ctx, client, msg, h.getDefaultAgentName(), fmt.Sprintf("Failed to save video: %v", err), clientID, replyKindError)
		return false
	}
	log.Printf("[handler] saved video to %s (%d bytes)", filePath, len(data))
	h.sendStructuredTextReply(ctx, client, msg, h.getDefaultAgentName(), buildSavedMediaReply("视频", filepath.Base(filePath)), clientID, replyKindSave)
	h.dispatchSavedMediaToDefaultAgent(ctx, client, msg, "视频", filePath)
	return true
}

func (h *Handler) dispatchSavedMediaToDefaultAgent(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, mediaType, path string) {
	prompt := fmt.Sprintf("收到一个来自微信的%s，已保存到本地。请基于该文件继续处理。\n%s", mediaType, path)
	h.dispatchSavedMediaPrompt(ctx, client, msg, mediaType, prompt)
}

func (h *Handler) dispatchSavedMediaPrompt(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, mediaType, prompt string) {
	_ = mediaType
	h.sendToDefaultAgent(ctx, client, msg, prompt, NewClientID())
}

func resolveVoiceInputMode(cfg *config.Config, transcript string, audioAvailable bool) string {
	mode := voiceModeTranscriptFirst
	if cfg != nil && strings.TrimSpace(cfg.VoiceInputModeDefault) != "" {
		mode = strings.ToLower(strings.TrimSpace(cfg.VoiceInputModeDefault))
	}
	switch mode {
	case voiceModeTranscriptFirst, voiceModeTranscriptPlusAudio, voiceModeAudioAnalysisRequest:
	default:
		mode = voiceModeTranscriptFirst
	}

	hasTranscript := strings.TrimSpace(transcript) != ""
	switch {
	case mode == voiceModeTranscriptFirst && !hasTranscript && audioAvailable:
		return voiceModeTranscriptPlusAudio
	case mode == voiceModeTranscriptPlusAudio && !audioAvailable && hasTranscript:
		return voiceModeTranscriptFirst
	case mode == voiceModeAudioAnalysisRequest && !audioAvailable && hasTranscript:
		return voiceModeTranscriptFirst
	default:
		return mode
	}
}

func buildVoiceAgentInput(transcript, audioPath, mode string) string {
	transcript = strings.TrimSpace(transcript)
	audioPath = strings.TrimSpace(audioPath)

	switch mode {
	case voiceModeAudioAnalysisRequest:
		if transcript != "" && audioPath != "" {
			return fmt.Sprintf("收到一条微信语音。请优先参考微信转写，并结合原始音频继续分析。\n\n微信转写：\n%s\n\n原始音频路径：\n%s", transcript, audioPath)
		}
		if audioPath != "" {
			return fmt.Sprintf("收到一条来自微信的语音，目前没有可用转写。请直接分析原始音频。\n%s", audioPath)
		}
	case voiceModeTranscriptPlusAudio:
		if transcript != "" && audioPath != "" {
			return fmt.Sprintf("以下内容来自微信语音转写：\n%s\n\n原始音频已保存到本地，可按需进一步分析：\n%s", transcript, audioPath)
		}
		if audioPath != "" {
			return fmt.Sprintf("收到一条来自微信的语音，目前没有可用转写。原始音频已保存到本地：\n%s", audioPath)
		}
	}

	if transcript != "" {
		return "以下内容来自微信语音转写：\n" + transcript
	}
	if audioPath != "" {
		return fmt.Sprintf("收到一条来自微信的语音，目前没有可用转写。原始音频已保存到本地：\n%s", audioPath)
	}
	return ""
}

func voiceModeExposesAudio(mode string) bool {
	return mode == voiceModeTranscriptPlusAudio || mode == voiceModeAudioAnalysisRequest
}

func saveIncomingMediaFile(dir, fileName string, data []byte, sidecar obsidianarchive.Sidecar, messageID int64) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	filePath, err := uniqueMediaPath(dir, fileName)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		return "", err
	}

	sidecarPath := filePath + ".sidecar.md"
	sidecar.ID = uuid.New().String()
	finalName := filepath.Base(filePath)
	originalBase := strings.TrimSuffix(filepath.Base(fileName), filepath.Ext(fileName))
	if sidecar.OriginalFilename == "" || sidecar.OriginalFilename == fileName {
		sidecar.OriginalFilename = finalName
	}
	if sidecar.Title == "" || sidecar.Title == originalBase {
		sidecar.Title = strings.TrimSuffix(finalName, filepath.Ext(finalName))
	}
	if err := obsidianarchive.SaveSidecar(sidecarPath, sidecar); err != nil {
		log.Printf("[handler] failed to write sidecar: %v", err)
	}

	if sidecar.Source == "wechat" {
		if err := obsidianarchive.RecordMedia(dir, sidecar.WechatUserID, messageID, sidecar, filePath); err != nil {
			log.Printf("[handler] failed to record media session item: %v", err)
		}
	}

	return filePath, nil
}

func buildSavedMediaReply(mediaType, fileName string) string {
	return fmt.Sprintf("已收到%s，已保存为 %s。正在继续处理。", mediaType, fileName)
}

func mediaTimestamp() string {
	return time.Now().Format("20060102-150405.000")
}

func uniqueMediaPath(dir, fileName string) (string, error) {
	base := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	ext := filepath.Ext(fileName)
	candidate := filepath.Join(dir, fileName)
	for i := 2; ; i++ {
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
		candidate = filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, i, ext))
	}
}

func sanitizeIncomingFileName(name string) string {
	name = strings.TrimSpace(filepath.Base(name))
	name = strings.ReplaceAll(name, string(os.PathSeparator), "_")
	name = strings.ReplaceAll(name, "/", "_")
	if name == "" || name == "." {
		name = mediaTimestamp() + ".bin"
	}
	return name
}

func detectVoiceExt(voice *ilink.VoiceItem) string {
	switch voice.EncodeType {
	case 5:
		return ".amr"
	case 6:
		return ".silk"
	case 7:
		return ".mp3"
	default:
		return ".audio"
	}
}

func describeMessageItems(msg ilink.WeixinMessage) string {
	if len(msg.ItemList) == 0 {
		return "[]"
	}

	parts := make([]string, 0, len(msg.ItemList))
	for idx, item := range msg.ItemList {
		parts = append(parts, fmt.Sprintf("{idx:%d type:%d text:%t image:%t voice:%t video:%t file:%t}",
			idx,
			item.Type,
			item.TextItem != nil,
			item.ImageItem != nil,
			item.VoiceItem != nil,
			item.VideoItem != nil,
			item.FileItem != nil,
		))
	}
	return "[" + strings.Join(parts, " ") + "]"
}

func detectImageExt(data []byte) string {
	if len(data) < 4 {
		return ".bin"
	}
	// PNG: 89 50 4E 47
	if data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47 {
		return ".png"
	}
	// JPEG: FF D8 FF
	if data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return ".jpg"
	}
	// GIF: 47 49 46
	if data[0] == 0x47 && data[1] == 0x49 && data[2] == 0x46 {
		return ".gif"
	}
	// WebP: 52 49 46 46 ... 57 45 42 50
	if len(data) >= 12 && data[0] == 0x52 && data[1] == 0x49 && data[8] == 0x57 && data[9] == 0x45 {
		return ".webp"
	}
	// BMP: 42 4D
	if data[0] == 0x42 && data[1] == 0x4D {
		return ".bmp"
	}
	return ".jpg" // default to jpg for WeChat images
}
