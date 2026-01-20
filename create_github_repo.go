//go:build ignore

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const (
	githubAPIURL = "https://api.github.com"
	repoName     = "Buh_Chat_bot"
)

type createRepoRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Private     bool   `json:"private,omitempty"`
}

type repoResponse struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	FullName    string `json:"full_name"`
	CloneURL    string `json:"clone_url"`
	SSHURL      string `json:"ssh_url"`
	HTMLURL     string `json:"html_url"`
	Private     bool   `json:"private"`
	Description string `json:"description"`
}

func main() {
	var (
		token   = flag.String("token", "", "GitHub personal access token (required)")
		private = flag.Bool("private", false, "Create private repository")
		desc    = flag.String("desc", "Bug Chat Bot repository", "Repository description")
		timeout = flag.Duration("timeout", 30*time.Second, "Request timeout")
	)
	flag.Parse()

	if *token == "" {
		tokenEnv := os.Getenv("GITHUB_TOKEN")
		if tokenEnv == "" {
			fmt.Fprintf(os.Stderr, "Error: GitHub token is required\n")
			fmt.Fprintf(os.Stderr, "Usage: %s -token YOUR_TOKEN [-private] [-desc DESCRIPTION]\n", os.Args[0])
			fmt.Fprintf(os.Stderr, "Or set GITHUB_TOKEN environment variable\n")
			os.Exit(1)
		}
		*token = tokenEnv
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	reqBody := createRepoRequest{
		Name:        repoName,
		Description: *desc,
		Private:     *private,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error marshaling request: %v\n", err)
		os.Exit(1)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", githubAPIURL+"/user/repos", bytes.NewBuffer(jsonData))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating request: %v\n", err)
		os.Exit(1)
	}

	req.Header.Set("Authorization", "token "+*token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: *timeout}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error making request: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading response: %v\n", err)
		os.Exit(1)
	}

	if resp.StatusCode != http.StatusCreated {
		fmt.Fprintf(os.Stderr, "Error: GitHub API returned status %d\n", resp.StatusCode)
		fmt.Fprintf(os.Stderr, "Response: %s\n", string(body))
		os.Exit(1)
	}

	var repo repoResponse
	if err := json.Unmarshal(body, &repo); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
		fmt.Fprintf(os.Stderr, "Response: %s\n", string(body))
		os.Exit(1)
	}

	fmt.Printf("✅ Repository created successfully!\n\n")
	fmt.Printf("Repository: %s\n", repo.FullName)
	fmt.Printf("URL: %s\n", repo.HTMLURL)
	fmt.Printf("Clone URL: %s\n", repo.CloneURL)
	fmt.Printf("SSH URL: %s\n", repo.SSHURL)
	fmt.Printf("Private: %v\n", repo.Private)
	if repo.Description != "" {
		fmt.Printf("Description: %s\n", repo.Description)
	}
}
