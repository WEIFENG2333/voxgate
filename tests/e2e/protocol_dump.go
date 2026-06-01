//go:build ignore

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/WEIFENG2333/voxgate/internal/asr"
	"github.com/WEIFENG2333/voxgate/internal/audio"
	asrproto "github.com/WEIFENG2333/voxgate/internal/proto"
)

func main() {
	audioPath := flag.String("audio", "tests/audio/zh_5s.wav", "audio file to send")
	credentialPath := flag.String("credential-path", "", "credential cache path")
	maxFrames := flag.Int("max-frames", 0, "limit audio frames; 0 sends the whole file")
	probeContinuation := flag.Bool("probe-continuation", false, "send two utterances in one session without FinishSession between them")
	silenceFrames := flag.Int("silence-frames", 150, "silence frames after each utterance for continuation probe")
	flag.Parse()

	if *credentialPath == "" {
		*credentialPath = fmt.Sprintf("%s/voxgate-protocol-dump-%d.json", os.TempDir(), time.Now().UnixNano())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	emit("probe.start", map[string]any{
		"audio":           *audioPath,
		"credential_path": *credentialPath,
	})

	manager := asr.CredentialManager{Path: *credentialPath, UserAgent: asr.DefaultUserAgent}
	creds, err := manager.Ensure(ctx, true)
	if err != nil {
		log.Fatal(err)
	}
	emit("credentials", map[string]any{
		"device_id":  creds.DeviceID,
		"install_id": creds.InstallID,
		"has_token":  creds.Token != "",
		"cdid_len":   len(creds.CDID),
	})

	src, err := audio.ConvertFile(ctx, *audioPath)
	if err != nil {
		log.Fatal(err)
	}
	defer src.Close()
	enc, err := audio.NewOpusEncoder()
	if err != nil {
		log.Fatal(err)
	}
	defer enc.Close()

	u, err := url.Parse(asr.WebSocketURL)
	if err != nil {
		log.Fatal(err)
	}
	q := u.Query()
	q.Set("aid", strconv.Itoa(asr.AID))
	q.Set("device_id", creds.DeviceID)
	u.RawQuery = q.Encode()
	header := http.Header{}
	header.Set("User-Agent", asr.DefaultUserAgent)
	header.Set("proto-version", "v2")
	header.Set("x-custom-keepalive", "true")
	emit("ws.connect.request", map[string]any{
		"url":     u.String(),
		"headers": header,
	})
	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, u.String(), header)
	if err != nil {
		if resp != nil {
			emit("ws.connect.error", map[string]any{"status": resp.Status, "headers": resp.Header})
		}
		log.Fatal(err)
	}
	defer conn.Close()
	if resp != nil {
		emit("ws.connect.response", map[string]any{"status": resp.Status, "headers": resp.Header})
	}

	requestID := uuid.NewString()
	send(conn, "StartTask", asrproto.Request{Token: creds.Token, ServiceName: "ASR", MethodName: "StartTask", RequestID: requestID})
	read(conn, "StartTask")

	sessionPayload, _ := json.Marshal(map[string]any{
		"audio_info":              map[string]any{"channel": 1, "format": "speech_opus", "sample_rate": 16000},
		"enable_punctuation":      true,
		"enable_speech_rejection": false,
		"extra": map[string]any{
			"app_name": "com.android.chrome", "cell_compress_rate": 8, "did": creds.DeviceID,
			"enable_asr_threepass": true, "enable_asr_twopass": true, "input_mode": "tool",
		},
	})
	send(conn, "StartSession", asrproto.Request{Token: creds.Token, ServiceName: "ASR", MethodName: "StartSession", Payload: string(sessionPayload), RequestID: requestID})
	read(conn, "StartSession")

	timestamp := time.Now().UnixMilli()
	frameIndex := 0
	if *probeContinuation {
		sent, err := sendSourceFrames(conn, enc, src.Clone(), requestID, timestamp, frameIndex, *maxFrames, true)
		if err != nil {
			log.Fatal(err)
		}
		frameIndex += sent
		sent, err = sendSilenceFrames(conn, enc, requestID, timestamp, frameIndex, *silenceFrames, asrproto.FrameStateMiddle)
		if err != nil {
			log.Fatal(err)
		}
		frameIndex += sent
		emit("continuation.first_audio_sent", map[string]any{"frames_total": frameIndex})
		first, err := readUntilFinal(conn, "continuation.first")
		if err != nil {
			emit("continuation.first.error", map[string]any{"error": err.Error()})
			return
		}
		emit("continuation.first.final", map[string]any{"text": first})

		sent, err = sendSourceFrames(conn, enc, src.Clone(), requestID, timestamp, frameIndex, *maxFrames, false)
		if err != nil {
			emit("continuation.second_send.error", map[string]any{"error": err.Error()})
			return
		}
		frameIndex += sent
		sent, err = sendSilenceFrames(conn, enc, requestID, timestamp, frameIndex, *silenceFrames, asrproto.FrameStateMiddle)
		if err != nil {
			emit("continuation.second_silence.error", map[string]any{"error": err.Error()})
			return
		}
		frameIndex += sent
		emit("continuation.second_audio_sent", map[string]any{"frames_total": frameIndex})
		second, err := readUntilFinal(conn, "continuation.second")
		if err != nil {
			emit("continuation.second.error", map[string]any{"error": err.Error()})
			return
		}
		emit("continuation.second.final", map[string]any{"text": second})
		send(conn, "FinishSession", asrproto.Request{Token: creds.Token, ServiceName: "ASR", MethodName: "FinishSession", RequestID: requestID})
		emit("probe.done", map[string]any{"mode": "continuation", "frames": frameIndex})
		return
	}
	sent, err := sendSourceFrames(conn, enc, src, requestID, timestamp, frameIndex, *maxFrames, true)
	if err != nil {
		log.Fatal(err)
	}
	frameIndex += sent
	if frameIndex > 0 {
		_, err := sendSilenceFrames(conn, enc, requestID, timestamp, frameIndex, 1, asrproto.FrameStateLast)
		if err != nil {
			log.Fatal(err)
		}
	}
	send(conn, "FinishSession", asrproto.Request{Token: creds.Token, ServiceName: "ASR", MethodName: "FinishSession", RequestID: requestID})
	emit("audio.sent", map[string]any{"frames": frameIndex, "duration_seconds": src.Duration().Seconds()})

	for {
		resp, raw, err := read(conn, "recv")
		if err != nil {
			emit("recv.error", map[string]any{"error": err.Error()})
			return
		}
		if resp.MessageType == "SessionFinished" || resp.MessageType == "TaskFailed" || resp.MessageType == "SessionFailed" {
			emit("probe.done", map[string]any{"last_message_type": resp.MessageType, "raw_len": len(raw)})
			return
		}
	}
}

func sendSourceFrames(conn *websocket.Conn, enc *audio.OpusEncoder, src *audio.Source, requestID string, timestamp int64, startFrame, maxFrames int, first bool) (int, error) {
	sent := 0
	for {
		if maxFrames > 0 && sent >= maxFrames {
			break
		}
		pcm, ok, err := src.NextFrame()
		if err != nil {
			return sent, err
		}
		if !ok {
			break
		}
		state := asrproto.FrameStateMiddle
		if first && sent == 0 {
			state = asrproto.FrameStateFirst
		}
		if err := sendPCMFrame(conn, enc, requestID, timestamp, startFrame+sent, pcm, state); err != nil {
			return sent, err
		}
		sent++
	}
	return sent, nil
}

func sendSilenceFrames(conn *websocket.Conn, enc *audio.OpusEncoder, requestID string, timestamp int64, startFrame, count int, state asrproto.FrameState) (int, error) {
	for i := 0; i < count; i++ {
		if err := sendPCMFrame(conn, enc, requestID, timestamp, startFrame+i, make([]byte, audio.BytesPerFrame), state); err != nil {
			return i, err
		}
	}
	return count, nil
}

func sendPCMFrame(conn *websocket.Conn, enc *audio.OpusEncoder, requestID string, timestamp int64, frameIndex int, pcm []byte, state asrproto.FrameState) error {
	opusFrame, err := enc.EncodePCMFrame(pcm)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"extra": map[string]any{}, "timestamp_ms": timestamp + int64(frameIndex*20)})
	req := asrproto.Request{ServiceName: "ASR", MethodName: "TaskRequest", Payload: string(payload), AudioData: opusFrame, RequestID: requestID, FrameState: state}
	if frameIndex < 3 || frameIndex%100 == 0 || state == asrproto.FrameStateLast {
		emit("TaskRequest.request", requestSummary(req))
	}
	return conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalRequest(req))
}

func readUntilFinal(conn *websocket.Conn, stage string) (string, error) {
	deadline := time.Now().Add(30 * time.Second)
	_ = conn.SetReadDeadline(deadline)
	defer conn.SetReadDeadline(time.Time{})
	for {
		resp, _, err := read(conn, stage)
		if err != nil {
			return "", err
		}
		if resp.MessageType == "SessionFinished" || resp.MessageType == "TaskFailed" || resp.MessageType == "SessionFailed" {
			return "", fmt.Errorf("unexpected %s: %s", resp.MessageType, resp.StatusMessage)
		}
		text, final := finalText(resp.ResultJSON)
		if final {
			return text, nil
		}
	}
}

func finalText(resultJSON string) (string, bool) {
	var r struct {
		Results []struct {
			Text          string `json:"text"`
			IsInterim     *bool  `json:"is_interim"`
			IsVADFinished bool   `json:"is_vad_finished"`
			Extra         struct {
				NonstreamResult bool `json:"nonstream_result"`
			} `json:"extra"`
		} `json:"results"`
	}
	if resultJSON == "" || json.Unmarshal([]byte(resultJSON), &r) != nil {
		return "", false
	}
	var text string
	isInterim := true
	vadFinished := false
	nonstream := false
	for _, item := range r.Results {
		text += item.Text
		if item.IsInterim != nil {
			isInterim = *item.IsInterim
		}
		vadFinished = item.IsVADFinished
		nonstream = item.Extra.NonstreamResult
	}
	return text, text != "" && (nonstream || (!isInterim && vadFinished))
}

func send(conn *websocket.Conn, stage string, req asrproto.Request) {
	emit(stage+".request", requestSummary(req))
	if err := conn.WriteMessage(websocket.BinaryMessage, asrproto.MarshalRequest(req)); err != nil {
		log.Fatal(err)
	}
}

func read(conn *websocket.Conn, stage string) (asrproto.Response, []byte, error) {
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return asrproto.Response{}, nil, err
		}
		if mt != websocket.BinaryMessage {
			emit(stage+".nonbinary", map[string]any{"message_type": mt, "len": len(data)})
			continue
		}
		resp, err := asrproto.UnmarshalResponse(data)
		if err != nil {
			return asrproto.Response{}, data, err
		}
		emit(stage+".response", map[string]any{
			"known":           resp,
			"wire_fields":     decodeWireFields(data),
			"result_json":     decodeResultJSON(resp.ResultJSON),
			"result_json_len": len(resp.ResultJSON),
			"raw_len":         len(data),
		})
		return resp, data, nil
	}
}

func requestSummary(req asrproto.Request) map[string]any {
	return map[string]any{
		"fields":      decodeWireFields(asrproto.MarshalRequest(req)),
		"service":     req.ServiceName,
		"method":      req.MethodName,
		"request_id":  req.RequestID,
		"has_token":   req.Token != "",
		"payload":     decodeResultJSON(req.Payload),
		"payload_len": len(req.Payload),
		"audio_len":   len(req.AudioData),
		"frame_state": req.FrameState,
	}
}

func decodeResultJSON(s string) any {
	if s == "" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	return v
}

func emit(event string, data map[string]any) {
	data["event"] = event
	data["ts"] = time.Now().Format(time.RFC3339Nano)
	_ = json.NewEncoder(os.Stdout).Encode(data)
}

func decodeWireFields(data []byte) []map[string]any {
	var fields []map[string]any
	for off := 0; off < len(data); {
		tag, n, ok := readVarint(data, off)
		if !ok {
			fields = append(fields, map[string]any{"offset": off, "error": "bad_tag"})
			return fields
		}
		off = n
		field := int(tag >> 3)
		wire := int(tag & 7)
		entry := map[string]any{"field": field, "wire": wire}
		switch wire {
		case 0:
			v, n, ok := readVarint(data, off)
			if !ok {
				entry["error"] = "bad_varint"
				fields = append(fields, entry)
				return fields
			}
			entry["value"] = v
			off = n
		case 2:
			l, n, ok := readVarint(data, off)
			if !ok || n+int(l) > len(data) {
				entry["error"] = "bad_length"
				fields = append(fields, entry)
				return fields
			}
			entry["len"] = int(l)
			off = n + int(l)
		case 1:
			entry["len"] = 8
			off += 8
		case 5:
			entry["len"] = 4
			off += 4
		default:
			entry["error"] = "unsupported_wire"
			fields = append(fields, entry)
			return fields
		}
		fields = append(fields, entry)
	}
	return fields
}

func readVarint(data []byte, off int) (uint64, int, bool) {
	var v uint64
	for shift := uint(0); shift < 64; shift += 7 {
		if off >= len(data) {
			return 0, off, false
		}
		c := data[off]
		off++
		v |= uint64(c&0x7f) << shift
		if c < 0x80 {
			return v, off, true
		}
	}
	return 0, off, false
}
