package asr

import "time"

// 应用标识
const (
	AID          = 401734
	AppName      = "oime"
	Package      = "com.bytedance.android.doubaoime"
	VersionName  = "1.3.9"
	VersionCode  = 100309006
	ProtoVersion = "v2" // WS 握手 header proto-version

	// BuiltinASRAppKey 是语音链路的内置 appKey，与 settings 下发的 bot appKey 不同。
	BuiltinASRAppKey = "OrnqKvSSrs"
)

// 鉴权与动态配置接口
const (
	RegisterURL = "https://log.snssdk.com/service/2/device_register/"
	SettingsURL = "https://is.snssdk.com/service/settings/v3/"

	settingsRefreshInterval = 12 * time.Hour
)

// 语音个性化接口（context）与 Wave 应用层加密
const (
	GetConfigURL = "https://ime.oceancloudapi.com/api/v1/user/get_config" // 拉取 sami_token
	HandshakeURL = "https://keyhub.zijieapi.com/handshake"                // Wave 握手
	UserWordsURL = "https://speech.bytedance.com/api/v3/context/ime/user_words"

	SamiAppKey        = "SYlxZr6LnvBaIVmF" // context 接口 appKey（区别于内置 ASR appKey）
	ContextResourceID = "asr.user.context"

	waveHKDFInfo       = "4e30514609050cd3" // Wave HKDF info 参数
	waveCipherChaCha20 = 4097               // Wave cipher_suite：ChaCha20
)

// WebSocket 端点（两个，行为一致，任选其一）
const (
	EndpointWS   = "wss://frontier-audio-ime-ws.doubao.com/ocean/api/v1/ws" // 纯 WS
	EndpointQUIC = "wss://frontier-audio-ime-quic.doubao.com/api/v1/ws"     // QUIC

	WebSocketURL = EndpointWS // 默认端点
)

// SAMI 协议：服务、方法、消息
const (
	ServiceNameASR = "ASR"

	MethodStartTask     = "StartTask"     // 建立任务
	MethodStartSession  = "StartSession"  // 声明音频格式与识别选项
	MethodTaskRequest   = "TaskRequest"   // 携带音频帧
	MethodFinishSession = "FinishSession" // 结束音频上传

	MessageTaskStarted     = "TaskStarted"
	MessageSessionStarted  = "SessionStarted"
	MessageTaskFailed      = "TaskFailed"
	MessageSessionFailed   = "SessionFailed"
	MessageSessionFinished = "SessionFinished"

	StatusCodeOK = 20000000 // 成功状态码
)

// 音频规格：16kHz 单声道，10ms/帧
const (
	AudioFormatSpeechOpus = "speech_opus" // opus 编码（默认）
	AudioFormatRaw        = "raw"         // 16bit PCM 裸流

	UpstreamSampleRate      = 16000
	UpstreamChannels        = 1
	UpstreamFrameDurationMS = 10
	UpstreamBytesPerFrame   = UpstreamSampleRate * UpstreamFrameDurationMS / 1000 * 2
)

// DefaultUserAgent 由默认设备画像生成。
var DefaultUserAgent = DefaultDevice.UserAgent()
