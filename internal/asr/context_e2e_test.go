package asr

import (
	"context"
	"os"
	"testing"
)

// TestE2EReportUserWords 走真实网络验证 Wave 加密 + sami_token + user_words 上报。
// 默认跳过；设 VOXGATE_E2E=1 运行。
func TestE2EReportUserWords(t *testing.T) {
	if os.Getenv("VOXGATE_E2E") != "1" {
		t.Skip("set VOXGATE_E2E=1 to run live network e2e")
	}
	ctx := context.Background()
	cm := CredentialManager{Path: "/tmp/voxgate-e2e-creds.json"}
	creds, err := cm.Ensure(ctx, false)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	cc := NewContextClient(creds, DefaultDevice, nil)
	if err := cc.ReportUserWords(ctx, []string{"voxgate", "VoxGate"}); err != nil {
		t.Fatalf("report user words: %v", err)
	}
	t.Logf("reported OK, did=%s", creds.DeviceID)
}
