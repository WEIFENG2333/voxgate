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
	switch compareVersion(version, latest) {
	case 0:
		fmt.Println("status up-to-date")
		return 0
	case 1:
		fmt.Println("status newer-than-latest")
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
	switch compareVersion(version, latest) {
	case 0:
		fmt.Println("status up-to-date")
		return 0
	case 1:
		fmt.Println("status newer-than-latest")
		return 0
	}
	fmt.Println("status update-available")
	fmt.Println("run", installCommand())
	return 0
}

func latestRelease(ctx context.Context) (string, error) {
	tag, err := latestReleaseFromAPI(ctx)
	if err == nil {
		return tag, nil
	}
	fallbackTag, fallbackErr := latestReleaseFromRedirect(ctx)
	if fallbackErr == nil {
		return fallbackTag, nil
	}
	return "", fmt.Errorf("%w; fallback failed: %v", err, fallbackErr)
}

func latestReleaseFromAPI(ctx context.Context) (string, error) {
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

func latestReleaseFromRedirect(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://github.com/"+repo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "voxgate/"+version)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.Request == nil || resp.Request.URL == nil {
		return "", fmt.Errorf("GitHub latest release redirect had no final URL")
	}
	finalURL := resp.Request.URL.String()
	const marker = "/releases/tag/"
	i := strings.LastIndex(finalURL, marker)
	if i < 0 {
		return "", fmt.Errorf("GitHub latest release redirect did not end at a tag")
	}
	tag := finalURL[i+len(marker):]
	if j := strings.IndexAny(tag, "?#/"); j >= 0 {
		tag = tag[:j]
	}
	if tag == "" {
		return "", fmt.Errorf("GitHub latest release redirect had empty tag")
	}
	return tag, nil
}

func sameVersion(current, latest string) bool {
	return compareVersion(current, latest) == 0
}

func compareVersion(current, latest string) int {
	currentParts := versionParts(current)
	latestParts := versionParts(latest)
	for i := 0; i < len(currentParts) || i < len(latestParts); i++ {
		var currentPart, latestPart int
		if i < len(currentParts) {
			currentPart = currentParts[i]
		}
		if i < len(latestParts) {
			latestPart = latestParts[i]
		}
		if currentPart < latestPart {
			return -1
		}
		if currentPart > latestPart {
			return 1
		}
	}
	return 0
}

func versionParts(v string) []int {
	v = strings.TrimPrefix(v, "v")
	fields := strings.Split(v, ".")
	parts := make([]int, 0, len(fields))
	for _, field := range fields {
		var part int
		for _, r := range field {
			if r < '0' || r > '9' {
				break
			}
			part = part*10 + int(r-'0')
		}
		parts = append(parts, part)
	}
	return parts
}

func installCommand() string {
	if runtime.GOOS == "windows" {
		return "irm https://raw.githubusercontent.com/" + repo + "/main/scripts/install.ps1 | iex"
	}
	return "curl -fsSL https://raw.githubusercontent.com/" + repo + "/main/scripts/install.sh | sh"
}
