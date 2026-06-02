package asr

const (
	ServiceNameASR = "ASR"

	MethodStartTask     = "StartTask"
	MethodStartSession  = "StartSession"
	MethodTaskRequest   = "TaskRequest"
	MethodFinishSession = "FinishSession"

	MessageTaskStarted     = "TaskStarted"
	MessageSessionStarted  = "SessionStarted"
	MessageTaskFailed      = "TaskFailed"
	MessageSessionFailed   = "SessionFailed"
	MessageSessionFinished = "SessionFinished"

	AudioFormatSpeechOpus = "speech_opus"
	AudioFormatRaw        = "raw"

	UpstreamSampleRate      = 16000
	UpstreamChannels        = 1
	UpstreamFrameDurationMS = 20
	UpstreamBytesPerFrame   = UpstreamSampleRate * UpstreamFrameDurationMS / 1000 * 2
)

const (
	AppName            = "oime"
	Package            = "com.bytedance.android.doubaoime"
	ContextVersionName = "1.3.9"
	ContextVersionCode = 100309006
)

const (
	GetConfigURL = "https://ime.oceancloudapi.com/api/v1/user/get_config"
	HandshakeURL = "https://keyhub.zijieapi.com/handshake"
	UserWordsURL = "https://speech.bytedance.com/api/v3/context/ime/user_words"

	SamiAppKey        = "SYlxZr6LnvBaIVmF"
	ContextResourceID = "asr.user.context"

	waveHKDFInfo       = "4e30514609050cd3"
	waveCipherChaCha20 = 4097
)
