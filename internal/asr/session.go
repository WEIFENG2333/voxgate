package asr

// SessionConfig 是 StartSession 的全部业务参数。
type SessionConfig struct {
	DeviceID string        // extra.did（来自 device_register）
	Device   DeviceProfile // 设备画像（上报 model/brand/os_version）

	// 音频信息
	AudioFormat string // "raw"(16bit PCM) | "speech_opus"(官方默认,需 opus 编码)
	SampleRate  int    // 16000
	Channel     int    // 1

	// 顶层开关
	EnablePunctuation     bool // 自动标点
	EnableSpeechRejection bool // 拒识非语音

	// 识别能力
	EnableASRTwoPass   bool // 两遍识别（流式 + 重打分）
	EnableASRThreePass bool // 三遍识别（最高精度）
	UseTwoPassRetry    bool // twopass 失败重试
	StrongDDC          bool // 强文本规整（口语→书面、数字/单位归一）
	EnableTextFilter   bool // 文本过滤
	EnablePrintChinese bool // 调试

	// 文本后处理
	EndSmoothWindowMS        int  // VAD 句尾平滑窗(ms)
	RemoveSpaceBetweenHanNum bool // 汉字-数字去空格
	RemoveSpaceBetweenHanEng bool // 汉字-英文去空格
	DisableUserWords         bool // 禁用个人词库（false=启用云端个性化）

	// 上下文 / 透传
	InputMode string // 输入入口标识
	Context   string // 上文文本（纠错）
	ASRParams string // 服务端透传参数（settings 下发）

	// 蜂窝/网络
	CellCompressRate              int // 蜂窝压缩档位
	SwitchNetworkQualityThreshold int
	SwitchNetworkRTTThreshold     int
	SwitchNetworkPingTimeout      int

	JoinUserExperienceImproveProgram bool
}

// DefaultSessionConfig 返回默认会话配置。
func DefaultSessionConfig(deviceID string, device DeviceProfile) SessionConfig {
	return SessionConfig{
		DeviceID:                         deviceID,
		Device:                           device,
		AudioFormat:                      AudioFormatSpeechOpus, // 默认 opus，可切 raw
		SampleRate:                       UpstreamSampleRate,
		Channel:                          UpstreamChannels,
		EnablePunctuation:                true,
		EnableSpeechRejection:            false,
		EnableASRTwoPass:                 true,
		EnableASRThreePass:               true,
		UseTwoPassRetry:                  true,
		StrongDDC:                        true,
		EnableTextFilter:                 false,
		EnablePrintChinese:               false,
		EndSmoothWindowMS:                800,
		RemoveSpaceBetweenHanNum:         true,
		RemoveSpaceBetweenHanEng:         true,
		DisableUserWords:                 false,
		InputMode:                        "",
		Context:                          "",
		ASRParams:                        "",
		CellCompressRate:                 8,
		SwitchNetworkQualityThreshold:    4,
		SwitchNetworkRTTThreshold:        273,
		SwitchNetworkPingTimeout:         1000,
		JoinUserExperienceImproveProgram: false,
	}
}

// ApplyDynamic 用 settings 下发的 asr_config 覆盖可动态配置的字段
func (s *SessionConfig) ApplyDynamic(dyn map[string]any) {
	if dyn == nil {
		return
	}
	if v, ok := intFrom(dyn["cell_compress_rate"]); ok {
		s.CellCompressRate = v
	}
	if v, ok := intFrom(dyn["switch_network_quality_threshold"]); ok {
		s.SwitchNetworkQualityThreshold = v
	}
	if v, ok := intFrom(dyn["switch_network_rtt_threshold"]); ok {
		s.SwitchNetworkRTTThreshold = v
	}
	if v, ok := intFrom(dyn["switch_network_ping_timeout"]); ok {
		s.SwitchNetworkPingTimeout = v
	}
	if v, ok := dyn["asr_params"].(string); ok && v != "" {
		s.ASRParams = v
	}
}

// ToPayload 生成 StartSession 的 payload（结构按 SAMI 协议组织）。
func (s SessionConfig) ToPayload() map[string]any {
	return map[string]any{
		"audio_info": map[string]any{
			"format":      s.AudioFormat,
			"sample_rate": s.SampleRate,
			"channel":     s.Channel,
		},
		"enable_punctuation":      s.EnablePunctuation,
		"enable_speech_rejection": s.EnableSpeechRejection,
		"extra": map[string]any{
			"app_name":                     Package,
			"did":                          s.DeviceID,
			"enable_asr_twopass":           s.EnableASRTwoPass,
			"enable_asr_threepass":         s.EnableASRThreePass,
			"use_twopass_retry":            s.UseTwoPassRetry,
			"strong_ddc":                   s.StrongDDC,
			"enable_text_filter":           s.EnableTextFilter,
			"enable_print_chinese":         s.EnablePrintChinese,
			"end_smooth_window_ms":         s.EndSmoothWindowMS,
			"remove_space_between_han_num": s.RemoveSpaceBetweenHanNum,
			"remove_space_between_han_eng": s.RemoveSpaceBetweenHanEng,
			"disable_user_words":           s.DisableUserWords,
			"input_mode":                   s.InputMode,
			"context":                      s.Context,
			"asr_params":                   s.ASRParams,
			"cell_compress_rate":           s.CellCompressRate,
			"network_change": map[string]any{
				"switch_network_quality_threshold": s.SwitchNetworkQualityThreshold,
				"switch_network_rtt_threshold":     s.SwitchNetworkRTTThreshold,
				"switch_network_ping_timeout":      s.SwitchNetworkPingTimeout,
			},
			"model":                                s.Device.Model,
			"brand":                                s.Device.Brand,
			"os_version":                           s.Device.OSVersion,
			"os_type":                              "Android",
			"app_version":                          VersionName,
			"join_user_experience_improve_program": s.JoinUserExperienceImproveProgram,
		},
	}
}

func intFrom(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), true
	case int:
		return x, true
	default:
		return 0, false
	}
}
