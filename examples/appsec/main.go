// Command appsec prints security defect groups visible in a repository.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/skarm/sourcecraft"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	repository := flag.String("repository", "", "repository UUID or internal numeric ID")

	flag.Parse()

	token := os.Getenv("SOURCECRAFT_TOKEN")
	if token == "" || *repository == "" {
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

		fmt.Printf("%s\t%s\t%s\n", defect.UUID, defect.RuleName, defect.FileName)
	}

	return nil
}
