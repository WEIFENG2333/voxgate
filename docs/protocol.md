# ASR 协议规范

输入法语音识别的完整协议规范，基于客户端逆向，并用 Python 与 Go 两套实现端到端真实跑通验证（识别结果逐字正确）。

- 业务语义（鉴权、参数、事件、流程）来自客户端静态逆向；wire 帧格式经实测确认。
- 后端是 SAMICore 音频 SDK 的非公开输入法 ASR 服务，仅供学习研究，可能随时变更或失效。
- 参考实现：Go `internal/asr/`。

---

## 1. 总览

一次识别的完整链路：

```
设备注册(HTTP) ─▶ 拉取动态配置(HTTP) ─▶ 建立 WebSocket
   ─▶ StartTask ─▶ StartSession ─▶ 流式 TaskRequest(音频帧) ─▶ FinishSession
   ◀── TaskStarted / SessionStarted / 流式识别结果 / SessionFinished
```

- 传输：WebSocket，消息体为裸 protobuf（binary frame）。
- 音频：16kHz / 单声道 / 16bit，10ms 一帧；编码 `speech_opus`（默认）或 `raw`（PCM 裸流）。
- 鉴权：设备注册拿 `device_id` + 内置 appKey，无需签名。

---

## 2. 鉴权

### 2.1 设备注册

| 项 | 值 |
|---|---|
| 方法 | `POST https://log.snssdk.com/service/2/device_register/` |
| 关键参数 | `aid=401734`、`app_name=oime`、`package=com.bytedance.android.doubaoime`、`version_code=100309006`、`channel=official` |
| Body | 标准 bdinstall：`{"magic_tag":"ss_app_log","header":{设备信息+cdid/openudid/clientudid},"_gen_time":<ms>}` |
| 签名 | 无需（首次注册放行） |
| 返回 | `device_id_str` / `install_id_str` |

设备信息字段（`device_type`/`device_brand`/`os_version`/`resolution` 等）真机取自 `Build.*`；自行实现时填一个真实机型即可，服务端不校验。

### 2.2 ASR appKey

语音链路用**内置 appKey `OrnqKvSSrs`**，无需任何网络获取，直接作为 protobuf 的 `token` 字段发送。

> settings 接口也会下发一个 `asr_config.app_key`（如 `RTIHIRzbwS`），那是 bot 调试用的 key，**不是语音 appKey**。两者服务端都接受，但语音链路用的是内置的。

### 2.3 动态配置（可选）

| 项 | 值 |
|---|---|
| 方法 | `POST https://is.snssdk.com/service/settings/v3/`，body=`body=null` |
| 签名 | header `x-ss-stub` = `MD5("body=null")` 大写 |
| 返回 | `data.settings.asr_config`（map） |

`asr_config` 下发一批可动态覆盖会话参数的值：`cell_compress_rate`、`asr_params`、`switch_network_*`、`retry_server_code`、`offline_wait_*` 等。客户端读取后覆盖默认会话参数。非语音必需，可跳过。

---

## 3. WebSocket 连接

### 3.1 端点（两个，行为一致）

| 端点 | URL |
|---|---|
| WS | `wss://frontier-audio-ime-ws.doubao.com/ocean/api/v1/ws` |
| QUIC | `wss://frontier-audio-ime-quic.doubao.com/api/v1/ws`（路径不含 `ocean`） |

官方靠 AB 实验二选一，自行实现任选其一，两个都实测可用。

### 3.2 query 参数

`aid=401734` + `device_id=<注册得到>`。

### 3.3 握手 header

仅 `proto-version: v2`。（PPE 调试环境会额外带 `x-tt-env` / `x-use-ppe`，生产不需要。）

---

## 4. wire 帧格式

每个 protobuf message 直接作为 WebSocket **binary message** 收发，没有额外的帧头。

### 4.1 请求 AsrRequest（client → server）

| 字段号 | 类型 | 名称 | 说明 |
|---|---|---|---|
| 2 | string | token | 填内置 appKey `OrnqKvSSrs` |
| 3 | string | service | 固定 `ASR` |
| 5 | string | method | `StartTask` / `StartSession` / `TaskRequest` / `FinishSession` |
| 6 | string | payload | JSON：StartSession=会话配置；TaskRequest=帧标志 |
| 7 | bytes | audio | 音频帧数据（仅 TaskRequest） |
| 8 | string | request_id | 整个会话同一个 UUID |
| 9 | varint | frame_state | 1=首帧 3=中间帧 9=末帧（仅 TaskRequest） |

### 4.2 响应 AsrResponse（server → client）

| 字段号 | 类型 | 名称 | 说明 |
|---|---|---|---|
| 1 | string | request_id | 回显 |
| 2 | string | task_id | 每条响应一个 |
| 3 | string | service | `ASR` |
| 4 | string | message_type | `TaskStarted`/`SessionStarted`/`SessionFinished`/`TaskFailed`/`SessionFailed`；中间结果为空 |
| 5 | varint | status_code | `20000000`=成功 |
| 6 | string | status_message | `OK` 或错误描述 |
| 7 | string | result_json | 识别结果 JSON（见 §7） |

> 字段 9(varint) / 11(bytes) 服务端偶发返回，含义未明，可忽略。

---

## 5. 完整交互流程

```
1. 建立 WebSocket（query: aid + device_id；header: proto-version:v2）

2. → StartTask    {token, service:"ASR", method:"StartTask", request_id}
   ← TaskStarted   (status_code 20000000)

3. → StartSession {token, service:"ASR", method:"StartSession", payload:<会话配置>, request_id}
   ← SessionStarted

4. 流式发送音频（每帧 10ms，按采集节奏）：
   → TaskRequest  {service:"ASR", method:"TaskRequest", payload:<帧标志>, audio:<帧>, request_id, frame_state}
        首帧 frame_state=1，中间=3，末帧=9
        帧标志(payload)：中间帧 {}；末帧 {"finish_audio":true}；需强制重打分时 {"force_asr_twopass":true}
        末帧标志放在最后一个真实音频帧上（不发额外静音帧）
   ← 期间持续回流式结果（is_interim:true，文本逐步增长）

5. → FinishSession {token, service:"ASR", method:"FinishSession", request_id}
   ← 定稿结果（is_interim:false, is_vad_finished:true）
   ← SessionFinished
```

---

## 6. StartSession 会话参数

`StartSession.payload` 为 JSON，结构：

```json
{
  "audio_info": {"format": "raw|speech_opus", "sample_rate": 16000, "channel": 1},
  "enable_punctuation": true,
  "enable_speech_rejection": false,
  "extra": { ... 见下表 ... }
}
```

### 固定值

| 字段 | 值 | 位置 |
|---|---|---|
| aid | `401734` | URL query + extra |
| app_name | `oime` | URL query |
| token(appKey) | `OrnqKvSSrs` | protobuf field 2 |
| service | `ASR` | protobuf field 3 |
| sample_rate / channel | `16000` / `1` | audio_info |

### 可调参数（默认值为官方默认）

| 字段 | 默认 | 含义 |
|---|---|---|
| `audio_info.format` | `speech_opus` | 音频编码；`raw`=16bit PCM（免编码） |
| `enable_punctuation` | true | 自动标点 |
| `enable_speech_rejection` | false | 拒识非语音内容 |
| `extra.enable_asr_twopass` | true | 两遍识别（流式 + 整句重打分） |
| `extra.enable_asr_threepass` | true | 三遍识别（最高精度通道） |
| `extra.use_twopass_retry` | true | twopass 失败重试 |
| `extra.strong_ddc` | true | 强文本规整（口语→书面、数字/单位归一） |
| `extra.enable_text_filter` | false | 敏感/无关文本过滤 |
| `extra.end_smooth_window_ms` | 800 | VAD 句尾平滑窗口(ms) |
| `extra.remove_space_between_han_num` | true | 汉字-数字间去空格 |
| `extra.remove_space_between_han_eng` | true | 汉字-英文间去空格 |
| `extra.disable_user_words` | false | 禁用个人词库（false=启用云端个性化） |
| `extra.input_mode` | "" | 输入入口标识 |
| `extra.context` | "" | 上文文本（多句纠错） |
| `extra.asr_params` | ""（settings 下发） | 服务端透传参数 |
| `extra.cell_compress_rate` | 8 | 蜂窝网音频压缩档位 |
| `extra.network_change` | `{quality:4, rtt:273, ping_timeout:1000}` | 网络切换阈值 |
| `extra.app_name` | `com.bytedance.android.doubaoime` | 宿主包名 |
| `extra.did` | `<device_id>` | 设备 id |
| `extra.model/brand/os_version/os_type/app_version` | 设备信息 | 机型上报 |
| `extra.join_user_experience_improve_program` | false | 用户体验改进计划 |

---

## 7. 识别结果 result_json

服务端在一次会话中陆续推送多种形态（用 `results` 是否为 null、`is_interim` 区分）：

```jsonc
// 首包（连接确认）
{"results": null, "extra": {"packet_number": 0, "client_ip": "...", "heartbeat_num": 0}}

// VAD 检测到开始说话
{"results": [{"text": "", "is_interim": true}], "extra": {"vad_start": true}}

// 心跳（无新结果）
{"results": null, "extra": {"packet_number": 3, "heartbeat_num": 5}}

// 中间结果（流式增长，会被覆盖）
{"results": [{"text": "今天天气", "start_time": 0, "end_time": 1.37, "is_interim": true}],
 "extra": {"seq_id": 5}}

// 定稿（一段语音结束 / 整句最终）
{"results": [
   {"text": "今天天气怎么样。", "start_time": 0, "end_time": 3.62,
    "is_interim": false, "is_vad_finished": true},
   {"text": "今天天气怎么样。", "is_interim": false, "extra": {"nonstream_result": true}}
 ],
 "extra": {"seq_id": 10, "lastpkt_latency": 148, "fast_modify_pairs": null,
           "pt": "{\"task_id\":\"...\"}"}}
```

`results[]` 字段：`text`、`start_time`/`end_time`（秒）、`is_interim`（true=中间/false=定稿）、`is_vad_finished`（一段结束）、`confidence`、`alternatives`（候选，含 `words` 逐词时间戳、`oi_decoding_info`、`semantic_related_to_prev`）、`extra.nonstream_result`（threepass 重打分结果）。

顶层 `extra`：`seq_id`、`packet_number`、`heartbeat_num`、`vad_start`、`client_ip`、`lastpkt_latency`、`fast_modify_pairs`、`pt`(含 task_id)、`audio_duration`、`model_avg_rtf` 等。

**解析决策**：取最后一条 `is_interim:false` 的 `text` 为最终结果；`results:null` 的包（首包/心跳）忽略。

---

## 8. 事件与错误码

事件以整数 ID 经 SDK 回调（协议字符串由 native 映射）。ASR 相关核心事件：

| 事件 | 含义 |
|---|---|
| TaskStarted (200) | 任务确认 |
| SessionStarted (204) | 会话确认 |
| SessionFinished (205) | 会话正常结束 |
| TaskFailed (202) / SessionFailed (207) | 任务/会话失败 |
| ASRResponse (209) / ASREnded (210) | 流式结果 / 结束 |
| VadStarted (250) / VadEnded (251) | 本地 VAD |

会话请求类型（`SAMICoreSessionRequestType`）：ASR 走 `MessageASR`(3) / `MessageASRV2`(8) 及对应 Retry(6/10)。

错误码段（`SAMICoreCode`）：
- 鉴权：`100007` token 过期、`100009` token/appKey 不匹配、`160001` ToB 鉴权失败
- 网络/会话：`185001` 创建客户端失败、`185003` 连接失败、`185004` 启动任务失败、`185010` 会话未找到、`185014` 创建会话失败
- 服务端业务：`40200001` Odin 鉴权失败；需重试码 `40100000/40100004/50000104/50700000`

---

## 9. 实现参考

**Go（本仓库）** `internal/asr/`：
- `protocol.go` 常量；`device.go` 设备画像与端点；`session.go` 会话参数；
- `credentials.go` 注册与动态配置；`client.go` 连接与流式收发；`proto/asr.go` protobuf 编解码。

运行：`make build && ./bin/voxgate transcribe <16k 单声道音频>`；端点/格式/设备用 `VOXGATE_ASR_ENDPOINT` / `VOXGATE_ASR_AUDIO_FORMAT` / `VOXGATE_ASR_DEVICE` 切换。

---

## 10. 附录

### 常量速查

| 项 | 值 |
|---|---|
| WS 端点 | `wss://frontier-audio-ime-ws.doubao.com/ocean/api/v1/ws` |
| QUIC 端点 | `wss://frontier-audio-ime-quic.doubao.com/api/v1/ws` |
| aid | `401734` |
| app_name / package | `oime` / `com.bytedance.android.doubaoime` |
| 内置 appKey | `OrnqKvSSrs` |
| 注册接口 | `log.snssdk.com/service/2/device_register/` |
| 配置接口 | `is.snssdk.com/service/settings/v3/` |
| 音频 | 16kHz / 单声道 / 16bit / 10ms 帧 |
| 成功码 | `20000000` |

### native 边界说明

业务语义层（URL、鉴权、参数、事件、流程）在 dex 可完整逆向。wire 帧的字节格式（protobuf 字段号、frontier 封装）由 native 库（`libime_net_sdk.so` Rust / `libsscronet.so`）实现、dex 不可见，本文档的字段号经实测请求确认有效。frontier 在客户端是 Cronet 透明字节通道，对裸 WebSocket 客户端透明，因此直接收发 protobuf 即可。
