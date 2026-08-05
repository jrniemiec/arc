package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/service"
)

var (
	embedAll    bool
	embedDryRun bool
)

func init() {
	embedCmd.Flags().BoolVar(&embedAll, "all", false, "re-embed every article, ignoring existing vectors")
	embedCmd.Flags().BoolVar(&embedDryRun, "dry-run", false, "report what would be embedded and estimated cost; no API calls")
	rootCmd.AddCommand(embedCmd)
}

var embedCmd = &cobra.Command{
	Use:   "embed",
	Short: "Rebuild the vector index for semantic search",
	Long: `Generate embeddings for articles and store them in the vector index at
~/.arc/index/, which backs the semantic half of arc search.

By default only articles missing from the index are embedded. Membership is
checked against the index itself rather than embed_model in meta.json, so a
lost, partial, or newly copied index rebuilds correctly — trusting meta.json
would skip every article and leave semantic search silently empty.

Only articles with a resolvable summary are eligible; embeddings are generated
from summary text, not the body.

Requires OPENAI_API_KEY (or ARC_OPENAI_API_KEY) and ingest.embed_profile in
config. --dry-run needs neither — it estimates from summaries on disk.

Cost is recorded to events.jsonl and shows up in arc stats.

  --all       re-embed everything. Use after changing the embedding model:
              existing vectors still look valid but are incompatible, so a
              plain 'arc embed' would skip them.
  --dry-run   count articles and estimate cost without calling the API.

Examples:
  arc embed
  arc embed --dry-run
  arc embed --all
  arc reindex && arc embed      # rebuild both indexes`,
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := svcFrom(cmd)

		var last int
		res, err := svc.Embed(cmd.Context(), service.EmbedOptions{
			All:    embedAll,
			DryRun: embedDryRun,
		}, func(done, total int) {
			last = done
			fmt.Fprintf(cmd.ErrOrStderr(), "\r  embedding %d/%d", done, total)
		})
		if last > 0 {
			fmt.Fprintln(cmd.ErrOrStderr())
		}
		if err != nil {
			return fmt.Errorf("embed: %w", err)
		}

		if isJSON(cmd) {
			if err := json.NewEncoder(cmd.OutOrStdout()).Encode(res); err != nil {
				return err
			}
			if res.Failed > 0 {
				return fmt.Errorf("%d article(s) failed to embed", res.Failed)
			}
			return nil
		}

		w := cmd.OutOrStdout()
		if res.DryRun {
			fmt.Fprintf(w, "%d of %d article(s) would be embedded with %s\n", res.Pending, res.Eligible, res.Model)
			fmt.Fprintf(w, "estimated %d tokens, ~$%.4f\n", res.Tokens, res.USD)
			return nil
		}

		if res.Pending == 0 {
			fmt.Fprintf(w, "vector index up to date (%d article(s) embedded)\n", res.Eligible)
			return nil
		}
		fmt.Fprintf(w, "embedded %d of %d article(s) with %s ($%.4f)\n", res.Embedded, res.Pending, res.Model, res.USD)

		if res.Failed > 0 {
			for _, e := range res.Errors {
				fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", e)
			}
			if res.Failed > len(res.Errors) {
				fmt.Fprintf(cmd.ErrOrStderr(), "  ... and %d more\n", res.Failed-len(res.Errors))
			}
			return fmt.Errorf("%d article(s) failed to embed", res.Failed)
		}
		return nil
	},
}
