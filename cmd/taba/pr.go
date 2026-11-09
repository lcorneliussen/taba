package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"taba.sh/taba/internal/wtc"
)

func addPRCommands(root *cobra.Command, asJSON *bool) {
	var collection string
	pr := &cobra.Command{Use: "pr", Short: "Manage collection-local pull request links", Args: cobra.NoArgs}
	pr.PersistentFlags().StringVar(&collection, "collection", "", "Collection directory (default: current)")
	context := func() (*wtc.Context, error) {
		if collection != "" {
			return wtc.OpenCollection(collection)
		}
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		return wtc.Discover(cwd)
	}
	path := &cobra.Command{Use: "path", Short: "Print the collection PR file path", Args: cobra.NoArgs}
	path.RunE = func(cmd *cobra.Command, args []string) error {
		c, err := context()
		if err != nil {
			return err
		}
		if *asJSON {
			return emit(envelope{OK: true, Data: map[string]string{"path": c.PRFile()}, Summary: c.PRFile()}, true)
		}
		fmt.Println(c.PRFile())
		return nil
	}
	pr.AddCommand(path)
	list := &cobra.Command{Use: "list", Short: "List enlisted pull requests", Args: cobra.NoArgs}
	list.RunE = func(cmd *cobra.Command, args []string) error {
		c, err := context()
		if err != nil {
			return err
		}
		records, err := c.ListPRs()
		if err != nil {
			return err
		}
		if *asJSON {
			return emit(envelope{OK: true, Data: records, Summary: fmt.Sprintf("%d enlisted PRs", len(records))}, true)
		}
		fmt.Printf("file: %s\n", c.PRFile())
		for _, r := range records {
			fmt.Printf("%-18s #%-6s %-28s %s\n", r.Repo, r.Number, r.Branch, r.Title)
		}
		return nil
	}
	pr.AddCommand(list)
	var branch, url, title string
	enlist := &cobra.Command{Use: "enlist <repo> <number>", Short: "Record a pull request in this collection", Args: cobra.ExactArgs(2)}
	enlist.Flags().StringVar(&branch, "branch", "", "Head branch (defaults to checked-out branch)")
	enlist.Flags().StringVar(&url, "url", "", "Pull request URL")
	enlist.Flags().StringVar(&title, "title", "", "Pull request title")
	enlist.RunE = func(cmd *cobra.Command, args []string) error {
		if err := wtc.ValidatePRIdentity(args[0], args[1]); err != nil {
			return err
		}
		c, err := context()
		if err != nil {
			return err
		}
		values := map[string]string{"repo": args[0], "number": args[1]}
		if err := c.RunHook("pr.enlist.pre", values); err != nil {
			return err
		}
		r, err := c.EnlistPR(wtc.PRRecord{Repo: args[0], Number: args[1], Branch: branch, URL: url, Title: title})
		if err != nil {
			return err
		}
		if err := c.RunHook("pr.enlist.post", values); err != nil {
			return err
		}
		summary := fmt.Sprintf("enlisted %s#%s in %s", r.Repo, r.Number, c.PRFile())
		if *asJSON {
			return emit(envelope{OK: true, Data: r, Summary: summary}, true)
		}
		fmt.Println(summary)
		return nil
	}
	pr.AddCommand(enlist)
	unlist := &cobra.Command{Use: "unlist <repo> <number>", Short: "Remove a collection PR link", Args: cobra.ExactArgs(2)}
	unlist.RunE = func(cmd *cobra.Command, args []string) error {
		if err := wtc.ValidatePRIdentity(args[0], args[1]); err != nil {
			return err
		}
		c, err := context()
		if err != nil {
			return err
		}
		values := map[string]string{"repo": args[0], "number": args[1]}
		if err := c.RunHook("pr.unlist.pre", values); err != nil {
			return err
		}
		if err := c.UnlistPR(args[0], args[1]); err != nil {
			return err
		}
		if err := c.RunHook("pr.unlist.post", values); err != nil {
			return err
		}
		summary := fmt.Sprintf("unlisted %s#%s from %s", args[0], args[1], c.PRFile())
		if *asJSON {
			return emit(envelope{OK: true, Summary: summary}, true)
		}
		fmt.Println(summary)
		return nil
	}
	pr.AddCommand(unlist)
	root.AddCommand(pr)
}
