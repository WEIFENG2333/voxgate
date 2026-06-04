// Low-latency microphone capture for voxgate.
// Captures the default input via AVAudioEngine, converts to 16 kHz mono
// signed-16-bit little-endian PCM, and writes raw frames to stdout — a drop-in,
// lower-latency replacement for `ffmpeg -f avfoundation` (which buffers ~0.4s).
//
// Build:  swiftc -O tests/realtime/miccap.swift -o bin/miccap
// Use:    bin/miccap | voxgate transcribe --input-format pcm16 --stream -
import AVFoundation

let engine = AVAudioEngine()
let input = engine.inputNode
let inFormat = input.outputFormat(forBus: 0)
guard let outFormat = AVAudioFormat(commonFormat: .pcmFormatInt16,
                                    sampleRate: 16000, channels: 1,
                                    interleaved: true),
      let converter = AVAudioConverter(from: inFormat, to: outFormat) else {
    FileHandle.standardError.write(Data("miccap: cannot create converter\n".utf8))
    exit(1)
}
let out = FileHandle.standardOutput
let ratio = outFormat.sampleRate / inFormat.sampleRate

// ~25 ms hardware buffer keeps capture latency low.
input.installTap(onBus: 0, bufferSize: 1200, format: inFormat) { buffer, _ in
    let cap = AVAudioFrameCount(Double(buffer.frameLength) * ratio) + 32
    guard let outBuf = AVAudioPCMBuffer(pcmFormat: outFormat, frameCapacity: cap) else { return }
    var err: NSError?
    var fed = false
    converter.convert(to: outBuf, error: &err) { _, status in
        if fed { status.pointee = .noDataNow; return nil }
        fed = true
        status.pointee = .haveData
        return buffer
    }
    guard err == nil, let ch = outBuf.int16ChannelData else { return }
    out.write(Data(bytes: ch[0], count: Int(outBuf.frameLength) * 2))
}

do {
    try engine.start()
} catch {
    FileHandle.standardError.write(Data("miccap: engine start failed: \(error)\n".utf8))
    exit(1)
}
RunLoop.current.run()
