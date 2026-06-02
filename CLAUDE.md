# CLAUDE.md

给 AI agent 的项目工作指南。

## 项目是什么

`voxgate` —— 中文语音转文字 CLI + 本地 OpenAI 兼容转写服务（Go）。底层封装豆包输入法的非公开 ASR 后端（字节 SAMICore，WebSocket 协议）。仅供学习研究。

## 协议真相（动协议代码前必读）

完整协议规范：**`docs/protocol.md`** —— 基于客户端逆向 + Python/Go 双实现端到端实测验证。改任何鉴权/连接/参数/帧相关代码前先读它。

其它文档：`docs/strategy.md`（分块/实时策略）、`docs/validation.md`（验证报告）。

## 架构

```
cmd/voxgate/            CLI 入口（transcribe / serve / auth / doctor / version）
internal/asr/           协议核心
  protocol.go           常量：端点、内置 appKey、方法/消息名、音频规格
  device.go             设备画像（真实机型）+ 端点名解析
  session.go            StartSession 全量参数 + ToPayload
  credentials.go        设备注册 + 内置 appKey + settings 动态配置
  client.go             WS 连接 + StartTask/StartSession/TaskRequest/FinishSession + 流式收发
  parser.go events.go   结果 JSON 解析与事件分类
internal/proto/asr.go   protobuf 编解码（wire 字段号）
internal/audio/         音频源：文件 pcm.go / 实时 live.go；opus 编码
internal/transcriber/   转写编排（长音频分块）
internal/transcription/ 服务装配（config → Runner → Client）
internal/server/        OpenAI 兼容 HTTP / SSE / realtime WS
internal/config/        配置（yaml + 环境变量）
tests/audio/            测试音频（zh_5s.* / en_aesop_46s.mp3 / zh_liaozhai_40s.mp3）
```

## 关键不变量（勿破坏）

- **鉴权**：ASR 用内置 appKey `OrnqKvSSrs`（`protocol.BuiltinASRAppKey`），**不**从 settings 取 token；settings 只拉动态配置覆盖会话参数。
- **wire**：裸 protobuf 直接作为 WebSocket binary 帧。Request 字段号 `2,3,5,6,7,8,9`，Response `1,2,3,4,5,6,7`（`proto/asr.go`，勿改）。
- **帧机制**：末帧 payload 带 `{"finish_audio":true}`（不发静音帧）；10ms/帧；format `speech_opus`(默认) 或 `raw`(PCM 免编码)。
- **双端点**：WS / QUIC 行为一致，`config.ASR.Endpoint` = `ws|quic`。
- **节奏**：服务端对发送节奏不敏感，文件转写全速发即可正确识别；`client.go` 的 realtime sleep 只为实时输入体验，文件转写不走它。
- **设备**：`device.go` 的机型是真实占位，服务端不校验，可换。

## 构建 / 测试 / 运行

依赖：CGO + 系统 `libopus`（opus 编码；`raw` 格式不编码但构建仍链接它）+ `ffmpeg`（解码非 wav 输入）。

```bash
make build                                          # 编译 → bin/voxgate
make test                                           # go test ./...
make vet / make fmt
./bin/voxgate transcribe tests/audio/zh_5s.wav      # 文件转写

# 切换协议配置（env 覆盖）
VOXGATE_ASR_AUDIO_FORMAT=raw  ./bin/voxgate transcribe tests/audio/zh_5s.wav
VOXGATE_ASR_ENDPOINT=quic     ./bin/voxgate transcribe tests/audio/zh_5s.wav
VOXGATE_ASR_DEVICE=samsung    ./bin/voxgate transcribe tests/audio/zh_5s.wav
```

配置在 `internal/config/config.go`：yaml 的 `asr.{endpoint,audio_format,device,...}` 与对应 `VOXGATE_ASR_*` 环境变量。

## 代码注释规范

注释只说**「是什么 / 做什么」**。**不要**写逆向出处（如「来自 XX.apk 逆向」「见 docs §X」「[SdkImpl.x():763]」「对齐 APK」「bot 调试」）。来源与背景信息放文档，不进代码。

## 逆向资料（本地，已 gitignore）

- 客户端 APK（项目根）
- `docs/reverse-src/`（反编译的核心类）

## 改动提示

- 改协议/参数/帧：先读 `docs/protocol.md`；改完跑 `make test` **并**用真实音频转写验证（`tests/audio/`）。
- 改帧机制时注意 `internal/asr/client_test.go` 的 mock 断言（与发送帧序耦合）。
- 验证真实链路有外部依赖（联网调用上游 + 设备注册），单测用 mock，端到端用 `tests/audio/`。
