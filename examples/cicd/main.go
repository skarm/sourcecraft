// Command cicd starts a workflow or reads an existing run, waits for completion,
// and prints statuses, paginated cube logs and temporary artifact download URLs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/skarm/sourcecraft"
)

type inputs []sourcecraft.InputValue

type resultPrinter struct {
	ctx            context.Context
	client         *sourcecraft.Client
	org, repo, run string
	out            io.Writer
	problems       []error
}

func (v *inputs) String() string {
	values := make([]string, 0, len(*v))
	for _, item := range *v {
		values = append(values, *item.Name+"="+*item.Value)
	}

	return strings.Join(values, ", ")
}

func (v *inputs) Set(text string) error {
	name, value, ok := strings.Cut(text, "=")
	if !ok || strings.TrimSpace(name) == "" {
		return errors.New("input must be name=value")
	}

	*v = append(*v, sourcecraft.InputValue{Name: new(name), Value: new(value)})

	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("cicd", flag.ContinueOnError)
	fs.SetOutput(out)
	org := fs.String("org", "", "organization slug")
	repo := fs.String("repo", "", "repository slug")
	workflow := fs.String("workflow", "", "workflow name from .sourcecraft/ci.yaml (starts a new run)")
	branch := fs.String("branch", "", "code branch; omit to use the repository default")
	configBranch := fs.String("config-branch", "", "CI configuration branch; omit to use the repository default")
	runSlug := fs.String("run", "", "read an existing run number without starting a new one")
	timeout := fs.Duration("timeout", 10*time.Minute, "total run-wait and result retrieval timeout")
	interval := fs.Duration("interval", time.Second, "run status polling interval")

	var values inputs
	fs.Var(&values, "input", "workflow input name=value; repeat for multiple inputs")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}

		return err
	}

	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}

	if strings.TrimSpace(*org) == "" || strings.TrimSpace(*repo) == "" || *timeout <= 0 || *interval <= 0 {
		return errors.New("provide -org, -repo and positive -timeout and -interval")
	}

	if (*workflow == "") == (*runSlug == "") {
		return errors.New("provide exactly one of -workflow or -run")
	}

	if *runSlug != "" && (*branch != "" || *configBranch != "" || len(values) > 0) {
		return errors.New("-branch, -config-branch and -input apply only to new runs")
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

	var current *sourcecraft.Run

	if *runSlug == "" {
		body := &sourcecraft.RunWorkflowsBody{Workflows: []sourcecraft.WorkflowData{{Name: workflow, Values: values}}}
		if *branch != "" {
			body.Head = &sourcecraft.GitRevision{Branch: branch}
		}

		if *configBranch != "" {
			body.ConfigRevision = &sourcecraft.GitRevision{Branch: configBranch}
		}

		current, _, err = client.RunWorkflows(ctx, *org, *repo, body)
	} else {
		current, _, err = client.GetRun(ctx, *org, *repo, *runSlug)
	}

	if err != nil {
		return err
	}

	if current.Slug == "" {
		return errors.New("CI response has no run slug")
	}

	if _, err := fmt.Fprintf(out, "Run %s; resume results with -run %s\n", current.Slug, current.Slug); err != nil {
		return fmt.Errorf("CI run %s was accepted, but writing its ID failed: %w", current.Slug, err)
	}

	completed, err := waitForRun(ctx, client, *org, *repo, current, *interval, out)
	if err != nil {
		return err
	}

	resultErr := printResults(ctx, client, *org, *repo, completed, out)
	if completed.Status != sourcecraft.RunStatusSuccess {
		resultErr = errors.Join(resultErr, fmt.Errorf("CI run %s ended with status %s", completed.Slug, completed.Status))
	}

	return resultErr
}

func waitForRun(ctx context.Context, client *sourcecraft.Client, org, repo string, current *sourcecraft.Run, interval time.Duration, out io.Writer) (*sourcecraft.Run, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		if _, err := fmt.Fprintf(out, "Run %s: %s\n", current.Slug, current.Status); err != nil {
			return nil, err
		}

		switch current.Status {
		case sourcecraft.RunStatusSuccess, sourcecraft.RunStatusFailed, sourcecraft.RunStatusCanceled, sourcecraft.RunStatusTimeout, sourcecraft.RunStatusSkipped, sourcecraft.RunStatusRejected:
			return current, nil
		case sourcecraft.RunStatusCreated, sourcecraft.RunStatusPrepared, sourcecraft.RunStatusProcessing, sourcecraft.RunStatusAwaitingApproval:
		default:
			return nil, fmt.Errorf("unknown CI run status %q", current.Status)
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}

		next, _, err := client.GetRun(ctx, org, repo, current.Slug)
		if err != nil {
			return nil, err
		}

		if next.Slug != current.Slug {
			return nil, errors.New("CI response has a different or empty run slug")
		}

		current = next
	}
}

func printResults(ctx context.Context, client *sourcecraft.Client, org, repo string, run *sourcecraft.Run, out io.Writer) error {
	p := resultPrinter{ctx: ctx, client: client, org: org, repo: repo, run: run.Slug, out: out}
	for _, message := range run.ErrorMessages {
		if _, err := fmt.Fprintf(out, "Run error: %s\n", message); err != nil {
			return errors.Join(append(p.problems, err)...)
		}
	}

	for _, summary := range run.Workflows {
		if err := p.workflow(summary.Slug); err != nil {
			return errors.Join(append(p.problems, err)...)
		}
	}

	return errors.Join(p.problems...)
}

func (p *resultPrinter) workflow(slug string) error {
	workflow, _, err := p.client.GetWorkflow(p.ctx, p.org, p.repo, p.run, slug)
	if err != nil {
		p.problems = append(p.problems, err)
		return nil
	}

	if _, err := fmt.Fprintf(p.out, "Workflow %s: %s\n", workflow.Slug, workflow.Status); err != nil {
		return err
	}

	for _, task := range workflow.Tasks {
		if err := p.task(workflow.Slug, task); err != nil {
			return err
		}
	}

	return nil
}

func (p *resultPrinter) task(workflow string, task sourcecraft.Task) error {
	if _, err := fmt.Fprintf(p.out, "Task %s: %s\n", task.Slug, task.Status); err != nil {
		return err
	}

	for _, cube := range task.Cubes {
		if err := p.cube(workflow, task.Slug, cube); err != nil {
			return err
		}
	}

	return nil
}

func (p *resultPrinter) cube(workflow, task string, cube sourcecraft.Cube) error {
	if _, err := fmt.Fprintf(p.out, "Cube %s/%s/%s: %s\n", workflow, task, cube.Slug, cube.Status); err != nil {
		return err
	}

	switch cube.Status {
	case sourcecraft.RunStatusSuccess, sourcecraft.RunStatusFailed, sourcecraft.RunStatusCanceled, sourcecraft.RunStatusTimeout:
	case sourcecraft.RunStatusCreated, sourcecraft.RunStatusPrepared, sourcecraft.RunStatusAwaitingApproval, sourcecraft.RunStatusSkipped, sourcecraft.RunStatusRejected:
		return nil // Cubes that did not execute have no output to fetch.
	default:
		p.problems = append(p.problems, fmt.Errorf("cube %s has unexpected status %q; retrieve this run again with -run", cube.Slug, cube.Status))
		return nil
	}

	if err := printCubeLogs(p.ctx, p.client, p.org, p.repo, p.run, workflow, task, cube.Slug, p.out); err != nil {
		p.problems = append(p.problems, fmt.Errorf("cube %s logs: %w", cube.Slug, err))
	}

	if _, err := fmt.Fprintln(p.out); err != nil {
		return err
	}

	artifacts, _, err := p.client.GetCubeArtifacts(p.ctx, p.org, p.repo, p.run, workflow, task, cube.Slug)
	if err != nil {
		p.problems = append(p.problems, fmt.Errorf("cube %s artifacts: %w", cube.Slug, err))
		return nil
	}

	for _, artifact := range artifacts.Artifacts {
		if _, err := fmt.Fprintf(p.out, "Artifact %s [%s]: %s\n", artifact.LocalPath, artifact.Status, artifact.DownloadURL); err != nil {
			return err
		}
	}

	return nil
}

func printCubeLogs(ctx context.Context, client *sourcecraft.Client, org, repo, run, workflow, task, cube string, out io.Writer) error {
	previousDone := false

	for page := int64(1); ; page++ {
		logs, _, err := client.GetCubeLogs(ctx, org, repo, run, workflow, task, cube, &sourcecraft.GetCubeLogsOptions{Page: page})
		if err != nil {
			// A full final page has no successor. Only accept that 404 after
			// reading a complete page from a cube that has finished writing.
			if apiErr, ok := errors.AsType[*sourcecraft.APIError](err); ok && apiErr.StatusCode == http.StatusNotFound && page > 1 && previousDone {
				return nil
			}

			return err
		}

		if _, err := fmt.Fprint(out, logs.Logs); err != nil {
			return err
		}

		if !logs.PageComplete {
			if !logs.Done {
				return errors.New("logs are still being written; retrieve this run again with -run")
			}

			return nil
		}
		// Done is cube-wide; a completed earlier page may still have successors.
		previousDone = logs.Done
	}
}
