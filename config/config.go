package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
)

// Config holds the application configuration.
type Config struct {
	DefaultAgent                 string                 `json:"default_agent"`
	APIAddr                      string                 `json:"api_addr,omitempty"`
	AgentOSBaseURL               string                 `json:"agent_os_base_url,omitempty"`
	AgentOSAPIKey                string                 `json:"agent_os_api_key,omitempty"`
	AgentOSLaunchPolicy          string                 `json:"agent_os_launch_policy,omitempty"`
	CanonicalUserID              string                 `json:"canonical_user_id,omitempty"`
	DefaultLaunchPack            string                 `json:"default_launch_pack,omitempty"`
	DefaultLaunchCapability      string                 `json:"default_launch_capability,omitempty"`
	SaveDir                      string                 `json:"save_dir,omitempty"`
	PersonaDir                   string                 `json:"persona_dir,omitempty"`
	VoiceInputModeDefault        string                 `json:"voice_input_mode_default,omitempty"`
	ArchiveToolEnabled           bool                   `json:"archive_tool_enabled,omitempty"`
	ObsidianEnabled              bool                   `json:"obsidian_enabled,omitempty"`
	ObsidianFormalWriteEnabled   bool                   `json:"obsidian_formal_write_enabled,omitempty"`
	ObsidianVaultDir             string                 `json:"obsidian_vault_dir,omitempty"`
	ObsidianVaultName            string                 `json:"obsidian_vault_name,omitempty"`
	ObsidianNotesDir             string                 `json:"obsidian_notes_dir,omitempty"`
	ObsidianAssetsDir            string                 `json:"obsidian_assets_dir,omitempty"`
	ObsidianOpenAfterArchive     bool                   `json:"obsidian_open_after_archive,omitempty"`
	ObsidianAutoArchiveEnabled   bool                   `json:"obsidian_auto_archive_enabled,omitempty"`
	ObsidianAutoArchiveMode      string                 `json:"obsidian_auto_archive_mode,omitempty"`
	ObsidianArchiveWindowMinutes int                    `json:"obsidian_archive_window_minutes,omitempty"`
	ObsidianArchiveReplyEnabled  bool                   `json:"obsidian_archive_reply_enabled,omitempty"`
	ObsidianVoiceArchiveMode     string                 `json:"obsidian_voice_archive_mode,omitempty"`
	ObsidianVideoArchiveMode     string                 `json:"obsidian_video_archive_mode,omitempty"`
	AgentInputPolicy             string                 `json:"agent_input_policy,omitempty"`
	Agents                       map[string]AgentConfig `json:"agents"`
}

// AgentConfig holds configuration for a single agent.
type AgentConfig struct {
	Type         string            `json:"type"`                    // "acp", "cli", or "http"
	Command      string            `json:"command,omitempty"`       // binary path (cli/acp type)
	Args         []string          `json:"args,omitempty"`          // extra args for command (e.g. ["acp"] for cursor)
	Aliases      []string          `json:"aliases,omitempty"`       // custom trigger commands (e.g. ["gpt", "4o"])
	Cwd          string            `json:"cwd,omitempty"`           // working directory (workspace)
	Env          map[string]string `json:"env,omitempty"`           // extra environment variables (cli/acp type)
	Model        string            `json:"model,omitempty"`         // model name
	SystemPrompt string            `json:"system_prompt,omitempty"` // system prompt
	Endpoint     string            `json:"endpoint,omitempty"`      // API endpoint (http type)
	APIKey       string            `json:"api_key,omitempty"`       // API key (http type)
	Headers      map[string]string `json:"headers,omitempty"`       // extra HTTP headers (http type)
	MaxHistory   int               `json:"max_history,omitempty"`   // max history (http type)
}

// BuildAliasMap builds a map from custom alias to agent name from all agent configs.
// It logs warnings for conflicts: duplicate aliases and aliases shadowing agent keys.
func BuildAliasMap(agents map[string]AgentConfig) map[string]string {
	// Built-in commands that cannot be overridden
	reserved := map[string]bool{
		"info": true, "help": true, "new": true, "clear": true, "cwd": true,
	}

	m := make(map[string]string)
	for name, cfg := range agents {
		for _, alias := range cfg.Aliases {
			if reserved[alias] {
				log.Printf("[config] WARNING: alias %q for agent %q conflicts with built-in command, ignored", alias, name)
				continue
			}
			if existing, ok := m[alias]; ok {
				log.Printf("[config] WARNING: alias %q is defined by both %q and %q, using %q", alias, existing, name, name)
			}
			m[alias] = name
		}
	}

	// Warn if a custom alias shadows an agent key
	for alias, target := range m {
		if _, isAgent := agents[alias]; isAgent && alias != target {
			log.Printf("[config] WARNING: alias %q (-> %q) shadows agent key %q", alias, target, alias)
		}
	}

	return m
}

// DefaultConfig returns an empty configuration.
func DefaultConfig() *Config {
	return &Config{
		Agents:                       make(map[string]AgentConfig),
		AgentOSLaunchPolicy:          "off",
		VoiceInputModeDefault:        "transcript_first",
		ArchiveToolEnabled:           true,
		ObsidianEnabled:              true,
		ObsidianFormalWriteEnabled:   true,
		ObsidianVaultDir:             "~/Documents/LongclawVault",
		ObsidianVaultName:            "LongclawVault",
		ObsidianNotesDir:             "Inbox/WeChat",
		ObsidianAssetsDir:            "Assets/WeChat",
		ObsidianOpenAfterArchive:     false,
		ObsidianAutoArchiveEnabled:   true,
		ObsidianAutoArchiveMode:      "hybrid",
		ObsidianArchiveWindowMinutes: 30,
		ObsidianArchiveReplyEnabled:  true,
		ObsidianVoiceArchiveMode:     "transcript_only",
		ObsidianVideoArchiveMode:     "asset+summary",
		AgentInputPolicy:             "canonical",
	}
}

// ConfigPath returns the path to the config file.
func ConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".weclaw", "config.json"), nil
}

// Load loads configuration from disk and environment variables.
func Load() (*Config, error) {
	cfg := DefaultConfig()

	path, err := ConfigPath()
	if err != nil {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			loadEnv(cfg)
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Agents == nil {
		cfg.Agents = make(map[string]AgentConfig)
	}

	applyDefaults(cfg)
	loadEnv(cfg)
	return cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.AgentOSLaunchPolicy == "" {
		cfg.AgentOSLaunchPolicy = "off"
	}
	if cfg.SaveDir == "" {
		cfg.SaveDir = defaultSaveDir()
	}
	if cfg.VoiceInputModeDefault == "" {
		cfg.VoiceInputModeDefault = "transcript_first"
	}
	if cfg.ObsidianVaultDir == "" {
		cfg.ObsidianVaultDir = "~/Documents/LongclawVault"
	}
	if cfg.AgentInputPolicy == "" {
		cfg.AgentInputPolicy = "canonical"
	}
	if cfg.ObsidianVaultName == "" {
		cfg.ObsidianVaultName = "LongclawVault"
	}
	if cfg.ObsidianNotesDir == "" {
		cfg.ObsidianNotesDir = "Inbox/WeChat"
	}
	if cfg.ObsidianAssetsDir == "" {
		cfg.ObsidianAssetsDir = "Assets/WeChat"
	}
	if cfg.ObsidianAutoArchiveMode == "" {
		cfg.ObsidianAutoArchiveMode = "hybrid"
	}
	if cfg.ObsidianArchiveWindowMinutes <= 0 {
		cfg.ObsidianArchiveWindowMinutes = 30
	}
	if cfg.ObsidianVoiceArchiveMode == "" {
		cfg.ObsidianVoiceArchiveMode = "transcript_only"
	}
	if cfg.ObsidianVideoArchiveMode == "" {
		cfg.ObsidianVideoArchiveMode = "asset+summary"
	}
}

func loadEnv(cfg *Config) {
	if v := os.Getenv("WECLAW_DEFAULT_AGENT"); v != "" {
		cfg.DefaultAgent = v
	}
	if v := os.Getenv("WECLAW_API_ADDR"); v != "" {
		cfg.APIAddr = v
	}
	if v := firstNonEmptyEnv("WECLAW_AGENT_OS_BASE_URL", "LONGCLAW_AGENT_OS_BASE_URL"); v != "" {
		cfg.AgentOSBaseURL = v
	}
	if v := firstNonEmptyEnv("WECLAW_AGENT_OS_API_KEY", "LONGCLAW_AGENT_OS_API_KEY"); v != "" {
		cfg.AgentOSAPIKey = v
	}
	if v := os.Getenv("WECLAW_AGENT_OS_LAUNCH_POLICY"); v != "" {
		cfg.AgentOSLaunchPolicy = v
	}
	if v := firstNonEmptyEnv("WECLAW_CANONICAL_USER_ID", "LONGCLAW_CANONICAL_USER_ID"); v != "" {
		cfg.CanonicalUserID = v
	}
	if v := firstNonEmptyEnv("WECLAW_DEFAULT_LAUNCH_PACK", "LONGCLAW_DEFAULT_LAUNCH_PACK"); v != "" {
		cfg.DefaultLaunchPack = v
	}
	if v := firstNonEmptyEnv("WECLAW_DEFAULT_LAUNCH_CAPABILITY", "LONGCLAW_DEFAULT_LAUNCH_CAPABILITY"); v != "" {
		cfg.DefaultLaunchCapability = v
	}
	if v := os.Getenv("WECLAW_SAVE_DIR"); v != "" {
		cfg.SaveDir = v
	}
	if v := os.Getenv("WECLAW_PERSONA_DIR"); v != "" {
		cfg.PersonaDir = v
	}
	if v := os.Getenv("WECLAW_VOICE_INPUT_MODE_DEFAULT"); v != "" {
		cfg.VoiceInputModeDefault = v
	}
	if v := os.Getenv("WECLAW_ARCHIVE_TOOL_ENABLED"); v != "" {
		cfg.ArchiveToolEnabled = v == "1" || v == "true" || v == "TRUE"
	}
	if v := os.Getenv("WECLAW_OBSIDIAN_VAULT_DIR"); v != "" {
		cfg.ObsidianVaultDir = v
	}
	if v := os.Getenv("WECLAW_OBSIDIAN_VAULT_NAME"); v != "" {
		cfg.ObsidianVaultName = v
	}
	if v := os.Getenv("WECLAW_OBSIDIAN_NOTES_DIR"); v != "" {
		cfg.ObsidianNotesDir = v
	}
	if v := os.Getenv("WECLAW_OBSIDIAN_ASSETS_DIR"); v != "" {
		cfg.ObsidianAssetsDir = v
	}
	if v := os.Getenv("WECLAW_OBSIDIAN_ENABLED"); v != "" {
		cfg.ObsidianEnabled = v == "1" || v == "true" || v == "TRUE"
	}
	if v := os.Getenv("WECLAW_OBSIDIAN_FORMAL_WRITE_ENABLED"); v != "" {
		cfg.ObsidianFormalWriteEnabled = v == "1" || v == "true" || v == "TRUE"
	}
	if v := os.Getenv("WECLAW_OBSIDIAN_OPEN_AFTER_ARCHIVE"); v != "" {
		cfg.ObsidianOpenAfterArchive = v == "1" || v == "true" || v == "TRUE"
	}
	if v := os.Getenv("WECLAW_OBSIDIAN_AUTO_ARCHIVE_ENABLED"); v != "" {
		cfg.ObsidianAutoArchiveEnabled = v == "1" || v == "true" || v == "TRUE"
	}
	if v := os.Getenv("WECLAW_OBSIDIAN_AUTO_ARCHIVE_MODE"); v != "" {
		cfg.ObsidianAutoArchiveMode = v
	}
	if v := os.Getenv("WECLAW_OBSIDIAN_ARCHIVE_WINDOW_MINUTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.ObsidianArchiveWindowMinutes = n
		}
	}
	if v := os.Getenv("WECLAW_OBSIDIAN_ARCHIVE_REPLY_ENABLED"); v != "" {
		cfg.ObsidianArchiveReplyEnabled = v == "1" || v == "true" || v == "TRUE"
	}
	if v := os.Getenv("WECLAW_OBSIDIAN_VOICE_ARCHIVE_MODE"); v != "" {
		cfg.ObsidianVoiceArchiveMode = v
	}
	if v := os.Getenv("WECLAW_OBSIDIAN_VIDEO_ARCHIVE_MODE"); v != "" {
		cfg.ObsidianVideoArchiveMode = v
	}
	if v := os.Getenv("WECLAW_AGENT_INPUT_POLICY"); v != "" {
		cfg.AgentInputPolicy = v
	}
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return ""
}

func defaultSaveDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "weclaw-workspace")
	}
	return filepath.Join(home, ".weclaw", "workspace")
}

// Save saves the configuration to disk.
func Save(cfg *Config) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	return os.WriteFile(path, data, 0o600)
}
