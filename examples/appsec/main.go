// Command appsec prints security defect groups visible in a repository.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
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
	fs := flag.NewFlagSet("appsec", flag.ContinueOnError)
	fs.SetOutput(out)
	repository := fs.String("repository", "", "repository UUID or internal numeric ID")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}

		return err
	}

	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}

	token := os.Getenv("SOURCECRAFT_TOKEN")
	if token == "" || strings.TrimSpace(*repository) == "" {
		return errors.New("set SOURCECRAFT_TOKEN and pass -repository <repository UUID or ID>")
	}

	client, err := sourcecraft.NewAppSecClient(token)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	fetch := func(ctx context.Context, token string) ([]*sourcecraft.AppSecDefectGroupDTO, string, error) {
		page, _, err := client.ListDefectGroups(ctx, &sourcecraft.AppSecListDefectGroupsOptions{GitRepo: *repository, PageSize: 100, PageToken: token})
		if err != nil {
			return nil, "", err
		}

		return page.Data, page.NextPageToken, nil
	}
	for defect, err := range sourcecraft.Paginate(ctx, fetch) {
		if err != nil {
			return err
		}

		if _, err := fmt.Fprintf(out, "%s\t%s\t%s\n", defect.UUID, defect.RuleName, defect.FileName); err != nil {
			return err
		}
	}

	return nil
}
