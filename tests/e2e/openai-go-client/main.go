package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

func main() {
	baseURL := flag.String("base-url", "http://127.0.0.1:8080/v1", "OpenAI-compatible base URL")
	apiKey := flag.String("api-key", "test-token", "API key")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: openai-go-client [--base-url URL] [--api-key KEY] <audio>")
		os.Exit(2)
	}
	f, err := os.Open(flag.Arg(0))
	if err != nil {
		panic(err)
	}
	defer f.Close()
	client := openai.NewClient(
		option.WithBaseURL(*baseURL),
		option.WithAPIKey(*apiKey),
	)
	result, err := client.Audio.Transcriptions.New(context.Background(), openai.AudioTranscriptionNewParams{
		File:           f,
		Model:          "ime-asr",
		ResponseFormat: openai.AudioResponseFormatJSON,
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Text)
}
