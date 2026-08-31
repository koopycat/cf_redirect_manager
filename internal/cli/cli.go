// Package cli provides the cf-redirect Cobra commands. It deliberately keeps
// prompting and rendering at the edge; planning and application remain in core.
package cli

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/koopycat/cf-redirect/internal/app"
	"github.com/koopycat/cf-redirect/internal/auth"
	"github.com/koopycat/cf-redirect/internal/cloudflare"
	"github.com/koopycat/cf-redirect/internal/config"
	"github.com/koopycat/cf-redirect/internal/csvio"
	"github.com/koopycat/cf-redirect/internal/domain"
	"github.com/koopycat/cf-redirect/internal/planner"
	"github.com/koopycat/cf-redirect/internal/textsafe"
	"github.com/koopycat/cf-redirect/internal/tui"
)

type options struct {
	accountID string
	listID    string
}

// NewRootCmd builds the production command tree.
func NewRootCmd() *cobra.Command {
	o := &options{}
	root := &cobra.Command{
		Use:           "cf-redirect",
		Short:         "Safely manage a Cloudflare Bulk Redirect List",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !isTerminal(cmd) {
				return fmt.Errorf("no command supplied; run cf-redirect --help")
			}
			return runTUI(cmd, o)
		},
	}
	root.PersistentFlags().StringVar(&o.accountID, "account-id", "", "Cloudflare account ID (or CLOUDFLARE_ACCOUNT_ID)")
	root.PersistentFlags().StringVar(&o.listID, "list-id", "", "Cloudflare redirect list ID (or CLOUDFLARE_LIST_ID)")
	root.AddCommand(listCmd(o), searchCmd(o), addCmd(o), editCmd(o), deleteCmd(o), importCmd(o), configCmd(o), authCmd(o), loginCmd(o), logoutCmd(o), statusCmd(o), tuiCmd(o))
	return root
}

func configured(cmd *cobra.Command, o *options) (config.Config, *cloudflare.Client, error) {
	cfg, err := config.Resolve(o.accountID, o.listID)
	if err != nil {
		return config.Config{}, nil, err
	}
	token, err := auth.NewResolver(cfg.AccountID).Token()
	if err != nil {
		return config.Config{}, nil, fmt.Errorf("authenticate: %w (set %s or run cf-redirect auth login)", err, auth.TokenEnv)
	}
	return cfg, &cloudflare.Client{Token: token}, nil
}

func listCmd(o *options) *cobra.Command {
	var format string
	cmd := &cobra.Command{Use: "list", Short: "List redirects", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, api, err := configured(cmd, o)
		if err != nil {
			return err
		}
		items, err := api.ListItems(cmd.Context(), cfg.AccountID, cfg.ListID)
		if err != nil {
			return err
		}
		return renderRedirects(cmd.OutOrStdout(), items, format)
	}}
	cmd.Flags().StringVarP(&format, "format", "f", "table", "Output: table, json, or csv")
	return cmd
}

func searchCmd(o *options) *cobra.Command {
	var format string
	cmd := &cobra.Command{Use: "search <query>", Short: "Search source, target, and comment", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, api, err := configured(cmd, o)
		if err != nil {
			return err
		}
		items, err := api.ListItems(cmd.Context(), cfg.AccountID, cfg.ListID)
		if err != nil {
			return err
		}
		q := strings.ToLower(args[0])
		filtered := items[:0]
		for _, item := range items {
			if strings.Contains(strings.ToLower(item.Source), q) || strings.Contains(strings.ToLower(item.Target), q) || strings.Contains(strings.ToLower(item.Comment), q) {
				filtered = append(filtered, item)
			}
		}
		return renderRedirects(cmd.OutOrStdout(), filtered, format)
	}}
	cmd.Flags().StringVarP(&format, "format", "f", "table", "Output: table, json, or csv")
	return cmd
}

func addCmd(o *options) *cobra.Command {
	var dryRun, yes bool
	cmd := &cobra.Command{Use: "add <source> <target>", Short: "Plan and add a redirect", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		return makeMutation(cmd, o, dryRun, yes, func(current []domain.Redirect) (planner.Plan, error) {
			return planner.AddRedirect(current, domain.New(args[0], args[1]))
		})
	}}
	mutationFlags(cmd, &dryRun, &yes)
	return cmd
}

func editCmd(o *options) *cobra.Command {
	var dryRun, yes bool
	cmd := &cobra.Command{Use: "edit <source> <target> [new-source]", Short: "Plan and edit a redirect by its exact source", Args: cobra.RangeArgs(2, 3), RunE: func(cmd *cobra.Command, args []string) error {
		return makeMutation(cmd, o, dryRun, yes, func(current []domain.Redirect) (planner.Plan, error) {
			item, err := findSource(current, args[0])
			if err != nil {
				return planner.Plan{}, err
			}
			newSource := args[0]
			if len(args) == 3 {
				newSource = args[2]
			}
			return planner.EditRedirect(current, item.ID, newSource, args[1])
		})
	}}
	mutationFlags(cmd, &dryRun, &yes)
	return cmd
}

func deleteCmd(o *options) *cobra.Command {
	var dryRun, yes bool
	cmd := &cobra.Command{Use: "delete <source>", Aliases: []string{"rm"}, Short: "Plan and delete a redirect by its exact source", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return makeMutation(cmd, o, dryRun, yes, func(current []domain.Redirect) (planner.Plan, error) {
			item, err := findSource(current, args[0])
			if err != nil {
				return planner.Plan{}, err
			}
			return planner.DeleteRedirect(current, item.ID)
		})
	}}
	mutationFlags(cmd, &dryRun, &yes)
	return cmd
}

func importCmd(o *options) *cobra.Command {
	var dryRun, yes bool
	cmd := &cobra.Command{Use: "import <file|->", Short: "Plan CSV add/update operations (never deletes omitted redirects)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		var reader io.Reader = cmd.InOrStdin()
		var file *os.File
		var err error
		if args[0] != "-" {
			file, err = os.Open(args[0])
			if err != nil {
				return err
			}
			defer file.Close()
			reader = file
		}
		imported, err := csvio.Read(reader)
		if err != nil {
			return err
		}
		return makeMutation(cmd, o, dryRun, yes, func(current []domain.Redirect) (planner.Plan, error) { return planner.ImportUpsert(current, imported) })
	}}
	mutationFlags(cmd, &dryRun, &yes)
	return cmd
}

func mutationFlags(cmd *cobra.Command, dryRun, yes *bool) {
	cmd.Flags().BoolVar(dryRun, "dry-run", false, "Show the plan without applying it")
	cmd.Flags().BoolVarP(yes, "yes", "y", false, "Apply without an interactive confirmation")
}

func makeMutation(cmd *cobra.Command, o *options, dryRun, yes bool, createPlan func([]domain.Redirect) (planner.Plan, error)) error {
	cfg, api, err := configured(cmd, o)
	if err != nil {
		return err
	}
	current, err := api.ListItems(cmd.Context(), cfg.AccountID, cfg.ListID)
	if err != nil {
		return err
	}
	plan, err := createPlan(current)
	if err != nil {
		return err
	}
	if err := renderPlan(cmd.OutOrStdout(), plan); err != nil {
		return err
	}
	if plan.Empty() || dryRun {
		return nil
	}
	if !yes && !isTerminal(cmd) {
		return fmt.Errorf("--yes is required for non-interactive mutations (or use --dry-run)")
	}
	if !yes {
		ok, err := confirm(cmd)
		if err != nil || !ok {
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
			return nil
		}
	}
	report, err := (app.Executor{API: api, AccountID: cfg.AccountID, ListID: cfg.ListID, PollInterval: cloudflare.DefaultBulkPollInterval}).Apply(cmd.Context(), plan)
	if err != nil {
		// Print every phase result so a failure is diagnosable instead of a
		// single terse error line, and include recovery hints.
		for _, phase := range report.Phases {
			if phase.Err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "phase %s: FAILED (%d requested): %v\n", phase.Phase, phase.Requested, phase.Err)
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(), "phase %s: completed (%d requested, operation %s)\n", phase.Phase, phase.Requested, phase.Operation.ID)
			}
		}
		var execErr *app.ExecutionError
		if errors.As(err, &execErr) {
			fmt.Fprintln(cmd.ErrOrStderr(), "The plan may be partially applied; run cf-redirect list to verify.")
		}
		var apiErr *cloudflare.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 429 {
			fmt.Fprintln(cmd.ErrOrStderr(), "Cloudflare rate limit: the token's rolling request budget is exhausted; wait a few minutes and retry.")
		}
		return err
	}
	for _, phase := range report.Phases {
		if phase.Err != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Phase %s: FAILED (%d requested): %v\n", phase.Phase, phase.Requested, phase.Err)
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Phase %s: applied %d item(s) (operation %s)\n", phase.Phase, phase.Requested, phase.Operation.ID)
	}
	return nil
}

func findSource(items []domain.Redirect, source string) (domain.Redirect, error) {
	for _, item := range items {
		if item.Source == source {
			if item.ID == "" {
				return domain.Redirect{}, fmt.Errorf("redirect source %q has no Cloudflare item ID", source)
			}
			return item, nil
		}
	}
	return domain.Redirect{}, fmt.Errorf("redirect source %q not found", source)
}

func confirm(cmd *cobra.Command) (bool, error) {
	fmt.Fprint(cmd.OutOrStdout(), "Apply this plan? [y/N] ")
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(line), "y") || strings.EqualFold(strings.TrimSpace(line), "yes"), nil
}
func isTerminal(cmd *cobra.Command) bool {
	in, out := cmd.InOrStdin(), cmd.OutOrStdout()
	i, iok := in.(*os.File)
	o, ook := out.(*os.File)
	return iok && ook && term.IsTerminal(i.Fd()) && term.IsTerminal(o.Fd())
}

// readPasswordInteractive prompts on stderr and reads a hidden value from the
// terminal stdin stream Cobra provides. It requires only stdin to be a TTY, so
// piping stdout does not silently swallow the prompt.
func readPasswordInteractive(cmd *cobra.Command, prompt string) (string, error) {
	in, ok := cmd.InOrStdin().(*os.File)
	if !ok || !term.IsTerminal(in.Fd()) {
		return "", fmt.Errorf("login requires a running terminal; pipe the token with --token-stdin")
	}
	if _, err := fmt.Fprint(cmd.ErrOrStderr(), prompt); err != nil {
		return "", err
	}
	defer fmt.Fprintln(cmd.ErrOrStderr())
	value, err := term.ReadPassword(in.Fd())
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(value)), nil
}

func renderPlan(w io.Writer, plan planner.Plan) error {
	// Render into a buffer first so a failing output can never let a mutation
	// proceed without its required plan preview.
	var b strings.Builder
	adds, updates, deletes := plan.Counts()
	fmt.Fprintf(&b, "Plan: %d add, %d update, %d delete\n", adds, updates, deletes)
	for _, c := range plan.Changes {
		switch c.Kind {
		case planner.Add:
			fmt.Fprintf(&b, "+ %s -> %s\n", textsafe.StripControls(c.After.Source), textsafe.StripControls(c.After.Target))
		case planner.Update:
			fmt.Fprintf(&b, "~ %s -> %s => %s -> %s\n", textsafe.StripControls(c.Before.Source), textsafe.StripControls(c.Before.Target), textsafe.StripControls(c.After.Source), textsafe.StripControls(c.After.Target))
		case planner.Delete:
			fmt.Fprintf(&b, "- %s\n", textsafe.StripControls(c.Before.Source))
		}
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("write plan: %w", err)
	}
	return nil
}

func renderRedirects(w io.Writer, items []domain.Redirect, format string) error {
	items = append([]domain.Redirect(nil), items...)
	for i := range items {
		items[i].Source = textsafe.StripControls(items[i].Source)
		items[i].Target = textsafe.StripControls(items[i].Target)
		items[i].Comment = textsafe.StripControls(items[i].Comment)
	}
	slices.SortFunc(items, func(a, b domain.Redirect) int { return strings.Compare(a.Source, b.Source) })
	switch format {
	case "json":
		return json.NewEncoder(w).Encode(items)
	case "csv":
		return csvio.Write(w, items)
	case "table", "":
		writer := csv.NewWriter(w)
		writer.Comma = '\t'
		_ = writer.Write([]string{"SOURCE", "TARGET", "STATUS", "COMMENT"})
		for _, item := range items {
			_ = writer.Write([]string{item.Source, item.Target, fmt.Sprint(item.EffectiveStatusCode()), item.Comment})
		}
		writer.Flush()
		return writer.Error()
	default:
		return fmt.Errorf("unknown format %q (use table, json, or csv)", format)
	}
}

func configCmd(o *options) *cobra.Command {
	root := &cobra.Command{Use: "config", Short: "Manage persistent account and list configuration", Args: cobra.NoArgs}
	show := &cobra.Command{Use: "show", Short: "Show resolved configuration", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Resolve(o.accountID, o.listID)
		if err != nil {
			return err
		}
		path, err := config.Path()
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "account_id: %s\nlist_id: %s\nconfig_file: %s\n", cfg.AccountID, cfg.ListID, path)
		return err
	}}
	root.RunE = show.RunE
	set := &cobra.Command{Use: "set", Short: "Persist the flag-provided account and list IDs", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(o.accountID) == "" || strings.TrimSpace(o.listID) == "" {
			return fmt.Errorf("config set requires --account-id and --list-id")
		}
		path, err := config.Path()
		if err != nil {
			return err
		}
		if err := config.Save(path, config.Config{AccountID: o.accountID, ListID: o.listID}); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Configuration saved to %s.\n", path)
		return nil
	}}
	root.AddCommand(show, set)
	return root
}

func authCmd(o *options) *cobra.Command {
	root := &cobra.Command{Use: "auth", Short: "Manage the API token in the OS keychain"}
	root.AddCommand(loginCmd(o), logoutCmd(o))
	return root
}

func loginCmd(o *options) *cobra.Command {
	var stdin bool
	cmd := &cobra.Command{Use: "login", Short: "Verify and store an API token in the OS keychain", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Resolve(o.accountID, o.listID)
		if err != nil {
			return err
		}
		var token string
		if stdin {
			data, readErr := io.ReadAll(cmd.InOrStdin())
			if readErr != nil {
				return readErr
			}
			token = strings.TrimSpace(string(data))
		} else {
			token, err = readPasswordInteractive(cmd, "Cloudflare API token: ")
			if err != nil {
				return err
			}
		}
		if token == "" {
			return fmt.Errorf("API token must not be empty")
		}
		if _, err := (&cloudflare.Client{Token: token}).ListItems(cmd.Context(), cfg.AccountID, cfg.ListID); err != nil {
			return fmt.Errorf("verify API token against configured redirect list: %w", err)
		}
		if err := auth.NewResolver(cfg.AccountID).Store(token); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "API token verified and stored in OS keychain.")
		return nil
	}}
	cmd.Flags().BoolVar(&stdin, "token-stdin", false, "Read API token from standard input")
	return cmd
}

func logoutCmd(o *options) *cobra.Command {
	return &cobra.Command{Use: "logout", Short: "Remove the account-specific stored API token", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Resolve(o.accountID, o.listID)
		if err != nil {
			return err
		}
		if err := auth.NewResolver(cfg.AccountID).Delete(); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Stored API token removed.")
		return nil
	}}
}

func statusCmd(o *options) *cobra.Command {
	return &cobra.Command{Use: "status <operation-id>", Short: "Show a Cloudflare bulk operation status", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cfg, api, err := configured(cmd, o)
		if err != nil {
			return err
		}
		operation, err := api.GetBulkOperation(cmd.Context(), cfg.AccountID, args[0])
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(operation)
	}}
}
func tuiCmd(o *options) *cobra.Command {
	return &cobra.Command{Use: "tui", Short: "Launch the interactive redirect editor", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !isTerminal(cmd) {
			return fmt.Errorf("tui requires an interactive terminal")
		}
		return runTUI(cmd, o)
	}}
}
func runTUI(cmd *cobra.Command, o *options) error {
	cfg, api, err := configured(cmd, o)
	if err != nil {
		return err
	}
	return tui.Run(api, cfg.AccountID, cfg.ListID)
}
