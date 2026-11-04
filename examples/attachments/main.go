// Command attachments streams a local file to an issue and retrieves its download URL.
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
	"path/filepath"
	"strings"
	"time"

	"github.com/skarm/sourcecraft"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("attachments", flag.ContinueOnError)
	fs.SetOutput(out)
	org := fs.String("org", "", "organization slug")
	repo := fs.String("repo", "", "repository slug")
	issue := fs.String("issue", "", "existing issue number (slug)")
	path := fs.String("file", "", "local file to upload")

	timeout := fs.Duration("timeout", 2*time.Minute, "total upload and metadata request timeout")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}

		return err
	}

	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}

	if strings.TrimSpace(*org) == "" || strings.TrimSpace(*repo) == "" || strings.TrimSpace(*issue) == "" || *path == "" || *timeout <= 0 {
		return errors.New("provide -org, -repo, -issue, -file and a positive -timeout")
	}

	token := os.Getenv("SOURCECRAFT_TOKEN")
	if token == "" {
		return errors.New("set SOURCECRAFT_TOKEN to a personal access token")
	}

	file, err := os.Open(*path)
	if err != nil {
		return err
	}
	defer func() {
		// This read-only file remains owned by the caller on every return path.
		_ = file.Close()
	}()

	info, err := file.Stat()
	if err != nil {
		return err
	}

	if info.IsDir() {
		return errors.New("-file must refer to a file, not a directory")
	}

	client, err := sourcecraft.NewClient(token)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	attachment, _, err := client.UploadIssueAttachment(ctx, *org, *repo, *issue, &sourcecraft.Upload{Filename: filepath.Base(*path), Reader: file})
	if err != nil {
		return fmt.Errorf("upload issue attachment: %w", err)
	}

	if _, err := fmt.Fprintf(out, "Uploaded: id=%s name=%s bytes=%d\n", attachment.ID, attachment.Name, attachment.Size); err != nil {
		return fmt.Errorf("attachment %s was uploaded, but writing its metadata failed: %w", attachment.ID, err)
	}

	if attachment.ID == "" {
		return errors.New("upload response has no attachment ID")
	}

	download, _, err := client.GetIssueAttachment(ctx, *org, *repo, *issue, attachment.ID)
	if err != nil {
		return fmt.Errorf("attachment %s was uploaded, but its download URL could not be retrieved: %w", attachment.ID, err)
	}

	_, err = fmt.Fprintf(out, "Temporary download URL: %s\n", download.URL)

	return err
}
