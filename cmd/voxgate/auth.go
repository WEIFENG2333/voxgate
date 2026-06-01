package main

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/WEIFENG2333/voxgate/internal/asr"
	"github.com/WEIFENG2333/voxgate/internal/config"
)

func auth(cfg config.Config) int {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	creds, err := (asr.CredentialManager{Path: cfg.CredentialPath, UserAgent: cfg.ASR.UserAgent}).Ensure(ctx, true)
	if err != nil {
		printErr("auth_error", err)
		return 3
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"device_id": creds.DeviceID, "credential_path": cfg.CredentialPath})
	return 0
}
