package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/config"
	"github.com/jrniemiec/arc/internal/clog"
	"github.com/jrniemiec/arc/library"
	"github.com/jrniemiec/arc/service"
)

// contextKey is used to store values in cobra's context.
type contextKey int

const (
	keyService contextKey = iota
	keyConfig
	keyJSON
)

var (
	cfgFile      string
	dataRoot     string
	articlesRoot string
	jsonOut      bool
)

var rootCmd = &cobra.Command{
	Use:   "arc",
	Short: "arc — personal knowledge OS",
	Long: `arc ingests articles, generates summaries and flashcards, and makes your knowledge searchable.

Pipeline commands can be composed with Unix pipes:
  arc extract <url>            extract plain text → stdout
  arc summarize [slug]         summarize article or piped text → stdout
  arc ingest <url|file>        full pipeline: extract → summarize → flash → flashcards → index

Examples:
  arc ingest https://example.com/article
  arc extract https://example.com/article | arc summarize --style bullets
  arc summarize --style technical --write 20260522-my-article
  arc read --summary 20260522-my-article
  arc search "attention mechanism"
  arc collections list
  arc collections read ml
  arc collections suggest --apply`,
}

// Execute runs the root command.
func Execute() {
	rootCmd.SilenceErrors = true // we log and print errors ourselves
	if err := rootCmd.Execute(); err != nil {
		clog.Error("arc error", "err", err)
		fmt.Fprintf(os.Stderr, "arc: %s\n", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: ~/.arc/config.json)")
	rootCmd.PersistentFlags().StringVar(&dataRoot, "data-root", "", "arc data root directory (default: ~/.arc)")
	rootCmd.PersistentFlags().StringVar(&articlesRoot, "articles-root", "", "articles directory (default: <data-root>/articles)")
	rootCmd.PersistentFlags().BoolVar(&jsonOut, "json", false, "output JSON")

	rootCmd.PersistentPreRunE = openLibrary
	rootCmd.PersistentPostRunE = closeLibrary
}

func openLibrary(cmd *cobra.Command, args []string) error {
	// Skip library init for help requests — opening the library for --help is wasteful
	// and can cause issues if the data directory doesn't exist yet.
	for _, a := range os.Args[1:] {
		if a == "-h" || a == "--help" {
			return nil
		}
	}
	if cmd.Name() == "help" {
		return nil
	}
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Initialise logging. clog.Init is idempotent — safe to call every command.
	logLevel, _ := clog.ParseLevel(cfg.LogLevel)
	clog.Init(cfg.LogPath, logLevel)

	lib, err := library.Open(cmd.Context(), cfg)
	if err != nil {
		return fmt.Errorf("open library: %w", err)
	}

	svc := service.New(lib, cfg)

	// Store in context for subcommands
	ctx := context.WithValue(cmd.Context(), keyService, svc)
	ctx = context.WithValue(ctx, keyConfig, cfg)
	ctx = context.WithValue(ctx, keyJSON, jsonOut)
	cmd.SetContext(ctx)
	return nil
}

func closeLibrary(cmd *cobra.Command, _ []string) error {
	// Library cleanup is handled by the service's underlying library.
	// Nothing to do here for now — connection pool closes on process exit.
	return nil
}

func loadConfig() (config.Config, error) {
	path := cfgFile
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return config.Default(), nil
		}
		path = filepath.Join(home, ".arc", "config.json")
	}

	cfg, err := config.Load(path)
	if err != nil {
		return cfg, err
	}

	// --data-root overrides config
	if dataRoot != "" {
		cfg.DataRoot = dataRoot
		cfg.ArticlesRoot = filepath.Join(dataRoot, "articles")
		cfg.DBPath = filepath.Join(dataRoot, "arc.db")
		cfg.VectorPath = filepath.Join(dataRoot, "index")
		cfg.EventsPath = filepath.Join(dataRoot, "events.jsonl")
	}
	// --articles-root overrides articles location independently
	if articlesRoot != "" {
		cfg.ArticlesRoot = articlesRoot
	}

	return cfg, nil
}

// svcFrom extracts the Service from a command's context.
func svcFrom(cmd *cobra.Command) *service.Service {
	return cmd.Context().Value(keyService).(*service.Service)
}

// cfgFrom extracts the Config from a command's context.
func cfgFrom(cmd *cobra.Command) config.Config {
	v, _ := cmd.Context().Value(keyConfig).(config.Config)
	return v
}

// isJSON returns true if --json was set.
func isJSON(cmd *cobra.Command) bool {
	v, _ := cmd.Context().Value(keyJSON).(bool)
	return v
}

// resolveSlug resolves a user query to an article slug via the service.
func resolveSlug(cmd *cobra.Command, query string) (string, error) {
	return svcFrom(cmd).ResolveSlug(cmd.Context(), query)
}

// resolveCollectionSlug resolves a user query to a collection slug via the service.
func resolveCollectionSlug(cmd *cobra.Command, query string) (string, error) {
	return svcFrom(cmd).ResolveCollectionSlug(cmd.Context(), query)
}

// exitErr prints an error and exits with code 1.
func exitErr(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "arc: "+format+"\n", args...)
	os.Exit(1)
}
