package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/config"
)

func init() {
	initCmd.PersistentPreRunE = func(*cobra.Command, []string) error { return nil }
	rootCmd.AddCommand(initCmd)
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize arc — create ~/.arc and write the default config",
	Long: `Init creates the arc data directory and writes a fully annotated
config file to ~/.arc/config.json (or the path set by --config).

All available LLM profiles are included with pricing and tradeoff notes
so you can make an informed choice before your first ingest.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfgPath := cfgFile
		if cfgPath == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("home dir: %w", err)
			}
			cfgPath = filepath.Join(home, ".arc", "config.json")
		}

		cfgDir := filepath.Dir(cfgPath)

		// ── Create directory structure ─────────────────────────────────────
		dirs := []string{
			cfgDir,
			filepath.Join(cfgDir, "articles"),
		}
		for _, d := range dirs {
			if err := os.MkdirAll(d, 0755); err != nil {
				return fmt.Errorf("create %s: %w", d, err)
			}
		}
		fmt.Fprintf(cmd.OutOrStdout(), "arc: data directory: %s\n", cfgDir)

		// ── Write config (warn if already exists) ──────────────────────────
		if _, err := os.Stat(cfgPath); err == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "arc: config already exists: %s\n", cfgPath)
			fmt.Fprint(cmd.OutOrStdout(), "arc: overwrite? [y/N]: ")
			r := bufio.NewReader(os.Stdin)
			line, _ := r.ReadString('\n')
			if strings.ToLower(strings.TrimSpace(line)) != "y" {
				fmt.Fprintln(cmd.OutOrStdout(), "arc: keeping existing config")
				return nil
			}
		}

		data, err := config.DefaultConfigJSON()
		if err != nil {
			return fmt.Errorf("serialize config: %w", err)
		}
		if err := os.WriteFile(cfgPath, data, 0644); err != nil {
			return fmt.Errorf("write config: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "arc: config written: %s\n", cfgPath)

		// ── API key instructions ───────────────────────────────────────────
		fmt.Fprintln(cmd.OutOrStdout(), `
arc: API keys are read from environment variables:
arc:   OpenAI     — OPENAI_API_KEY     (or ARC_OPENAI_API_KEY)
arc:   Anthropic  — ANTHROPIC_API_KEY  (or ARC_ANTHROPIC_API_KEY)
arc:   Ollama     — no key needed; set host in profile if not localhost
arc:
arc: Add to your shell profile (~/.zshrc or ~/.bashrc):
arc:   export OPENAI_API_KEY="sk-..."
arc:   export ANTHROPIC_API_KEY="sk-ant-..."`)

		// ── Prompt to review config ────────────────────────────────────────
		fmt.Fprintf(cmd.OutOrStdout(), "\narc: → review and edit %s\n", cfgPath)
		fmt.Fprintln(cmd.OutOrStdout(), "arc:   set ingest.summary_profile, flash_profile, flashcard_profile to your preferred model")
		fmt.Fprint(cmd.OutOrStdout(), "\narc: press Enter when ready to validate, or q to quit: ")

		r := bufio.NewReader(os.Stdin)
		for {
			line, _ := r.ReadString('\n')
			line = strings.TrimSpace(strings.ToLower(line))
			if line == "q" || line == "quit" {
				fmt.Fprintln(cmd.OutOrStdout(), "arc: run `arc init` again when ready")
				return nil
			}

			cfg, err := config.Load(cfgPath)
			if err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "arc: config error: %v\narc: fix the file and press Enter to retry: ", err)
				continue
			}
			if err := cfg.Validate(); err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "arc: %v\narc: fix the file and press Enter to retry: ", err)
				continue
			}

			fmt.Fprintln(cmd.OutOrStdout(), "arc: config OK")
			break
		}

		fmt.Fprintln(cmd.OutOrStdout(), "\narc: setup complete. try:")
		fmt.Fprintln(cmd.OutOrStdout(), "arc:   arc ingest --dry-run https://example.com/article")
		fmt.Fprintln(cmd.OutOrStdout(), "arc:   arc ingest https://example.com/article")
		return nil
	},
}
