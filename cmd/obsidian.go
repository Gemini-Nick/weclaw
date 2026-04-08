package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	obsidianarchive "github.com/fastclaw-ai/weclaw/obsidian"
	"github.com/spf13/cobra"
)

var (
	archivePathFlag    string
	archiveTitleFlag   string
	archiveRemarkFlag  string
	obsidianFormalFlag bool
)

func init() {
	obsidianCmd.AddCommand(obsidianInitCmd)
	obsidianCmd.AddCommand(obsidianMarkCmd)
	obsidianCmd.AddCommand(obsidianSyncCmd)
	obsidianCmd.AddCommand(obsidianCleanupCmd)
	obsidianCmd.AddCommand(obsidianMaintainCmd)

	obsidianMarkCmd.Flags().StringVar(&archivePathFlag, "path", "", "absolute path to a workspace media file")
	obsidianMarkCmd.Flags().StringVar(&archiveTitleFlag, "title", "", "optional note title")
	obsidianMarkCmd.Flags().StringVar(&archiveRemarkFlag, "remark", "", "optional note remark")
	obsidianCmd.PersistentFlags().BoolVar(&obsidianFormalFlag, "formal", false, "write to the formal Obsidian destination instead of the debug target")

	rootCmd.AddCommand(obsidianCmd)
}

var obsidianCmd = &cobra.Command{
	Use:   "obsidian",
	Short: "Manage WeChat media archive into Obsidian",
}

var obsidianInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Create the default Obsidian vault directories",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		cfg = effectiveObsidianConfig(cfg)
		if err := obsidianarchive.InitVault(cfg); err != nil {
			return err
		}
		fmt.Printf("initialized vault at %s\n", cfg.ObsidianVaultDir)
		return nil
	},
}

var obsidianMarkCmd = &cobra.Command{
	Use:   "mark",
	Short: "Mark a saved media file for Obsidian archiving",
	RunE: func(cmd *cobra.Command, args []string) error {
		if archivePathFlag == "" {
			return fmt.Errorf("--path is required")
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		cfg = effectiveObsidianConfig(cfg)
		sidecar, err := obsidianarchive.MarkForObsidian(cfg, archivePathFlag, archiveTitleFlag, archiveRemarkFlag)
		if err != nil {
			return err
		}
		fmt.Printf("marked %s as %s\n", archivePathFlag, sidecar.ArchiveState)
		return nil
	},
}

var obsidianSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Archive pending WeChat media into Obsidian",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		cfg = effectiveObsidianConfig(cfg)
		workspaceDir := cfg.SaveDir
		if workspaceDir == "" {
			home, _ := os.UserHomeDir()
			workspaceDir = filepath.Join(home, ".weclaw", "workspace")
		}
		result, err := obsidianarchive.SyncPending(cfg, workspaceDir)
		if err != nil {
			return err
		}
		sessionResult, err := obsidianarchive.SyncPendingSessions(cfg, workspaceDir)
		if err != nil {
			return err
		}
		fmt.Println(obsidianarchive.SummaryLineWithSessions(sessionResult, result, obsidianarchive.CleanupResult{}))
		return nil
	},
}

var obsidianCleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Clean old or archived WeChat media from workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		cfg = effectiveObsidianConfig(cfg)
		workspaceDir := cfg.SaveDir
		if workspaceDir == "" {
			home, _ := os.UserHomeDir()
			workspaceDir = filepath.Join(home, ".weclaw", "workspace")
		}
		result, err := obsidianarchive.CleanupWorkspace(workspaceDir, time.Now())
		if err != nil {
			return err
		}
		fmt.Println(obsidianarchive.SummaryLine(obsidianarchive.SyncResult{}, result))
		return nil
	},
}

var obsidianMaintainCmd = &cobra.Command{
	Use:   "maintain",
	Short: "Run Obsidian sync and workspace cleanup together",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		cfg = effectiveObsidianConfig(cfg)
		workspaceDir := cfg.SaveDir
		if workspaceDir == "" {
			home, _ := os.UserHomeDir()
			workspaceDir = filepath.Join(home, ".weclaw", "workspace")
		}
		syncResult, err := obsidianarchive.SyncPending(cfg, workspaceDir)
		if err != nil {
			return err
		}
		sessionResult, err := obsidianarchive.SyncPendingSessions(cfg, workspaceDir)
		if err != nil {
			return err
		}
		cleanupResult, err := obsidianarchive.CleanupWorkspace(workspaceDir, time.Now())
		if err != nil {
			return err
		}
		fmt.Println(obsidianarchive.SummaryLineWithSessions(sessionResult, syncResult, cleanupResult))
		return nil
	},
}

func effectiveObsidianConfig(cfg *config.Config) *config.Config {
	if obsidianFormalFlag {
		return cfg
	}
	return obsidianarchive.WithDebugTarget(cfg)
}
