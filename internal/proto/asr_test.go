package proto

import "testing"

func TestMarshalRequestFieldNumbers(t *testing.T) {
	got := MarshalRequest(Request{
		Token: "tok", ServiceName: "ASR", MethodName: "TaskRequest", Payload: "{}", AudioData: []byte{1, 2}, RequestID: "rid", FrameState: FrameStateLast,
	})
	want := []byte{
		0x12, 0x03, 't', 'o', 'k',
		0x1a, 0x03, 'A', 'S', 'R',
		0x2a, 0x0b, 'T', 'a', 's', 'k', 'R', 'e', 'q', 'u', 'e', 's', 't',
		0x32, 0x02, '{', '}',
		0x3a, 0x02, 1, 2,
		0x42, 0x03, 'r', 'i', 'd',
		0x48, 0x09,
	}
	if string(got) != string(want) {
		t.Fatalf("unexpected bytes\n got: %x\nwant: %x", got, want)
	}
}

func TestUnmarshalResponse(t *testing.T) {
	data := MarshalResponse(Response{RequestID: "rid", MessageType: "SessionFinished", StatusCode: 200, StatusMessage: "OK", ResultJSON: `{"results":[]}`})
	resp, err := UnmarshalResponse(data)
	if err != nil {
		t.Fatal(err)
	}
	if resp.RequestID != "rid" || resp.MessageType != "SessionFinished" || resp.StatusCode != 200 || resp.ResultJSON == "" {
		t.Fatalf("bad response: %+v", resp)
	}
}
