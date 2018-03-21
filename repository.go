package dewy

import (
	"context"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path"

	"github.com/google/go-github/github"
	"golang.org/x/oauth2"
)

type Repository interface {
	Fetch() error
	Download() error
}

type GithubReleaseRepository struct {
	token       string
	endpoint    string
	owner       string
	name        string
	artifact    string
	downloadURL string
}

func NewRepository(c RepositoryConfig) Repository {
	switch c.Provider {
	case GITHUB:
		return &GithubReleaseRepository{
			token:    c.Token,
			endpoint: c.Endpoint,
			owner:    c.Owner,
			name:     c.Name,
			artifact: c.Artifact,
		}
	default:
		panic("no repository provider")
	}
}

func (g *GithubReleaseRepository) Fetch() error {
	ctx := context.Background()
	c, err := g.client(ctx)
	if err != nil {
		return err
	}
	release, _, err := c.Repositories.GetLatestRelease(ctx, g.owner, g.name)
	if err != nil {
		return err
	}
	for _, v := range release.Assets {
		if *v.Name == g.artifact {
			g.downloadURL = *v.BrowserDownloadURL
			break
		}
		//fmt.Printf("%s -- Size: %d, Download: %d <%s>\n", *v.Name, *v.Size, *v.DownloadCount, *v.BrowserDownloadURL)
	}
	return nil
}

func (g *GithubReleaseRepository) Download() error {
	res, err := http.Get(g.downloadURL)
	if err != nil {
		return err
	}

	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return err
	}

	_, filename := path.Split(g.downloadURL)
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	file.Write(body)

	return nil
}

func (g *GithubReleaseRepository) client(ctx context.Context) (*github.Client, error) {
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: g.token},
	)
	tc := oauth2.NewClient(ctx, ts)
	client := github.NewClient(tc)

	if g.endpoint != "" {
		url, err := url.Parse(g.endpoint)
		if err != nil {
			return nil, err
		}
		client.BaseURL = url
	}

	return client, nil
}