package asr

import (
	"fmt"
	"strings"
)

// DeviceProfile 设备画像，用于设备注册与会话上报。
// 用真实机型更贴近真实流量；服务端不校验机型，可替换。
type DeviceProfile struct {
	Model        string // 机型 (Build.MODEL)
	Brand        string // 品牌 (Build.BRAND)
	Manufacturer string // 厂商 (Build.MANUFACTURER)
	OSVersion    string // 系统版本 (Build.VERSION.RELEASE)
	OSAPI        int    // SDK 版本 (Build.VERSION.SDK_INT)
	Resolution   string // 分辨率 "宽*高"
	DPI          string // 屏幕密度
	CPUABI       string // CPU 架构
	ROMBuild     string // 构建号 (Build.ID)
}

// UserAgent 生成请求 User-Agent。
func (d DeviceProfile) UserAgent() string {
	return fmt.Sprintf(
		"com.bytedance.android.doubaoime/%d (Linux; U; Android %s; zh_CN; %s; Build/%s; Cronet/TTNetVersion:94cf429a 2025-11-17 QuicVersion:1f89f732 2025-05-08)",
		VersionCode, d.OSVersion, d.Model, d.ROMBuild)
}

// 预置机型
var (
	DeviceXiaomi14 = DeviceProfile{
		Model: "23127PN0CC", Brand: "Xiaomi", Manufacturer: "Xiaomi",
		OSVersion: "14", OSAPI: 34, Resolution: "1200*2670", DPI: "480",
		CPUABI: "arm64-v8a", ROMBuild: "UKQ1.230804.001",
	}
	DeviceSamsungS24Ultra = DeviceProfile{
		Model: "SM-S9280", Brand: "samsung", Manufacturer: "samsung",
		OSVersion: "14", OSAPI: 34, Resolution: "1440*3120", DPI: "505",
		CPUABI: "arm64-v8a", ROMBuild: "UP1A.231005.007",
	}
	DevicePixel8Pro = DeviceProfile{
		Model: "Pixel 8 Pro", Brand: "google", Manufacturer: "Google",
		OSVersion: "14", OSAPI: 34, Resolution: "1344*2992", DPI: "480",
		CPUABI: "arm64-v8a", ROMBuild: "UQ1A.240105.004",
	}

	DefaultDevice = DeviceXiaomi14 // 默认
)

// DeviceByName 按名选预置机型，未知名回退默认。
func DeviceByName(name string) DeviceProfile {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "samsung", "s24", "s24ultra", "galaxy":
		return DeviceSamsungS24Ultra
	case "pixel", "pixel8", "pixel8pro", "google":
		return DevicePixel8Pro
	default:
		return DeviceXiaomi14
	}
}

// EndpointURL 把端点名（ws / quic）解析为 WebSocket URL。
func EndpointURL(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "quic":
		return EndpointQUIC
	default:
		return EndpointWS
	}
}
