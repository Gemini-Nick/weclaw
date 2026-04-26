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
	"github.com/google/uuid"
)

const (
	ArchiveScopeSession       = "session"
	TriggerReasonExplicit     = "explicit"
	TriggerReasonAutoInferred = "auto_inferred"
)

type SessionEntry struct {
	MessageID int64  `json:"message_id"`
	Role      string `json:"role"`
	Kind      string `json:"kind"`
	Text      string `json:"text,omitempty"`
	AgentName string `json:"agent_name,omitempty"`
	CreatedAt string `json:"created_at"`
}

type MediaEntry struct {
	MessageID         int64  `json:"message_id"`
	MediaType         string `json:"media_type"`
	Path              string `json:"path"`
	OriginalFilename  string `json:"original_filename,omitempty"`
	Transcript        string `json:"transcript,omitempty"`
	ObsidianNotePath  string `json:"obsidian_note_path,omitempty"`
	ObsidianAssetPath string `json:"obsidian_asset_path,omitempty"`
	CreatedAt         string `json:"created_at"`
}

type SessionWindow struct {
	UserID             string         `json:"user_id"`
	CanonicalSessionID string         `json:"canonical_session_id,omitempty"`
	CanonicalUserID    string         `json:"canonical_user_id,omitempty"`
	ContextToken       string         `json:"context_token,omitempty"`
	UpdatedAt          string         `json:"updated_at"`
	Messages           []SessionEntry `json:"messages"`
	Media              []MediaEntry   `json:"media"`
}

type SessionArchiveTask struct {
	ID             string         `json:"id"`
	ArchiveScope   string         `json:"archive_scope"`
	ArchiveState   string         `json:"archive_state"`
	UserID         string         `json:"user_id"`
	ConversationID string         `json:"conversation_id"`
	TriggerText    string         `json:"trigger_text,omitempty"`
	TriggerReason  string         `json:"trigger_reason"`
	Title          string         `json:"title,omitempty"`
	NotePath       string         `json:"note_path,omitempty"`
	AssetPaths     []string       `json:"asset_paths,omitempty"`
	MessageIDs     []int64        `json:"message_ids,omitempty"`
	CreatedAt      string         `json:"created_at"`
	UpdatedAt      string         `json:"updated_at"`
	Messages       []SessionEntry `json:"messages"`
	Media          []MediaEntry   `json:"media"`
}

type SessionSyncResult struct {
	SessionsArchived int
	SessionsFailed   int
	SessionsSkipped  int
	LastNoteTitle    string
	LastNotePath     string
}

func RecordUserMessage(workspaceDir, userID string, messageID int64, text string) error {
	window, path, err := loadSessionWindow(workspaceDir, userID)
	if err != nil {
		return err
	}
	window.Messages = upsertSessionEntry(window.Messages, SessionEntry{
		MessageID: messageID,
		Role:      "user",
		Kind:      "text",
		Text:      strings.TrimSpace(text),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	window.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return saveJSON(path, window)
}

func UpdateSessionWindowMetadata(
	workspaceDir, userID, canonicalSessionID, canonicalUserID, contextToken string,
) error {
	window, path, err := loadSessionWindow(workspaceDir, userID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(canonicalSessionID) != "" {
		window.CanonicalSessionID = strings.TrimSpace(canonicalSessionID)
	}
	if strings.TrimSpace(canonicalUserID) != "" {
		window.CanonicalUserID = strings.TrimSpace(canonicalUserID)
	}
	if strings.TrimSpace(contextToken) != "" {
		window.ContextToken = strings.TrimSpace(contextToken)
	}
	window.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return saveJSON(path, window)
}

func RecordAgentReply(workspaceDir, userID string, messageID int64, agentName, text string) error {
	window, path, err := loadSessionWindow(workspaceDir, userID)
	if err != nil {
		return err
	}
	window.Messages = upsertSessionEntry(window.Messages, SessionEntry{
		MessageID: messageID,
		Role:      "agent",
		Kind:      "reply",
		Text:      strings.TrimSpace(text),
		AgentName: agentName,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	window.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return saveJSON(path, window)
}

func RecordMedia(workspaceDir, userID string, messageID int64, sidecar Sidecar, mediaPath string) error {
	window, path, err := loadSessionWindow(workspaceDir, userID)
	if err != nil {
		return err
	}
	window.Media = upsertMediaEntry(window.Media, MediaEntry{
		MessageID:        messageID,
		MediaType:        sidecar.MediaType,
		Path:             mediaPath,
		OriginalFilename: sidecar.OriginalFilename,
		Transcript:       sidecar.Remark,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
	})
	window.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return saveJSON(path, window)
}

func HasRecordedMessageID(workspaceDir, userID string, messageID int64) bool {
	if messageID == 0 {
		return false
	}
	window, _, err := loadSessionWindow(workspaceDir, userID)
	if err != nil {
		return false
	}
	for _, existing := range window.Messages {
		if existing.MessageID == messageID {
			return true
		}
	}
	for _, existing := range window.Media {
		if existing.MessageID == messageID {
			return true
		}
	}
	return false
}

func upsertSessionEntry(entries []SessionEntry, entry SessionEntry) []SessionEntry {
	key := sessionEntryKey(entry)
	for i, existing := range entries {
		if sessionEntryKey(existing) != key {
			continue
		}
		entries[i] = mergeSessionEntry(existing, entry)
		return entries
	}
	return append(entries, entry)
}

func upsertMediaEntry(entries []MediaEntry, entry MediaEntry) []MediaEntry {
	key := mediaEntryKey(entry)
	for i, existing := range entries {
		if mediaEntryKey(existing) != key {
			continue
		}
		entries[i] = mergeMediaEntry(existing, entry)
		return entries
	}
	return append(entries, entry)
}

func sessionEntryKey(entry SessionEntry) string {
	if entry.MessageID != 0 {
		if entry.Role == "agent" {
			return fmt.Sprintf("msg:%d|role:%s|kind:%s|agent:%s", entry.MessageID, entry.Role, entry.Kind, entry.AgentName)
		}
		return fmt.Sprintf("msg:%d|role:%s|kind:%s", entry.MessageID, entry.Role, entry.Kind)
	}
	return fmt.Sprintf("msg:0|role:%s|kind:%s|agent:%s|text:%s", entry.Role, entry.Kind, entry.AgentName, strings.TrimSpace(entry.Text))
}

func mediaEntryKey(entry MediaEntry) string {
	if entry.MessageID != 0 {
		return fmt.Sprintf("msg:%d|type:%s|path:%s", entry.MessageID, entry.MediaType, entry.Path)
	}
	return fmt.Sprintf("msg:0|type:%s|path:%s|name:%s", entry.MediaType, entry.Path, entry.OriginalFilename)
}

func mergeSessionEntry(prev, next SessionEntry) SessionEntry {
	if strings.TrimSpace(next.Text) != "" {
		prev.Text = next.Text
	}
	if next.AgentName != "" {
		prev.AgentName = next.AgentName
	}
	if next.CreatedAt != "" {
		prev.CreatedAt = next.CreatedAt
	}
	return prev
}

func mergeMediaEntry(prev, next MediaEntry) MediaEntry {
	if next.OriginalFilename != "" {
		prev.OriginalFilename = next.OriginalFilename
	}
	if next.Transcript != "" {
		prev.Transcript = next.Transcript
	}
	if next.ObsidianNotePath != "" {
		prev.ObsidianNotePath = next.ObsidianNotePath
	}
	if next.ObsidianAssetPath != "" {
		prev.ObsidianAssetPath = next.ObsidianAssetPath
	}
	if next.CreatedAt != "" {
		prev.CreatedAt = next.CreatedAt
	}
	return prev
}

func QueueSessionArchive(cfg *config.Config, workspaceDir, userID, triggerText, triggerReason string) (SessionArchiveTask, string, error) {
	window, windowPath, err := loadSessionWindow(workspaceDir, userID)
	if err != nil {
		return SessionArchiveTask{}, "", err
	}
	window = pruneWindow(window, cfg.ObsidianArchiveWindowMinutes)
	if len(window.Media) == 0 && triggerReason == TriggerReasonExplicit {
		window.Media = append(window.Media, loadRecentMediaForUser(workspaceDir, userID, cfg.ObsidianArchiveWindowMinutes)...)
		window.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if len(window.Messages) == 0 && len(window.Media) == 0 {
		return SessionArchiveTask{}, "", fmt.Errorf("no recent conversation to archive")
	}

	taskID := sessionTaskID(window)
	taskPath := filepath.Join(tasksDir(workspaceDir), taskID+".json")
	if existing, err := loadTask(taskPath); err == nil {
		switch existing.ArchiveState {
		case ArchiveStatePendingObsidian:
			return existing, "pending", nil
		case ArchiveStateArchivedObsidian:
			return existing, "archived", nil
		}
	}

	task := SessionArchiveTask{
		ID:             taskID,
		ArchiveScope:   ArchiveScopeSession,
		ArchiveState:   ArchiveStatePendingObsidian,
		UserID:         userID,
		ConversationID: userID,
		TriggerText:    strings.TrimSpace(triggerText),
		TriggerReason:  triggerReason,
		Title:          generateArchiveTitle(window, triggerText),
		MessageIDs:     collectMessageIDs(window),
		Messages:       append([]SessionEntry(nil), window.Messages...),
		Media:          append([]MediaEntry(nil), window.Media...),
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	if err := os.MkdirAll(tasksDir(workspaceDir), 0o755); err != nil {
		return SessionArchiveTask{}, "", err
	}
	if err := saveJSON(taskPath, task); err != nil {
		return SessionArchiveTask{}, "", err
	}
	fmt.Fprintf(os.Stderr, "[obsidian] archive trigger matched trigger_reason=%s conversation_id=%s selected_media_count=%d title=%q\n", triggerReason, task.ConversationID, len(task.Media), task.Title)
	_ = os.Remove(windowPath)
	return task, "queued", nil
}

func SyncPendingSessions(cfg *config.Config, workspaceDir string) (SessionSyncResult, error) {
	if !cfg.ObsidianEnabled {
		return SessionSyncResult{}, nil
	}
	if err := InitVault(cfg); err != nil {
		return SessionSyncResult{}, err
	}
	entries, err := os.ReadDir(tasksDir(workspaceDir))
	if err != nil {
		if os.IsNotExist(err) {
			return SessionSyncResult{}, nil
		}
		return SessionSyncResult{}, err
	}

	var result SessionSyncResult
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(tasksDir(workspaceDir), entry.Name())
		task, err := loadTask(path)
		if err != nil {
			result.SessionsFailed++
			continue
		}
		if task.ArchiveState != ArchiveStatePendingObsidian || task.ArchiveScope != ArchiveScopeSession {
			result.SessionsSkipped++
			continue
		}
		if err := syncSessionTask(cfg, workspaceDir, path, &task); err != nil {
			task.ArchiveState = ArchiveStateFailedObsidian
			task.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			_ = saveJSON(path, task)
			result.SessionsFailed++
			continue
		}
		result.SessionsArchived++
		result.LastNoteTitle = task.Title
		result.LastNotePath = task.NotePath
		fmt.Fprintf(os.Stderr, "[obsidian] task archived conversation_id=%s note_path=%s asset_paths=%s source_message_ids=%v\n", task.ConversationID, task.NotePath, strings.Join(task.AssetPaths, ","), task.MessageIDs)
	}

	return result, nil
}

func syncSessionTask(cfg *config.Config, workspaceDir, path string, task *SessionArchiveTask) error {
	monthDir := monthSegment(time.Now())
	noteDir := filepath.Join(notesDir(cfg), monthDir)
	assetDir := filepath.Join(assetsDir(cfg), monthDir)
	if err := os.MkdirAll(noteDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(assetDir, 0o755); err != nil {
		return err
	}

	var relAssets []string
	for idx := range task.Media {
		media := &task.Media[idx]
		if media.Path == "" {
			continue
		}
		if shouldArchiveMediaAsset(cfg, media.MediaType) {
			if _, err := os.Stat(media.Path); err != nil {
				return err
			}
			base := uniqueBasename(assetDir, filepath.Base(media.Path))
			dst := filepath.Join(assetDir, base)
			if err := copyFile(media.Path, dst); err != nil {
				return err
			}
			relAsset := filepath.ToSlash(filepath.Join(cfg.ObsidianAssetsDir, monthDir, base))
			relAssets = append(relAssets, relAsset)
			media.ObsidianAssetPath = relAsset
		}

		sidecarPath := media.Path + ".sidecar.md"
		if sidecar, err := LoadSidecar(sidecarPath); err == nil {
			sidecar = normalizeSidecar(media.Path, sidecar)
			sidecar.ArchiveState = ArchiveStateArchivedObsidian
			sidecar.ObsidianAssetPath = media.ObsidianAssetPath
			_ = SaveSidecar(sidecarPath, sidecar)
		}
	}

	noteName := uniqueBasename(noteDir, sanitizeNoteName(task.Title)+".md")
	relNote := filepath.ToSlash(filepath.Join(cfg.ObsidianNotesDir, monthDir, noteName))
	notePath := filepath.Join(noteDir, noteName)
	task.NotePath = relNote
	task.AssetPaths = relAssets
	if err := os.WriteFile(notePath, []byte(buildSessionNote(task)), 0o644); err != nil {
		return err
	}
	task.ArchiveState = ArchiveStateArchivedObsidian
	task.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := saveJSON(path, task); err != nil {
		return err
	}
	for _, media := range task.Media {
		sidecarPath := media.Path + ".sidecar.md"
		if sidecar, err := LoadSidecar(sidecarPath); err == nil {
			sidecar = normalizeSidecar(media.Path, sidecar)
			sidecar.ArchiveState = ArchiveStateArchivedObsidian
			sidecar.ObsidianNotePath = relNote
			if media.ObsidianAssetPath != "" {
				sidecar.ObsidianAssetPath = media.ObsidianAssetPath
			}
			_ = SaveSidecar(sidecarPath, sidecar)
		}
	}
	if cfg.ObsidianOpenAfterArchive {
		_ = OpenNote(cfg, relNote)
	}
	return nil
}

func buildSessionNote(task *SessionArchiveTask) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("title: " + yamlQuote(task.Title) + "\n")
	sb.WriteString("source: wechat\n")
	sb.WriteString("status: inbox\n")
	sb.WriteString("archive_scope: session\n")
	sb.WriteString("archive_state: archived_obsidian\n")
	sb.WriteString("debug: false\n")
	sb.WriteString("conversation_id: " + yamlQuote(task.ConversationID) + "\n")
	sb.WriteString("trigger_reason: " + yamlQuote(task.TriggerReason) + "\n")
	sb.WriteString("created_at: " + yamlQuote(task.CreatedAt) + "\n")
	sb.WriteString("source_message_ids: " + yamlQuote(joinMessageIDs(task.MessageIDs)) + "\n")
	sb.WriteString("asset_paths: " + yamlQuote(strings.Join(task.AssetPaths, ",")) + "\n")
	sb.WriteString("archived_at: " + yamlQuote(time.Now().UTC().Format(time.RFC3339)) + "\n")
	sb.WriteString("---\n\n")
	sb.WriteString("## 归档意图\n\n")
	if strings.TrimSpace(task.TriggerText) != "" {
		sb.WriteString(task.TriggerText + "\n\n")
	} else {
		sb.WriteString("自动识别为可沉淀到知识库的会话。\n\n")
	}
	sb.WriteString("## 会话摘要\n\n")
	sb.WriteString(buildSessionSummary(task) + "\n\n")
	sb.WriteString("## 关键对话\n\n")
	for _, msg := range task.Messages {
		switch msg.Role {
		case "user":
			sb.WriteString("- 用户：")
		default:
			label := "Agent"
			if msg.AgentName != "" {
				label = msg.AgentName
			}
			sb.WriteString("- " + label + "：")
		}
		sb.WriteString(strings.TrimSpace(msg.Text) + "\n")
	}
	if len(task.Media) > 0 {
		sb.WriteString("\n## 附件\n\n")
		for _, media := range task.Media {
			switch media.MediaType {
			case "image":
				sb.WriteString("![[")
				sb.WriteString(media.ObsidianAssetPath)
				sb.WriteString("]]\n\n")
			case "voice":
				if strings.TrimSpace(media.ObsidianAssetPath) != "" {
					sb.WriteString("- 语音附件：[[")
					sb.WriteString(media.ObsidianAssetPath)
					sb.WriteString("]]\n")
				} else {
					sb.WriteString("- 语音附件：未归档原始音频，仅保留转写\n")
				}
				if strings.TrimSpace(media.Transcript) != "" {
					sb.WriteString("  - 转写：")
					sb.WriteString(strings.TrimSpace(media.Transcript))
					sb.WriteString("\n")
				}
			case "video":
				if strings.TrimSpace(media.ObsidianAssetPath) != "" {
					sb.WriteString("- 视频附件：[[")
					sb.WriteString(media.ObsidianAssetPath)
					sb.WriteString("]]\n")
					sb.WriteString("  - 说明：保留原始视频，详情见上方会话摘要。\n")
				}
			default:
				if strings.TrimSpace(media.ObsidianAssetPath) != "" {
					sb.WriteString("- 文件附件：[[")
					sb.WriteString(media.ObsidianAssetPath)
					sb.WriteString("]]\n")
				}
			}
		}
	}
	return sb.String()
}

func buildSessionSummary(task *SessionArchiveTask) string {
	var parts []string
	if len(task.Media) > 0 {
		types := make([]string, 0, len(task.Media))
		for _, media := range task.Media {
			types = append(types, media.MediaType)
		}
		parts = append(parts, "本轮会话包含媒体："+strings.Join(uniqueStrings(types), "、")+"。")
	}
	if len(task.Messages) > 0 {
		firstUser := ""
		lastAgent := ""
		for _, msg := range task.Messages {
			if firstUser == "" && msg.Role == "user" && strings.TrimSpace(msg.Text) != "" {
				firstUser = strings.TrimSpace(msg.Text)
			}
			if msg.Role == "agent" && strings.TrimSpace(msg.Text) != "" {
				lastAgent = strings.TrimSpace(msg.Text)
			}
		}
		if firstUser != "" {
			parts = append(parts, "用户主要诉求："+truncateRunes(firstUser, 80)+"。")
		}
		if lastAgent != "" {
			parts = append(parts, "最近一次 agent 回复："+truncateRunes(lastAgent, 100)+"。")
		}
	}
	if len(parts) == 0 {
		return "本轮微信会话已归档。"
	}
	return strings.Join(parts, " ")
}

func DetectArchiveIntent(cfg *config.Config, text string, hasRecentMedia bool) (string, bool) {
	if !cfg.ObsidianEnabled || !cfg.ObsidianAutoArchiveEnabled {
		return "", false
	}
	trimmed := strings.TrimSpace(strings.ToLower(text))
	if trimmed == "" {
		return "", false
	}
	explicitKeywords := []string{"归档到知识库", "收录到知识库", "加入 obsidian", "加入obsidian", "存到知识库", "收录到 obsidian", "沉淀到知识库"}
	for _, keyword := range explicitKeywords {
		if strings.Contains(trimmed, strings.ToLower(keyword)) {
			return TriggerReasonExplicit, true
		}
	}
	if strings.Contains(trimmed, "知识库") && (strings.Contains(trimmed, "归档") || strings.Contains(trimmed, "收录") || strings.Contains(trimmed, "存到") || strings.Contains(trimmed, "存档") || strings.Contains(trimmed, "保存")) {
		return TriggerReasonExplicit, true
	}
	if strings.Contains(trimmed, "obsidian") && (strings.Contains(trimmed, "归档") || strings.Contains(trimmed, "加入") || strings.Contains(trimmed, "存")) {
		return TriggerReasonExplicit, true
	}
	if cfg.ObsidianAutoArchiveMode != "hybrid" {
		return "", false
	}
	if !hasRecentMedia {
		return "", false
	}
	autoKeywords := []string{"知识库", "沉淀", "留档", "后续参考", "方案保留", "后面复用", "归档"}
	score := 0
	for _, keyword := range autoKeywords {
		if strings.Contains(trimmed, strings.ToLower(keyword)) {
			score++
		}
	}
	if score >= 2 {
		return TriggerReasonAutoInferred, true
	}
	return "", false
}

func RecentMediaExists(workspaceDir, userID string, windowMinutes int) bool {
	window, _, err := loadSessionWindow(workspaceDir, userID)
	if err != nil {
		return false
	}
	window = pruneWindow(window, windowMinutes)
	return len(window.Media) > 0
}

func loadSessionWindow(workspaceDir, userID string) (SessionWindow, string, error) {
	path := sessionWindowPath(workspaceDir, userID)
	window := SessionWindow{UserID: userID}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return window, path, nil
		}
		return SessionWindow{}, path, err
	}
	if err := json.Unmarshal(data, &window); err != nil {
		return SessionWindow{}, path, err
	}
	if window.UserID == "" {
		window.UserID = userID
	}
	window.Messages = normalizeSessionEntries(window.Messages)
	window.Media = normalizeMediaEntries(window.Media)
	return window, path, nil
}

func loadTask(path string) (SessionArchiveTask, error) {
	var task SessionArchiveTask
	data, err := os.ReadFile(path)
	if err != nil {
		return task, err
	}
	err = json.Unmarshal(data, &task)
	return task, err
}

func saveJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func sessionRoot(workspaceDir string) string {
	return filepath.Join(expandHome(workspaceDir), ".obsidian")
}

func sessionWindowPath(workspaceDir, userID string) string {
	return filepath.Join(sessionRoot(workspaceDir), "sessions", safeUserSegment(userID)+".json")
}

func tasksDir(workspaceDir string) string {
	return filepath.Join(sessionRoot(workspaceDir), "tasks")
}

func safeUserSegment(userID string) string {
	re := regexp.MustCompile(`[^a-zA-Z0-9._-]+`)
	cleaned := re.ReplaceAllString(userID, "_")
	if cleaned == "" {
		return "user"
	}
	return cleaned
}

func pruneWindow(window SessionWindow, windowMinutes int) SessionWindow {
	if windowMinutes <= 0 {
		return window
	}
	cutoff := time.Now().Add(-time.Duration(windowMinutes) * time.Minute)
	var msgs []SessionEntry
	for _, msg := range window.Messages {
		if t := parseTime(msg.CreatedAt); t.IsZero() || !t.Before(cutoff) {
			msgs = append(msgs, msg)
		}
	}
	var media []MediaEntry
	for _, item := range window.Media {
		if t := parseTime(item.CreatedAt); t.IsZero() || !t.Before(cutoff) {
			media = append(media, item)
		}
	}
	window.Messages = msgs
	window.Media = media
	window.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return window
}

func sessionTaskID(window SessionWindow) string {
	lastID := int64(0)
	for _, id := range collectMessageIDs(window) {
		if id > lastID {
			lastID = id
		}
	}
	if lastID == 0 {
		return uuid.New().String()
	}
	return fmt.Sprintf("%s-%d", safeUserSegment(window.UserID), lastID)
}

func normalizeSessionEntries(entries []SessionEntry) []SessionEntry {
	out := make([]SessionEntry, 0, len(entries))
	for _, entry := range entries {
		out = upsertSessionEntry(out, entry)
	}
	return out
}

func normalizeMediaEntries(entries []MediaEntry) []MediaEntry {
	out := make([]MediaEntry, 0, len(entries))
	for _, entry := range entries {
		out = upsertMediaEntry(out, entry)
	}
	return out
}

func collectMessageIDs(window SessionWindow) []int64 {
	uniq := map[int64]struct{}{}
	for _, msg := range window.Messages {
		if msg.MessageID != 0 {
			uniq[msg.MessageID] = struct{}{}
		}
	}
	for _, media := range window.Media {
		if media.MessageID != 0 {
			uniq[media.MessageID] = struct{}{}
		}
	}
	var ids []int64
	for id := range uniq {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func generateArchiveTitle(window SessionWindow, triggerText string) string {
	cleaned := strings.TrimSpace(triggerText)
	replacements := []string{"归档到知识库", "收录到知识库", "加入 Obsidian", "加入Obsidian", "存到知识库", "这是", "请", "，", ",", "。"}
	for _, repl := range replacements {
		cleaned = strings.ReplaceAll(cleaned, repl, "")
	}
	cleaned = strings.TrimSpace(cleaned)
	if cleaned != "" {
		return truncateRunes(cleaned, 40)
	}
	for _, msg := range window.Messages {
		if msg.Role == "user" && strings.TrimSpace(msg.Text) != "" {
			return truncateRunes(strings.TrimSpace(msg.Text), 40)
		}
	}
	return "微信会话归档 " + time.Now().Format("2006-01-02 15:04")
}

func monthSegment(t time.Time) string {
	return t.Format("2006-01")
}

func truncateRunes(s string, max int) string {
	rs := []rune(strings.TrimSpace(s))
	if len(rs) <= max {
		return string(rs)
	}
	return string(rs[:max]) + "..."
}

func uniqueStrings(items []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, item := range items {
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

func joinMessageIDs(ids []int64) string {
	if len(ids) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf("%d", id))
	}
	return strings.Join(parts, ",")
}

func loadRecentMediaForUser(workspaceDir, userID string, windowMinutes int) []MediaEntry {
	sidecars, err := listSidecars(workspaceDir)
	if err != nil {
		return nil
	}
	cutoff := time.Now().Add(-time.Duration(windowMinutes) * time.Minute)
	var media []MediaEntry
	for _, sidecarPath := range sidecars {
		sidecar, err := LoadSidecar(sidecarPath)
		if err != nil {
			continue
		}
		mediaPath := strings.TrimSuffix(sidecarPath, ".sidecar.md")
		sidecar = normalizeSidecar(mediaPath, sidecar)
		if sidecar.Source != "wechat" || sidecar.WechatUserID != userID {
			continue
		}
		createdAt := parseTime(sidecar.CreatedAt)
		if !createdAt.IsZero() && createdAt.Before(cutoff) {
			continue
		}
		media = append(media, MediaEntry{
			MediaType:         sidecar.MediaType,
			Path:              mediaPath,
			OriginalFilename:  sidecar.OriginalFilename,
			Transcript:        sidecar.Remark,
			ObsidianNotePath:  sidecar.ObsidianNotePath,
			ObsidianAssetPath: sidecar.ObsidianAssetPath,
			CreatedAt:         sidecar.CreatedAt,
		})
	}
	sort.Slice(media, func(i, j int) bool {
		return parseTime(media[i].CreatedAt).After(parseTime(media[j].CreatedAt))
	})
	return media
}
