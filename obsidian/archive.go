package obsidian

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/google/uuid"
)

const DebugSubdir = "_debug"

const (
	ArchiveStateActive           = "active"
	ArchiveStatePendingObsidian  = "pending_obsidian"
	ArchiveStateArchivedObsidian = "archived_obsidian"
	ArchiveStateFailedObsidian   = "failed_obsidian"
	defaultRetentionDays         = 7
)

type Sidecar struct {
	ID                string
	Source            string
	MediaType         string
	CreatedAt         string
	RetentionDays     int
	ArchiveState      string
	ObsidianNotePath  string
	ObsidianAssetPath string
	Title             string
	Remark            string
	OriginalFilename  string
	WechatUserID      string
}

type SyncResult struct {
	Archived int
	Failed   int
	Skipped  int
}

type CleanupResult struct {
	DeletedExpired  int
	DeletedArchived int
	RetainedPending int
	RetainedFailed  int
}

func InitVault(cfg *config.Config) error {
	if !cfg.ObsidianEnabled {
		return nil
	}

	for _, dir := range []string{
		vaultDir(cfg),
		notesDir(cfg),
		assetsDir(cfg),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func MarkForObsidian(cfg *config.Config, mediaPath, title, remark string) (Sidecar, error) {
	mediaPath = expandHome(mediaPath)
	sidecarPath := mediaPath + ".sidecar.md"
	sidecar, err := LoadSidecar(sidecarPath)
	if err != nil {
		return Sidecar{}, err
	}
	sidecar = normalizeSidecar(mediaPath, sidecar)
	if sidecar.Source != "wechat" {
		return Sidecar{}, fmt.Errorf("unsupported source %q", sidecar.Source)
	}
	if title != "" {
		sidecar.Title = strings.TrimSpace(title)
	}
	if remark != "" {
		sidecar.Remark = strings.TrimSpace(remark)
	}
	if sidecar.Title == "" {
		sidecar.Title = strings.TrimSuffix(filepath.Base(mediaPath), filepath.Ext(mediaPath))
	}
	sidecar.ArchiveState = ArchiveStatePendingObsidian
	if err := SaveSidecar(sidecarPath, sidecar); err != nil {
		return Sidecar{}, err
	}
	if cfg != nil && cfg.ObsidianEnabled {
		_ = InitVault(cfg)
	}
	return sidecar, nil
}

func SyncPending(cfg *config.Config, workspaceDir string) (SyncResult, error) {
	if !cfg.ObsidianEnabled {
		return SyncResult{}, errors.New("obsidian integration is disabled")
	}
	if err := InitVault(cfg); err != nil {
		return SyncResult{}, err
	}

	sidecars, err := listSidecars(workspaceDir)
	if err != nil {
		return SyncResult{}, err
	}

	var result SyncResult
	for _, sidecarPath := range sidecars {
		sidecar, err := LoadSidecar(sidecarPath)
		if err != nil {
			result.Failed++
			continue
		}
		mediaPath := strings.TrimSuffix(sidecarPath, ".sidecar.md")
		sidecar = normalizeSidecar(mediaPath, sidecar)
		if sidecar.Source != "wechat" {
			result.Skipped++
			continue
		}
		if sidecar.ArchiveState != ArchiveStatePendingObsidian {
			result.Skipped++
			continue
		}

		if err := syncOne(cfg, mediaPath, sidecarPath, &sidecar); err != nil {
			sidecar.ArchiveState = ArchiveStateFailedObsidian
			_ = SaveSidecar(sidecarPath, sidecar)
			result.Failed++
			continue
		}
		result.Archived++
	}

	return result, nil
}

func CleanupWorkspace(workspaceDir string, now time.Time) (CleanupResult, error) {
	sidecars, err := listSidecars(workspaceDir)
	if err != nil {
		return CleanupResult{}, err
	}

	var result CleanupResult
	for _, sidecarPath := range sidecars {
		sidecar, err := LoadSidecar(sidecarPath)
		if err != nil {
			continue
		}
		mediaPath := strings.TrimSuffix(sidecarPath, ".sidecar.md")
		sidecar = normalizeSidecar(mediaPath, sidecar)
		if sidecar.Source != "wechat" {
			continue
		}

		switch sidecar.ArchiveState {
		case ArchiveStateArchivedObsidian:
			if err := deleteMediaAndSidecar(mediaPath, sidecarPath); err == nil {
				result.DeletedArchived++
			}
		case ArchiveStatePendingObsidian:
			result.RetainedPending++
		case ArchiveStateFailedObsidian:
			result.RetainedFailed++
		default:
			createdAt := parseTime(sidecar.CreatedAt)
			if createdAt.IsZero() {
				info, statErr := os.Stat(mediaPath)
				if statErr == nil {
					createdAt = info.ModTime()
				}
			}
			retentionDays := sidecar.RetentionDays
			if retentionDays <= 0 {
				retentionDays = defaultRetentionDays
			}
			if !createdAt.IsZero() && createdAt.Add(time.Duration(retentionDays)*24*time.Hour).Before(now) {
				if err := deleteMediaAndSidecar(mediaPath, sidecarPath); err == nil {
					result.DeletedExpired++
				}
			}
		}
	}

	return result, nil
}

func SummaryLine(sync SyncResult, cleanup CleanupResult) string {
	return fmt.Sprintf("obsidian archived=%d failed=%d skipped=%d cleaned_expired=%d cleaned_archived=%d retained_pending=%d retained_failed=%d",
		sync.Archived,
		sync.Failed,
		sync.Skipped,
		cleanup.DeletedExpired,
		cleanup.DeletedArchived,
		cleanup.RetainedPending,
		cleanup.RetainedFailed,
	)
}

func SummaryLineWithSessions(session SessionSyncResult, sync SyncResult, cleanup CleanupResult) string {
	summary := fmt.Sprintf("obsidian sessions_archived=%d sessions_failed=%d sessions_skipped=%d archived=%d failed=%d skipped=%d cleaned_expired=%d cleaned_archived=%d retained_pending=%d retained_failed=%d",
		session.SessionsArchived,
		session.SessionsFailed,
		session.SessionsSkipped,
		sync.Archived,
		sync.Failed,
		sync.Skipped,
		cleanup.DeletedExpired,
		cleanup.DeletedArchived,
		cleanup.RetainedPending,
		cleanup.RetainedFailed,
	)
	if session.LastNoteTitle != "" || session.LastNotePath != "" {
		summary += fmt.Sprintf(" last_note_title=%q last_note_path=%q", session.LastNoteTitle, session.LastNotePath)
	}
	return summary
}

func OpenNote(cfg *config.Config, notePath string) error {
	notePath = filepath.ToSlash(notePath)
	if cfg.ObsidianVaultName != "" {
		u := fmt.Sprintf("obsidian://open?vault=%s&file=%s", url.QueryEscape(cfg.ObsidianVaultName), url.QueryEscape(notePath))
		return exec.Command("open", u).Run()
	}
	return exec.Command("open", filepath.Join(vaultDir(cfg), notePath)).Run()
}

func WithDebugTarget(cfg *config.Config) *config.Config {
	if cfg == nil {
		return nil
	}
	clone := *cfg
	clone.ObsidianNotesDir = filepath.ToSlash(filepath.Join(cfg.ObsidianNotesDir, DebugSubdir))
	clone.ObsidianAssetsDir = filepath.ToSlash(filepath.Join(cfg.ObsidianAssetsDir, DebugSubdir))
	return &clone
}

func SaveSidecar(path string, sidecar Sidecar) error {
	if sidecar.ID == "" {
		sidecar.ID = uuid.New().String()
	}
	if sidecar.Source == "" {
		sidecar.Source = "wechat"
	}
	if sidecar.CreatedAt == "" {
		sidecar.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if sidecar.RetentionDays <= 0 {
		sidecar.RetentionDays = defaultRetentionDays
	}
	if sidecar.ArchiveState == "" {
		sidecar.ArchiveState = ArchiveStateActive
	}

	var lines []string
	lines = append(lines, "---")
	for _, kv := range []struct {
		k string
		v string
	}{
		{"id", sidecar.ID},
		{"source", sidecar.Source},
		{"media_type", sidecar.MediaType},
		{"created_at", sidecar.CreatedAt},
		{"retention_days", strconv.Itoa(sidecar.RetentionDays)},
		{"archive_state", sidecar.ArchiveState},
		{"obsidian_note_path", sidecar.ObsidianNotePath},
		{"obsidian_asset_path", sidecar.ObsidianAssetPath},
		{"title", sidecar.Title},
		{"remark", sidecar.Remark},
		{"original_filename", sidecar.OriginalFilename},
		{"wechat_user_id", sidecar.WechatUserID},
	} {
		if kv.v == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s: %s", kv.k, yamlQuote(kv.v)))
	}
	lines = append(lines, "---", "")
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

func LoadSidecar(path string) (Sidecar, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Sidecar{}, err
	}
	lines := strings.Split(string(data), "\n")
	var sidecar Sidecar
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || line == "---" || !strings.Contains(line, ":") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		key := strings.TrimSpace(parts[0])
		value := yamlUnquote(strings.TrimSpace(parts[1]))
		switch key {
		case "id":
			sidecar.ID = value
		case "source":
			sidecar.Source = value
		case "media_type":
			sidecar.MediaType = value
		case "created_at":
			sidecar.CreatedAt = value
		case "retention_days":
			sidecar.RetentionDays, _ = strconv.Atoi(value)
		case "archive_state":
			sidecar.ArchiveState = value
		case "obsidian_note_path":
			sidecar.ObsidianNotePath = value
		case "obsidian_asset_path":
			sidecar.ObsidianAssetPath = value
		case "title":
			sidecar.Title = value
		case "remark":
			sidecar.Remark = value
		case "original_filename":
			sidecar.OriginalFilename = value
		case "wechat_user_id":
			sidecar.WechatUserID = value
		}
	}
	if sidecar.RetentionDays <= 0 {
		sidecar.RetentionDays = defaultRetentionDays
	}
	if sidecar.ArchiveState == "" {
		sidecar.ArchiveState = ArchiveStateActive
	}
	return sidecar, nil
}

func syncOne(cfg *config.Config, mediaPath, sidecarPath string, sidecar *Sidecar) error {
	if _, err := os.Stat(mediaPath); err != nil {
		return err
	}

	monthDir := monthSegment(time.Now())
	noteDir := filepath.Join(notesDir(cfg), monthDir)
	assetDir := filepath.Join(assetsDir(cfg), monthDir)
	if err := os.MkdirAll(noteDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(assetDir, 0o755); err != nil {
		return err
	}

	relAsset := ""
	if shouldArchiveMediaAsset(cfg, sidecar.MediaType) {
		assetBase := uniqueBasename(assetDir, filepath.Base(mediaPath))
		destAsset := filepath.Join(assetDir, assetBase)
		if err := copyFile(mediaPath, destAsset); err != nil {
			return err
		}
		relAsset = filepath.ToSlash(filepath.Join(cfg.ObsidianAssetsDir, monthDir, assetBase))
	}

	noteTitle := sidecar.Title
	if noteTitle == "" {
		noteTitle = strings.TrimSuffix(filepath.Base(mediaPath), filepath.Ext(mediaPath))
	}
	noteName := uniqueBasename(noteDir, sanitizeNoteName(noteTitle)+".md")
	relNote := filepath.ToSlash(filepath.Join(cfg.ObsidianNotesDir, monthDir, noteName))
	notePath := filepath.Join(noteDir, noteName)

	if err := os.WriteFile(notePath, []byte(buildNoteContent(sidecar, mediaPath, relAsset)), 0o644); err != nil {
		return err
	}

	sidecar.ObsidianAssetPath = relAsset
	sidecar.ObsidianNotePath = relNote
	sidecar.ArchiveState = ArchiveStateArchivedObsidian
	if err := SaveSidecar(sidecarPath, *sidecar); err != nil {
		return err
	}
	if cfg.ObsidianOpenAfterArchive {
		_ = OpenNote(cfg, relNote)
	}
	return nil
}

func buildNoteContent(sidecar *Sidecar, mediaPath, relAsset string) string {
	title := sidecar.Title
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(mediaPath), filepath.Ext(mediaPath))
	}
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("title: " + yamlQuote(title) + "\n")
	sb.WriteString("source: wechat\n")
	sb.WriteString("status: inbox\n")
	sb.WriteString("archive_scope: media\n")
	sb.WriteString("archive_state: archived_obsidian\n")
	sb.WriteString("created_at: " + yamlQuote(sidecar.CreatedAt) + "\n")
	sb.WriteString("archived_at: " + yamlQuote(time.Now().UTC().Format(time.RFC3339)) + "\n")
	sb.WriteString("media_type: " + yamlQuote(sidecar.MediaType) + "\n")
	sb.WriteString("original_filename: " + yamlQuote(filepath.Base(mediaPath)) + "\n")
	sb.WriteString("wechat_user_id: " + yamlQuote(sidecar.WechatUserID) + "\n")
	sb.WriteString("conversation_id: " + yamlQuote(sidecar.WechatUserID) + "\n")
	if relAsset != "" {
		sb.WriteString("asset_path: " + yamlQuote(relAsset) + "\n")
		sb.WriteString("asset_paths: " + yamlQuote(relAsset) + "\n")
	}
	if sidecar.Remark != "" {
		sb.WriteString("remark: " + yamlQuote(sidecar.Remark) + "\n")
	}
	sb.WriteString("---\n\n")
	sb.WriteString("来自微信的媒体已归档到 Obsidian。\n\n")
	if relAsset == "" && sidecar.MediaType == "voice" {
		sb.WriteString("该语音仅保留转写，不归档原始音频附件。\n")
	} else if sidecar.MediaType == "image" {
		sb.WriteString("![[")
		sb.WriteString(relAsset)
		sb.WriteString("]]\n")
	} else if relAsset != "" {
		sb.WriteString("[[")
		sb.WriteString(relAsset)
		sb.WriteString("]]\n")
	}
	if sidecar.Remark != "" {
		sb.WriteString("\n备注：")
		sb.WriteString(sidecar.Remark)
		sb.WriteString("\n")
	}
	return sb.String()
}

func shouldArchiveMediaAsset(cfg *config.Config, mediaType string) bool {
	if mediaType != "voice" {
		return true
	}
	if cfg == nil {
		return false
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.ObsidianVoiceArchiveMode))
	return mode == "audio+transcript"
}

func normalizeSidecar(mediaPath string, sidecar Sidecar) Sidecar {
	if sidecar.Source == "" {
		sidecar.Source = "wechat"
	}
	if sidecar.MediaType == "" {
		sidecar.MediaType = inferMediaType(mediaPath)
	}
	if sidecar.Title == "" {
		sidecar.Title = strings.TrimSuffix(filepath.Base(mediaPath), filepath.Ext(mediaPath))
	}
	if sidecar.OriginalFilename == "" {
		sidecar.OriginalFilename = filepath.Base(mediaPath)
	}
	if sidecar.CreatedAt == "" {
		if info, err := os.Stat(mediaPath); err == nil {
			sidecar.CreatedAt = info.ModTime().UTC().Format(time.RFC3339)
		}
	}
	if sidecar.RetentionDays <= 0 {
		sidecar.RetentionDays = defaultRetentionDays
	}
	if sidecar.ArchiveState == "" {
		sidecar.ArchiveState = ArchiveStateActive
	}
	return sidecar
}

func inferMediaType(mediaPath string) string {
	switch strings.ToLower(filepath.Ext(mediaPath)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".heic", ".tiff":
		return "image"
	default:
		return "file"
	}
}

func listSidecars(workspaceDir string) ([]string, error) {
	pattern := filepath.Join(expandHome(workspaceDir), "*.sidecar.md")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	return matches, nil
}

func deleteMediaAndSidecar(mediaPath, sidecarPath string) error {
	_ = os.Remove(mediaPath)
	_ = os.Remove(sidecarPath)
	return nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

func vaultDir(cfg *config.Config) string {
	return expandHome(cfg.ObsidianVaultDir)
}

func notesDir(cfg *config.Config) string {
	return filepath.Join(vaultDir(cfg), filepath.FromSlash(cfg.ObsidianNotesDir))
}

func assetsDir(cfg *config.Config) string {
	return filepath.Join(vaultDir(cfg), filepath.FromSlash(cfg.ObsidianAssetsDir))
}

func uniqueBasename(dir, base string) string {
	base = sanitizeNoteName(base)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	if name == "" {
		name = "item"
	}
	candidate := name + ext
	for i := 1; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, candidate)); os.IsNotExist(err) {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d%s", name, i, ext)
	}
}

func sanitizeNoteName(name string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "\n", " ", "\r", " ", "\t", " ")
	name = strings.TrimSpace(replacer.Replace(name))
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "untitled"
	}
	return name
}

func yamlQuote(v string) string {
	v = strings.ReplaceAll(v, "\\", "\\\\")
	v = strings.ReplaceAll(v, "\"", "\\\"")
	return `"` + v + `"`
}

func yamlUnquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
		v = v[1 : len(v)-1]
	}
	v = strings.ReplaceAll(v, "\\\"", "\"")
	v = strings.ReplaceAll(v, "\\\\", "\\")
	return v
}

func parseTime(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, raw)
	return t
}

func expandHome(path string) string {
	if path == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	return path
}
