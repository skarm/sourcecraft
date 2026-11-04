// Command pullrequests creates a PR or merges an existing PR and waits for the result.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/skarm/sourcecraft"
)

const actionCreate = "create"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		if _, err := fmt.Fprintln(out, "Usage: pullrequests <create|merge> -org <slug> -repo <slug> [options]\nUse create -h or merge -h for command options."); err != nil {
			return err
		}

		if len(args) == 0 {
			return errors.New("choose create or merge")
		}

		return nil
	}

	action := args[0]
	if action != actionCreate && action != "merge" {
		return fmt.Errorf("unknown command %q", action)
	}

	fs := flag.NewFlagSet(action, flag.ContinueOnError)
	fs.SetOutput(out)
	org := fs.String("org", "", "organization slug")
	repo := fs.String("repo", "", "repository slug")
	timeout := fs.Duration("timeout", 5*time.Minute, "total request and merge-wait timeout")

	var (
		source, target, title, description, pr string
		draft, squash, deleteSource            bool
	)

	if action == actionCreate {
		fs.StringVar(&source, "source", "", "existing source branch")
		fs.StringVar(&target, "target", "", "existing target branch")
		fs.StringVar(&title, "title", "", "pull request title")
		fs.StringVar(&description, "description", "", "pull request description")
		fs.BoolVar(&draft, "draft", false, "create a draft instead of publishing")
	} else {
		fs.StringVar(&pr, "pr", "", "existing pull request number (slug)")
		fs.BoolVar(&squash, "squash", false, "override squash setting: -squash=true or -squash=false")
		fs.BoolVar(&deleteSource, "delete-source", false, "override source branch deletion setting")
	}

	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}

		return err
	}

	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}

	if strings.TrimSpace(*org) == "" || strings.TrimSpace(*repo) == "" || *timeout <= 0 {
		return errors.New("provide -org, -repo and a positive -timeout")
	}

	if action == actionCreate && (strings.TrimSpace(source) == "" || strings.TrimSpace(target) == "" || strings.TrimSpace(title) == "") {
		return errors.New("create requires -source, -target and -title")
	}

	if action == "merge" && strings.TrimSpace(pr) == "" {
		return errors.New("merge requires -pr")
	}

	token := os.Getenv("SOURCECRAFT_TOKEN")
	if token == "" {
		return errors.New("set SOURCECRAFT_TOKEN to a personal access token")
	}

	client, err := sourcecraft.NewClient(token)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	if action == actionCreate {
		created, _, err := client.CreatePullRequest(ctx, *org, *repo, &sourcecraft.CreatePullRequestBody{
			Title: new(title), Description: new(description), SourceBranch: new(source), TargetBranch: new(target), Publish: new(!draft),
		}, nil)
		if err != nil {
			return fmt.Errorf("create pull request: %w", err)
		}

		if _, err := fmt.Fprintf(out, "PR %s: id=%s status=%s\n", created.Slug, created.ID, created.Status); err != nil {
			return fmt.Errorf("pull request %s was created, but writing its metadata failed: %w", created.Slug, err)
		}

		return nil
	}

	body := &sourcecraft.MergePullRequestBody{}
	// Unspecified flags remain nil, preserving the repository's merge settings.
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "squash":
			body.Squash = new(squash)
		case "delete-source":
			body.DeleteBranch = new(deleteSource)
		}
	})

	return mergePullRequest(ctx, client, *org, *repo, pr, body, out)
}

func mergePullRequest(ctx context.Context, client *sourcecraft.Client, org, repo, pr string, body *sourcecraft.MergePullRequestBody, out io.Writer) error {
	operation, _, err := client.MergePullRequest(ctx, org, repo, pr, body, nil)
	if err != nil {
		return fmt.Errorf("start merging PR %s: %w", pr, err)
	}

	if operation.OperationID == "" {
		return errors.New("merge response has no operation ID")
	}

	if _, err := fmt.Fprintf(out, "Merge operation: %s\n", operation.OperationID); err != nil {
		return fmt.Errorf("merge operation %s started, but writing its ID failed: %w", operation.OperationID, err)
	}

	completed, _, err := client.WaitPullRequestMerge(ctx, operation.OperationID, time.Second)
	if err != nil {
		return fmt.Errorf("wait for merge operation %s: %w", operation.OperationID, err)
	}

	if completed.Response == nil {
		return errors.New("successful merge operation has no result")
	}

	_, err = fmt.Fprintf(out, "Merged PR %s: commit=%s target_base=%s\n", pr, completed.Response.MergeCommitHash, completed.Response.TargetCommitHash)

	return err
}
