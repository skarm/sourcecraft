// Command repositories prints all repositories visible to the authenticated user.
package main

import (
	"context"
	"errors"
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
	token := os.Getenv("SOURCECRAFT_TOKEN")
	if token == "" {
		return errors.New("set SOURCECRAFT_TOKEN to a SourceCraft personal access token")
	}

	client, err := sourcecraft.NewClient(token)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	fetch := func(ctx context.Context, token string) ([]sourcecraft.Repository, string, error) {
		page, _, err := client.ListMyRepositories(ctx, &sourcecraft.ListMyRepositoriesOptions{PageSize: 100, PageToken: token})
		if err != nil {
			return nil, "", err
		}

		return page.Repositories, page.NextPageToken, nil
	}
	for repository, err := range sourcecraft.Paginate(ctx, fetch) {
		if err != nil {
			return err
		}

		fmt.Printf("%s\t%s\n", repository.Slug, repository.WebURL)
	}

	return nil
}
