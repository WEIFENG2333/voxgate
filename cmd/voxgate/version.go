package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"
)

func versionCmd(args []string) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	check := fs.Bool("check", false, "check latest GitHub release")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	fmt.Println("voxgate", version)
	if !*check {
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	latest, err := latestRelease(ctx)
	if err != nil {
		printErr("version_check_error", err)
		return 1
	}
	fmt.Println("latest", latest)
	if sameVersion(version, latest) {
		fmt.Println("status up-to-date")
		return 0
	}
	fmt.Println("status update-available")
	fmt.Println("upgrade", installCommand())
	return 0
}

func updateCmd(args []string) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		printErr("invalid_args", fmt.Errorf("usage: voxgate update"))
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	latest, err := latestRelease(ctx)
	if err != nil {
		printErr("version_check_error", err)
		return 1
	}
	fmt.Println("current", version)
	fmt.Println("latest", latest)
	if sameVersion(version, latest) {
		fmt.Println("status up-to-date")
		return 0
	}
	fmt.Println("status update-available")
	fmt.Println("run", installCommand())
	return 0
}

func latestRelease(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "voxgate/"+version)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub latest release returned %s", resp.Status)
	}

	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.TagName == "" {
		return "", fmt.Errorf("latest release has no tag_name")
	}
	return body.TagName, nil
}

func sameVersion(current, latest string) bool {
	return strings.TrimPrefix(current, "v") == strings.TrimPrefix(latest, "v")
}

func installCommand() string {
	if runtime.GOOS == "windows" {
		return "irm https://raw.githubusercontent.com/" + repo + "/main/scripts/install.ps1 | iex"
	}
	return "curl -fsSL https://raw.githubusercontent.com/" + repo + "/main/scripts/install.sh | sh"
}
