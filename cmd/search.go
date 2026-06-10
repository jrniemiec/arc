package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jrniemiec/arc/service"
	"github.com/jrniemiec/arc/store"
)

var (
	searchCollection string
	searchTag        string
	searchLimit      int
	searchNoSemantic bool
)

func init() {
	searchCmd.Flags().StringVar(&searchCollection, "collection", "", "filter by collection ID")
	searchCmd.Flags().StringVar(&searchTag, "tag", "", "filter by tag")
	searchCmd.Flags().IntVar(&searchLimit, "limit", 20, "maximum number of results")
	searchCmd.Flags().BoolVar(&searchNoSemantic, "no-semantic", false, "keyword-only search, skip vector component")
	rootCmd.AddCommand(searchCmd)
}

// highlightSnippet replaces SQLite snippet markers [term] with bold ANSI when tty.
var snippetRe = regexp.MustCompile(`\[([^\]]+)\]`)

func highlightSnippet(s string, tty bool) string {
	if !tty {
		// Keep markers readable as *term*
		return snippetRe.ReplaceAllString(s, "*$1*")
	}
	return snippetRe.ReplaceAllStringFunc(s, func(m string) string {
		inner := m[1 : len(m)-1]
		return "\033[1m" + inner + "\033[0m" // bold
	})
}

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search articles by keyword",
	Long: `Search the knowledge base and print matching articles with context snippets.

Searches are performed against the preferred summary variant (same variant used
by arc read --summary), not the full body. Both indexes are built from the same
summary text.

Search modes (set by --no-semantic):
  combined (default)  FTS5 keyword + vector semantic search run in parallel;
                      scores normalized to [0,1] and summed; results deduped.
                      Falls back to keyword-only if the vector index is empty.
  keyword             FTS5 BM25 ranking only; fast, exact-term matching.

Each result shows a source badge indicating which index returned it:
  [fts]     keyword match only
  [vector]  semantic match only (similarity ≥ 0.5)
  [both]    matched in both indexes (highest combined score)

Excerpt shows the matching context from the summary with matched terms highlighted
(bold on terminal, *asterisks* in pipes). Vector-only hits have no excerpt.

To populate the vector index: arc reindex (requires OPENAI_API_KEY).

Examples:
  arc search "attention mechanism"
  arc search "transformer" --limit 5
  arc search "diffusion" --tag ml
  arc search "gpt" --no-semantic
  arc search "gpt" --json`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc := svcFrom(cmd)

		var tags []string
		if searchTag != "" {
			tags = []string{searchTag}
		}

		mode := store.QueryCombined
		if searchNoSemantic {
			mode = store.QueryKeyword
		}

		req := service.SearchRequest{
			Query:      strings.Join(args, " "),
			Collection: searchCollection,
			Tags:       tags,
			Mode:       mode,
			Limit:      searchLimit,
		}

		results, err := svc.Search(cmd.Context(), req)
		if err != nil {
			return fmt.Errorf("search: %w", err)
		}

		if isJSON(cmd) {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(results)
		}

		if len(results) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "no results for %q\n", args[0])
			return nil
		}

		w := cmd.OutOrStdout()
		tty := isTTY(os.Stdout)

		fmt.Fprintf(w, "results for %q (%d):\n\n", args[0], len(results))
		for i, r := range results {
			a := r.Article

			// Line 1: index + slug + date + source badge (when not pure FTS)
			date := a.IngestedAt.Format("2006-01-02")
			badge := "  " + dim("["+r.Source+"]", tty)
			if tty {
				fmt.Fprintf(w, "\033[1m%d.\033[0m  %s  \033[2m(%s)\033[0m%s\n", i+1, a.ID, date, badge)
			} else {
				fmt.Fprintf(w, "%d.  %s  (%s)%s\n", i+1, a.ID, date, badge)
			}

			// Line 2: title (if present)
			if a.Title != "" {
				fmt.Fprintf(w, "    %s\n", a.Title)
			}

			// Line 3: excerpt with highlighted terms
			if r.Excerpt != "" {
				excerpt := strings.Join(strings.Fields(r.Excerpt), " ")
				fmt.Fprintf(w, "    %s\n", highlightSnippet(excerpt, tty))
			}

			fmt.Fprintln(w)
		}
		return nil
	},
}
