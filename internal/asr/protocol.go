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
