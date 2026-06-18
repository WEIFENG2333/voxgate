//go:build ignore

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/WEIFENG2333/voxgate/internal/audio"
)

func main() {
	url := flag.String("url", "ws://127.0.0.1:8080/v1/realtime", "realtime websocket URL")
	token := flag.String("token", "", "bearer token")
	audioPath := flag.String("audio", "tests/audio/zh_clean_6s.wav", "audio file")
	chunkMS := flag.Int("chunk-ms", 100, "PCM chunk size in milliseconds")
	realtime := flag.Bool("realtime", false, "sleep between chunks to simulate microphone input")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	src, err := audio.ConvertFile(ctx, *audioPath)
	if err != nil {
		log.Fatal(err)
	}
	pcm := collectPCM(src)
	header := http.Header{}
	if *token != "" {
		header.Set("Authorization", "Bearer "+*token)
	}
	conn, _, err := websocket.DefaultDialer.Dial(*url, header)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	start := time.Now()
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			fmt.Printf("[%s] %s\n", time.Since(start).Truncate(time.Millisecond), string(data))
			if strings.Contains(string(data), `"conversation.item.input_audio_transcription.completed"`) {
				os.Exit(0)
			}
		}
	}()
	send(conn, map[string]any{
		"type": "session.update",
		"session": map[string]any{
			"type": "transcription",
			"audio": map[string]any{"input": map[string]any{
				"format":         map[string]any{"type": "audio/pcm", "rate": audio.SampleRate},
				"transcription":  map[string]any{"model": "gpt-realtime-whisper"},
				"turn_detection": nil,
			}},
		},
	})
	bytesPerChunk := audio.SampleRate * 2 * *chunkMS / 1000
	if bytesPerChunk <= 0 {
		bytesPerChunk = audio.BytesPerFrame
	}
	for off := 0; off < len(pcm); off += bytesPerChunk {
		end := off + bytesPerChunk
		if end > len(pcm) {
			end = len(pcm)
		}
		send(conn, map[string]any{
			"type":  "input_audio_buffer.append",
			"audio": base64.StdEncoding.EncodeToString(pcm[off:end]),
		})
		if *realtime {
			time.Sleep(time.Duration(*chunkMS) * time.Millisecond)
		}
	}
	send(conn, map[string]any{"type": "input_audio_buffer.commit"})
	select {}
}

func collectPCM(src *audio.Source) []byte {
	var out []byte
	for {
		frame, ok, err := src.NextFrame()
		if err != nil {
			log.Fatal(err)
		}
		if !ok {
			return out
		}
		out = append(out, frame...)
	}
}

func send(conn *websocket.Conn, v any) {
	data, _ := json.Marshal(v)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Fatal(err)
	}
}
