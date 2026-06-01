package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/WEIFENG2333/voxgate/internal/asr"
	"github.com/WEIFENG2333/voxgate/internal/audio"
	"github.com/WEIFENG2333/voxgate/internal/config"
)

func doctor(cfg config.Config) int {
	ok := true
	check := func(name string, err error) {
		if err != nil {
			ok = false
			fmt.Fprintf(os.Stderr, "FAIL %s: %v\n", name, err)
			return
		}
		fmt.Fprintf(os.Stderr, "OK   %s\n", name)
	}

	if path := os.Getenv("VOXGATE_FFMPEG"); path != "" {
		_, err := os.Stat(path)
		check("ffmpeg", err)
	} else {
		_, err := exec.LookPath("ffmpeg")
		check("ffmpeg", err)
	}

	_, err := audio.NewOpusEncoder()
	check("libopus", err)

	_, err = asr.LoadCredentials(cfg.CredentialPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARN credentials: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "OK   credentials\n")
	}
	if ok {
		return 0
	}
	return 1
}
