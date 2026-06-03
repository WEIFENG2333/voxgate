package proto

import (
	"errors"
	"fmt"
)

type FrameState int32

const (
	FrameStateFirst  FrameState = 1
	FrameStateMiddle FrameState = 3
	FrameStateLast   FrameState = 9
)

type Request struct {
	Token       string
	ServiceName string
	MethodName  string
	Payload     string
	AudioData   []byte
	RequestID   string
	FrameState  FrameState
}

type Response struct {
	RequestID     string
	TaskID        string
	ServiceName   string
	MessageType   string
	StatusCode    int32
	StatusMessage string
	ResultJSON    string
	Unknown9      int32
	Unknown11     []byte
}

// MarshalRequest encodes Request using the observed upstream protobuf field numbers.
func MarshalRequest(req Request) []byte {
	var b []byte
	writeString(&b, 2, req.Token)
	writeString(&b, 3, req.ServiceName)
	writeString(&b, 5, req.MethodName)
	writeString(&b, 6, req.Payload)
	writeBytes(&b, 7, req.AudioData)
	writeString(&b, 8, req.RequestID)
	if req.FrameState != 0 {
		writeVarintField(&b, 9, uint64(req.FrameState))
	}
	return b
}

// UnmarshalRequest decodes the observed upstream protobuf request envelope.
// It is primarily used by protocol-level E2E tests to make the mock upstream
// validate the same fields that the real upstream receives.
func UnmarshalRequest(data []byte) (Request, error) {
	var req Request
	for off := 0; off < len(data); {
		tag, n, err := readVarint(data, off)
		if err != nil {
			return req, err
		}
		off = n
		field := int(tag >> 3)
		wire := int(tag & 7)
		switch wire {
		case 0:
			v, n, err := readVarint(data, off)
			if err != nil {
				return req, err
			}
			off = n
			if field == 9 {
				req.FrameState = FrameState(v)
			}
		case 2:
			l, n, err := readVarint(data, off)
			if err != nil {
				return req, err
			}
			off = n
			end := off + int(l)
			if end < off || end > len(data) {
				return req, errors.New("protobuf: truncated length-delimited field")
			}
			raw := data[off:end]
			s := string(raw)
			off = end
			switch field {
			case 2:
				req.Token = s
			case 3:
				req.ServiceName = s
			case 5:
				req.MethodName = s
			case 6:
				req.Payload = s
			case 7:
				req.AudioData = append(req.AudioData[:0], raw...)
			case 8:
				req.RequestID = s
			}
		case 1:
			if off+8 > len(data) {
				return req, errors.New("protobuf: truncated fixed64 field")
			}
			off += 8
		case 5:
			if off+4 > len(data) {
				return req, errors.New("protobuf: truncated fixed32 field")
			}
			off += 4
		default:
			return req, fmt.Errorf("protobuf: unsupported wire type %d", wire)
		}
	}
	return req, nil
}

func UnmarshalResponse(data []byte) (Response, error) {
	var resp Response
	for off := 0; off < len(data); {
		tag, n, err := readVarint(data, off)
		if err != nil {
			return resp, err
		}
		off = n
		field := int(tag >> 3)
		wire := int(tag & 7)
		switch wire {
		case 0:
			v, n, err := readVarint(data, off)
			if err != nil {
				return resp, err
			}
			off = n
			switch field {
			case 5:
				resp.StatusCode = int32(v)
			case 9:
				resp.Unknown9 = int32(v)
			}
		case 2:
			l, n, err := readVarint(data, off)
			if err != nil {
				return resp, err
			}
			off = n
			end := off + int(l)
			if end < off || end > len(data) {
				return resp, errors.New("protobuf: truncated length-delimited field")
			}
			raw := data[off:end]
			s := string(raw)
			off = end
			switch field {
			case 1:
				resp.RequestID = s
			case 2:
				resp.TaskID = s
			case 3:
				resp.ServiceName = s
			case 4:
				resp.MessageType = s
			case 6:
				resp.StatusMessage = s
			case 7:
				resp.ResultJSON = s
			case 11:
				resp.Unknown11 = append(resp.Unknown11[:0], raw...)
			}
		case 1:
			if off+8 > len(data) {
				return resp, errors.New("protobuf: truncated fixed64 field")
			}
			off += 8
		case 5:
			if off+4 > len(data) {
				return resp, errors.New("protobuf: truncated fixed32 field")
			}
			off += 4
		default:
			return resp, fmt.Errorf("protobuf: unsupported wire type %d", wire)
		}
	}
	return resp, nil
}

func MarshalResponse(resp Response) []byte {
	var b []byte
	writeString(&b, 1, resp.RequestID)
	writeString(&b, 2, resp.TaskID)
	writeString(&b, 3, resp.ServiceName)
	writeString(&b, 4, resp.MessageType)
	if resp.StatusCode != 0 {
		writeVarintField(&b, 5, uint64(resp.StatusCode))
	}
	writeString(&b, 6, resp.StatusMessage)
	writeString(&b, 7, resp.ResultJSON)
	if resp.Unknown9 != 0 {
		writeVarintField(&b, 9, uint64(resp.Unknown9))
	}
	return b
}

func writeString(b *[]byte, field int, s string) {
	if s != "" {
		writeBytes(b, field, []byte(s))
	}
}

func writeBytes(b *[]byte, field int, v []byte) {
	if len(v) == 0 {
		return
	}
	writeVarint(b, uint64(field<<3|2))
	writeVarint(b, uint64(len(v)))
	*b = append(*b, v...)
}

func writeVarintField(b *[]byte, field int, v uint64) {
	writeVarint(b, uint64(field<<3))
	writeVarint(b, v)
}

func writeVarint(b *[]byte, v uint64) {
	for v >= 0x80 {
		*b = append(*b, byte(v)|0x80)
		v >>= 7
	}
	*b = append(*b, byte(v))
}

func readVarint(data []byte, off int) (uint64, int, error) {
	var v uint64
	for shift := uint(0); shift < 64; shift += 7 {
		if off >= len(data) {
			return 0, off, errors.New("protobuf: truncated varint")
		}
		c := data[off]
		off++
		v |= uint64(c&0x7f) << shift
		if c < 0x80 {
			return v, off, nil
		}
	}
	return 0, off, errors.New("protobuf: varint too long")
}
