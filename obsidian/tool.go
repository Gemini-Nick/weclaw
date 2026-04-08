package obsidian

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
)

const (
	ArchiveToolName             = "obsidian_archive"
	TriggerReasonAgentTool      = "agent_tool"
	archiveToolFence            = "weclaw-obsidian-tool"
	defaultArchiveToolMode      = "formal"
	defaultArchiveSummary       = "本轮微信会话已归档到 Obsidian。"
	maxArchiveContextMessages   = 8
	maxArchiveContextMediaItems = 6
)

var archiveToolBlockPattern = regexp.MustCompile("(?s)```" + archiveToolFence + "\\s*(\\{.*?\\})\\s*```")

type ArchiveToolRequest struct {
	Tool               string   `json:"tool"`
	ConversationID     string   `json:"conversation_id"`
	Title              string   `json:"title"`
	Summary            string   `json:"summary"`
	MessageIDs         []int64  `json:"message_ids"`
	SelectedMediaPaths []string `json:"selected_media_paths"`
	NoteBody           string   `json:"note_body"`
	Mode               string   `json:"mode"`
}

type ArchiveToolResult struct {
	Invoked    bool
	Mode       string
	Title      string
	NotePath   string
	AssetPaths []string
	Reply      string
}

type ArchiveContext struct {
	UserID         string
	ConversationID string
	MessageIDs     []int64
	Messages       []SessionEntry
	Media          []MediaEntry
}

func BuildAgentArchivePrompt(cfg *config.Config, workspaceDir, userID, userText string) string {
	if cfg == nil || !cfg.ObsidianEnabled || !cfg.ArchiveToolEnabled || strings.TrimSpace(userText) == "" {
		return userText
	}

	ctx, err := LoadArchiveContext(workspaceDir, userID, cfg.ObsidianArchiveWindowMinutes)
	if err != nil {
		return userText
	}

	var sb strings.Builder
	sb.WriteString(strings.TrimSpace(userText))
	sb.WriteString("\n\n")
	sb.WriteString("附加系统指令：\n")
	sb.WriteString("- 如果用户明确要求“归档到 Obsidian / 收录知识库 / 沉淀到知识库”，由你决定是否归档。\n")
	sb.WriteString("- 一旦决定归档，不要直接写 Obsidian vault 文件，也不要在正文中暴露内部协议。\n")
	sb.WriteString("- 先给用户一条自然中文回复，然后在回复末尾追加唯一一个工具块。\n")
	sb.WriteString("- 工具块必须使用如下 fenced block，且 JSON 必须合法：\n")
	sb.WriteString("```")
	sb.WriteString(archiveToolFence)
	sb.WriteString("\n")
	sb.WriteString("{\"tool\":\"obsidian_archive\",\"mode\":\"formal\",\"conversation_id\":\"")
	sb.WriteString(ctx.ConversationID)
	sb.WriteString("\",\"title\":\"标题\",\"summary\":\"一句摘要\",\"message_ids\":[消息ID],\"selected_media_paths\":[\"仅可从下方媒体列表选择的绝对路径\"],\"note_body\":\"完整 markdown 正文\"}\n")
	sb.WriteString("```\n")
	sb.WriteString("- 只有在你确定需要归档时才输出工具块；普通问答不要输出。\n")
	sb.WriteString("- selected_media_paths 只能从“可用媒体”里选择；若本轮没有附件，可传空数组。\n")
	sb.WriteString("- mode 默认 formal；除非用户明确要求调试，否则不要写 debug。\n")
	sb.WriteString("- note_body 请直接输出最终要写入 Obsidian 的 markdown 正文，不要包含 frontmatter。\n")
	sb.WriteString("- 如果你没有调用工具，但用户明显在要求归档，请在正常回复里明确说明未执行归档。\n")
	sb.WriteString("\n当前归档上下文：\n")
	sb.WriteString(fmt.Sprintf("conversation_id: %s\n", ctx.ConversationID))
	if len(ctx.Messages) > 0 {
		sb.WriteString("最近消息：\n")
		start := 0
		if len(ctx.Messages) > maxArchiveContextMessages {
			start = len(ctx.Messages) - maxArchiveContextMessages
		}
		for _, entry := range ctx.Messages[start:] {
			label := "用户"
			if entry.Role == "agent" {
				label = entry.AgentName
				if strings.TrimSpace(label) == "" {
					label = "agent"
				}
			}
			text := truncateRunes(strings.TrimSpace(entry.Text), 120)
			sb.WriteString(fmt.Sprintf("- %s #%d: %s\n", label, entry.MessageID, text))
		}
	}
	if len(ctx.Media) > 0 {
		sb.WriteString("可用媒体：\n")
		media := ctx.Media
		if len(media) > maxArchiveContextMediaItems {
			media = media[:maxArchiveContextMediaItems]
		}
		for _, item := range media {
			line := fmt.Sprintf("- %s | %s", item.MediaType, item.Path)
			if strings.TrimSpace(item.Transcript) != "" {
				line += " | 转写: " + truncateRunes(strings.TrimSpace(item.Transcript), 80)
			}
			sb.WriteString(line + "\n")
		}
	} else {
		sb.WriteString("可用媒体：无\n")
	}

	return sb.String()
}

func LoadArchiveContext(workspaceDir, userID string, windowMinutes int) (ArchiveContext, error) {
	window, _, err := loadSessionWindow(workspaceDir, userID)
	if err != nil {
		return ArchiveContext{}, err
	}
	window = pruneWindow(window, windowMinutes)
	ctx := ArchiveContext{
		UserID:         userID,
		ConversationID: userID,
		Messages:       append([]SessionEntry(nil), window.Messages...),
		Media:          append([]MediaEntry(nil), window.Media...),
		MessageIDs:     collectMessageIDs(window),
	}
	return ctx, nil
}

func LikelyArchiveIntent(cfg *config.Config, text string) bool {
	if cfg == nil || !cfg.ObsidianEnabled || !cfg.ArchiveToolEnabled {
		return false
	}
	trimmed := strings.TrimSpace(strings.ToLower(text))
	if trimmed == "" {
		return false
	}
	targets := []string{"obsidian", "obsdian", "知识库"}
	verbs := []string{"归档", "收录", "沉淀", "存档", "保存", "加入", "归入", "放进"}
	targetHit := false
	for _, target := range targets {
		if strings.Contains(trimmed, strings.ToLower(target)) {
			targetHit = true
			break
		}
	}
	if !targetHit {
		return false
	}
	for _, verb := range verbs {
		if strings.Contains(trimmed, strings.ToLower(verb)) {
			return true
		}
	}
	return false
}

func ApplyAgentArchiveTool(cfg *config.Config, workspaceDir, userID, rawReply string) (ArchiveToolResult, error) {
	cleanReply, req, err := ParseArchiveToolRequest(rawReply)
	if err != nil {
		return ArchiveToolResult{}, err
	}
	if req == nil {
		return ArchiveToolResult{Reply: cleanReply}, nil
	}
	if cfg == nil || !cfg.ObsidianEnabled || !cfg.ArchiveToolEnabled {
		return ArchiveToolResult{}, fmt.Errorf("obsidian archive tool is disabled")
	}

	ctx, err := LoadArchiveContext(workspaceDir, userID, cfg.ObsidianArchiveWindowMinutes)
	if err != nil {
		return ArchiveToolResult{}, err
	}
	result, err := ExecuteArchiveTool(cfg, workspaceDir, ctx, *req)
	if err != nil {
		return ArchiveToolResult{}, err
	}

	reply := strings.TrimSpace(cleanReply)
	ack := fmt.Sprintf("已归档到 Obsidian：%s\n路径：%s", result.Title, result.NotePath)
	if reply == "" {
		reply = ack
	} else if !strings.Contains(reply, result.NotePath) {
		reply = strings.TrimSpace(reply) + "\n\n" + ack
	}
	result.Reply = reply
	result.Invoked = true
	return result, nil
}

func ParseArchiveToolRequest(rawReply string) (string, *ArchiveToolRequest, error) {
	reply := strings.TrimSpace(rawReply)
	matches := archiveToolBlockPattern.FindStringSubmatch(reply)
	if len(matches) == 0 {
		return reply, nil, nil
	}

	var req ArchiveToolRequest
	if err := json.Unmarshal([]byte(matches[1]), &req); err != nil {
		return "", nil, fmt.Errorf("parse archive tool request: %w", err)
	}
	if strings.TrimSpace(req.Tool) == "" {
		req.Tool = ArchiveToolName
	}
	if req.Tool != ArchiveToolName {
		return "", nil, fmt.Errorf("unsupported tool %q", req.Tool)
	}
	reply = strings.TrimSpace(archiveToolBlockPattern.ReplaceAllString(reply, ""))
	return reply, &req, nil
}

func ExecuteArchiveTool(cfg *config.Config, workspaceDir string, ctx ArchiveContext, req ArchiveToolRequest) (ArchiveToolResult, error) {
	if cfg == nil || !cfg.ObsidianEnabled {
		return ArchiveToolResult{}, fmt.Errorf("obsidian integration is disabled")
	}
	if !cfg.ArchiveToolEnabled {
		return ArchiveToolResult{}, fmt.Errorf("obsidian archive tool is disabled")
	}

	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = defaultArchiveToolMode
	}
	if mode == "formal" && !cfg.ObsidianFormalWriteEnabled {
		return ArchiveToolResult{}, fmt.Errorf("formal obsidian writes are disabled")
	}
	targetCfg := cfg
	if mode != "formal" {
		mode = "debug"
		targetCfg = WithDebugTarget(cfg)
	}
	if err := InitVault(targetCfg); err != nil {
		return ArchiveToolResult{}, err
	}

	messageIDs := filterMessageIDs(req.MessageIDs, ctx.MessageIDs)
	if len(messageIDs) == 0 {
		messageIDs = append([]int64(nil), ctx.MessageIDs...)
	}

	selectedMedia, err := selectArchiveMedia(ctx, req.SelectedMediaPaths)
	if err != nil {
		return ArchiveToolResult{}, err
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = inferArchiveTitle(ctx, req.Summary)
	}
	title = truncateRunes(title, 80)
	if title == "" {
		title = "微信会话归档 " + time.Now().Format("2006-01-02 15:04")
	}

	summary := strings.TrimSpace(req.Summary)
	if summary == "" {
		summary = inferArchiveSummary(ctx)
	}
	body := strings.TrimSpace(req.NoteBody)
	if body == "" {
		body = buildArchiveBodyFromContext(ctx, summary)
	}

	monthDir := monthSegment(time.Now())
	noteDir := filepath.Join(notesDir(targetCfg), monthDir)
	assetDir := filepath.Join(assetsDir(targetCfg), monthDir)
	if err := os.MkdirAll(noteDir, 0o755); err != nil {
		return ArchiveToolResult{}, err
	}
	if err := os.MkdirAll(assetDir, 0o755); err != nil {
		return ArchiveToolResult{}, err
	}

	var assetPaths []string
	for _, media := range selectedMedia {
		if media.Path == "" {
			continue
		}
		relAsset := ""
		if shouldArchiveMediaAsset(targetCfg, media.MediaType) {
			if _, err := os.Stat(media.Path); err != nil {
				return ArchiveToolResult{}, fmt.Errorf("missing selected media %s: %w", media.Path, err)
			}
			base := uniqueBasename(assetDir, filepath.Base(media.Path))
			dst := filepath.Join(assetDir, base)
			if err := copyFile(media.Path, dst); err != nil {
				return ArchiveToolResult{}, err
			}
			relAsset = filepath.ToSlash(filepath.Join(targetCfg.ObsidianAssetsDir, monthDir, base))
			assetPaths = append(assetPaths, relAsset)
		}
		sidecarPath := media.Path + ".sidecar.md"
		if sidecar, err := LoadSidecar(sidecarPath); err == nil {
			sidecar = normalizeSidecar(media.Path, sidecar)
			sidecar.ArchiveState = ArchiveStateArchivedObsidian
			sidecar.ObsidianAssetPath = relAsset
			_ = SaveSidecar(sidecarPath, sidecar)
		}
	}

	noteName := uniqueBasename(noteDir, sanitizeNoteName(title)+".md")
	relNote := filepath.ToSlash(filepath.Join(targetCfg.ObsidianNotesDir, monthDir, noteName))
	notePath := filepath.Join(noteDir, noteName)
	content := buildArchiveToolNote(title, summary, body, ctx.ConversationID, messageIDs, assetPaths, mode == "debug")
	if err := os.WriteFile(notePath, []byte(content), 0o644); err != nil {
		return ArchiveToolResult{}, err
	}

	for idx, media := range selectedMedia {
		sidecarPath := media.Path + ".sidecar.md"
		if sidecar, err := LoadSidecar(sidecarPath); err == nil {
			sidecar = normalizeSidecar(media.Path, sidecar)
			sidecar.ArchiveState = ArchiveStateArchivedObsidian
			sidecar.ObsidianNotePath = relNote
			if idx < len(assetPaths) {
				sidecar.ObsidianAssetPath = assetPaths[idx]
			}
			_ = SaveSidecar(sidecarPath, sidecar)
		}
	}

	if mode == "formal" {
		_ = os.Remove(sessionWindowPath(workspaceDir, ctx.UserID))
	}
	fmt.Fprintf(os.Stderr, "[obsidian] archive tool invoked mode=%s conversation_id=%s selected_media_count=%d note_path=%s asset_paths=%s\n", mode, ctx.ConversationID, len(selectedMedia), relNote, strings.Join(assetPaths, ","))
	if targetCfg.ObsidianOpenAfterArchive {
		_ = OpenNote(targetCfg, relNote)
	}
	return ArchiveToolResult{
		Invoked:    true,
		Mode:       mode,
		Title:      title,
		NotePath:   relNote,
		AssetPaths: assetPaths,
	}, nil
}

func buildArchiveToolNote(title, summary, body, conversationID string, messageIDs []int64, assetPaths []string, debug bool) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("title: " + yamlQuote(title) + "\n")
	sb.WriteString("source: wechat\n")
	sb.WriteString("status: inbox\n")
	sb.WriteString("archive_scope: session\n")
	sb.WriteString("archive_state: archived_obsidian\n")
	if debug {
		sb.WriteString("debug: true\n")
	} else {
		sb.WriteString("debug: false\n")
	}
	sb.WriteString("trigger_reason: " + yamlQuote(TriggerReasonAgentTool) + "\n")
	sb.WriteString("conversation_id: " + yamlQuote(conversationID) + "\n")
	sb.WriteString("created_at: " + yamlQuote(time.Now().UTC().Format(time.RFC3339)) + "\n")
	sb.WriteString("source_message_ids: " + yamlQuote(joinMessageIDs(messageIDs)) + "\n")
	sb.WriteString("asset_paths: " + yamlQuote(strings.Join(assetPaths, ",")) + "\n")
	sb.WriteString("archived_at: " + yamlQuote(time.Now().UTC().Format(time.RFC3339)) + "\n")
	sb.WriteString("---\n\n")
	sb.WriteString("## 摘要\n\n")
	if strings.TrimSpace(summary) == "" {
		sb.WriteString(defaultArchiveSummary + "\n\n")
	} else {
		sb.WriteString(strings.TrimSpace(summary) + "\n\n")
	}
	sb.WriteString(strings.TrimSpace(body))
	if len(assetPaths) > 0 {
		sb.WriteString("\n\n## 附件\n\n")
		for _, assetPath := range assetPaths {
			switch strings.ToLower(filepath.Ext(assetPath)) {
			case ".png", ".jpg", ".jpeg", ".gif", ".webp":
				sb.WriteString("![[")
				sb.WriteString(assetPath)
				sb.WriteString("]]\n\n")
			default:
				sb.WriteString("- [[")
				sb.WriteString(assetPath)
				sb.WriteString("]]\n")
			}
		}
	}
	return strings.TrimSpace(sb.String()) + "\n"
}

func selectArchiveMedia(ctx ArchiveContext, paths []string) ([]MediaEntry, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	allowed := make(map[string]MediaEntry, len(ctx.Media))
	for _, item := range ctx.Media {
		allowed[filepath.Clean(item.Path)] = item
	}
	var selected []MediaEntry
	seen := map[string]bool{}
	for _, path := range paths {
		clean := filepath.Clean(strings.TrimSpace(path))
		if clean == "" || seen[clean] {
			continue
		}
		item, ok := allowed[clean]
		if !ok {
			return nil, fmt.Errorf("selected media path is not in archive context: %s", clean)
		}
		seen[clean] = true
		selected = append(selected, item)
	}
	return selected, nil
}

func inferArchiveTitle(ctx ArchiveContext, summary string) string {
	if strings.TrimSpace(summary) != "" {
		return truncateRunes(strings.TrimSpace(summary), 40)
	}
	for _, msg := range ctx.Messages {
		if msg.Role == "user" && strings.TrimSpace(msg.Text) != "" {
			return truncateRunes(strings.TrimSpace(msg.Text), 40)
		}
	}
	return ""
}

func inferArchiveSummary(ctx ArchiveContext) string {
	if len(ctx.Media) == 0 && len(ctx.Messages) == 0 {
		return defaultArchiveSummary
	}
	var parts []string
	if len(ctx.Media) > 0 {
		var mediaTypes []string
		for _, item := range ctx.Media {
			mediaTypes = append(mediaTypes, item.MediaType)
		}
		parts = append(parts, "附件类型："+strings.Join(uniqueStrings(mediaTypes), "、")+"。")
	}
	for _, msg := range ctx.Messages {
		if msg.Role == "user" && strings.TrimSpace(msg.Text) != "" {
			parts = append(parts, "用户关注点："+truncateRunes(strings.TrimSpace(msg.Text), 80)+"。")
			break
		}
	}
	if len(parts) == 0 {
		return defaultArchiveSummary
	}
	return strings.Join(parts, " ")
}

func buildArchiveBodyFromContext(ctx ArchiveContext, summary string) string {
	var sb strings.Builder
	if strings.TrimSpace(summary) != "" {
		sb.WriteString(summary)
		sb.WriteString("\n\n")
	}
	sb.WriteString("## 会话摘录\n\n")
	for _, msg := range ctx.Messages {
		label := "用户"
		if msg.Role == "agent" {
			label = msg.AgentName
			if strings.TrimSpace(label) == "" {
				label = "agent"
			}
		}
		sb.WriteString("- ")
		sb.WriteString(label)
		sb.WriteString("：")
		sb.WriteString(strings.TrimSpace(msg.Text))
		sb.WriteString("\n")
	}
	if len(ctx.Messages) == 0 {
		sb.WriteString("- 本轮无文本会话，仅归档附件。\n")
	}
	return strings.TrimSpace(sb.String())
}

func filterMessageIDs(candidate, allowed []int64) []int64 {
	if len(candidate) == 0 || len(allowed) == 0 {
		return nil
	}
	allowedSet := make(map[int64]struct{}, len(allowed))
	for _, id := range allowed {
		allowedSet[id] = struct{}{}
	}
	var out []int64
	seen := map[int64]bool{}
	for _, id := range candidate {
		if _, ok := allowedSet[id]; !ok || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
