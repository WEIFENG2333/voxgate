#!/usr/bin/env python3
import argparse
from openai import OpenAI


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", default="http://127.0.0.1:8080/v1")
    parser.add_argument("--api-key", default="test-token")
    parser.add_argument("audio")
    args = parser.parse_args()

    client = OpenAI(base_url=args.base_url, api_key=args.api_key)
    with open(args.audio, "rb") as f:
        result = client.audio.transcriptions.create(
            model="ime-asr",
            file=f,
            response_format="json",
        )
    print(result.text)


if __name__ == "__main__":
    main()
