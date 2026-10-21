package ws

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// The full path a chat payload travels: EncodeSend frames it for the
// server, the server relays the opaque payload (never parsing it)
// inside a typed message frame, and DecodeFrame gets the original
// bytes back out the other end.
func TestSendPayloadSurvivesTheServerRelay(t *testing.T) {
	sealed := []byte{0x00, 0x01, 0xfe, 0xff, 0x2a}

	sent, err := EncodeSend("usr_01JEXAMPLE", sealed)
	if err != nil {
		t.Fatalf("EncodeSend: %v", err)
	}

	var outbound struct {
		To      string          `json:"to"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(sent, &outbound); err != nil {
		t.Fatalf("sent frame is not valid JSON: %v", err)
	}
	if outbound.To != "usr_01JEXAMPLE" {
		t.Fatalf("to = %q, want usr_01JEXAMPLE", outbound.To)
	}

	relayed, err := json.Marshal(map[string]any{
		"type":    EventTypeMessage,
		"from":    "usr_01JEXAMPLE",
		"payload": json.RawMessage(outbound.Payload),
	})
	if err != nil {
		t.Fatalf("build relayed frame: %v", err)
	}

	got, err := DecodeFrame(relayed)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if got.Type != EventTypeMessage {
		t.Errorf("Type = %q, want %q", got.Type, EventTypeMessage)
	}
	if !bytes.Equal(got.Payload, sealed) {
		t.Errorf("Payload = %v, want %v", got.Payload, sealed)
	}
}

func TestEncodeSendProducesTheServerDocumentedShape(t *testing.T) {
	frame, err := EncodeSend("usr_1", []byte("x"))
	if err != nil {
		t.Fatalf("EncodeSend: %v", err)
	}

	var shape map[string]any
	if err := json.Unmarshal(frame, &shape); err != nil {
		t.Fatalf("frame is not valid JSON: %v", err)
	}
	if len(shape) != 2 {
		t.Errorf("frame has %d fields, want exactly to+payload: %s", len(shape), frame)
	}
	if shape["to"] != "usr_1" {
		t.Errorf("to = %v, want usr_1", shape["to"])
	}
	// base64, so the opaque binary payload survives a JSON round trip
	// intact and the server never has to interpret it.
	if shape["payload"] != "eA==" {
		t.Errorf("payload = %v, want base64 of \"x\" (eA==)", shape["payload"])
	}
}

func TestEncodeSendRejectsEmptyRecipient(t *testing.T) {
	if _, err := EncodeSend("", []byte("x")); err == nil {
		t.Fatal("EncodeSend with empty recipient: want error, got nil")
	}
}

func TestDecodeFrameMessage(t *testing.T) {
	got, err := DecodeFrame([]byte(`{"type":"message","from":"usr_sender","payload":"aGVsbG8="}`))
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if got.From != "usr_sender" {
		t.Errorf("From = %q, want usr_sender", got.From)
	}
	if string(got.Payload) != "hello" {
		t.Errorf("Payload = %q, want hello", got.Payload)
	}
}

func TestDecodeFrameNodeEventsCarryFromAndNoPayload(t *testing.T) {
	for _, tc := range []struct {
		frame string
		want  string
	}{
		{`{"type":"node_request","from":"usr_a"}`, EventTypeNodeRequest},
		{`{"type":"node_accepted","from":"usr_b"}`, EventTypeNodeAccepted},
	} {
		got, err := DecodeFrame([]byte(tc.frame))
		if err != nil {
			t.Errorf("%s: DecodeFrame: %v", tc.want, err)
			continue
		}
		if got.Type != tc.want || got.From == "" || got.Payload != nil {
			t.Errorf("%s: got %+v, want Type=%q with From set and no payload", tc.want, got, tc.want)
		}
	}
}

func TestDecodeFrameErrorEvent(t *testing.T) {
	got, err := DecodeFrame([]byte(`{"type":"error","error":"rate_limited"}`))
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if !got.IsRateLimited() {
		t.Errorf("IsRateLimited() = false for %+v, want true", got)
	}

	other, err := DecodeFrame([]byte(`{"type":"error","error":"something_else"}`))
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	if other.IsRateLimited() {
		t.Error("IsRateLimited() = true for a non-rate_limited error")
	}
}

// A frame type this client has never heard of must parse into an Event
// the caller can ignore, not fail the pump - the server's protocol has
// grown before (API.md's key-exchange types) and a client that
// hard-fails on the next one would drop every connection.
func TestDecodeFrameUnknownTypeIsNotAnError(t *testing.T) {
	got, err := DecodeFrame([]byte(`{"type":"key_exchange_request","from":"usr_a","request_id":"req_1"}`))
	if err != nil {
		t.Fatalf("DecodeFrame on an unknown type: %v", err)
	}
	if got.Type != "key_exchange_request" {
		t.Errorf("Type = %q, want key_exchange_request", got.Type)
	}
}

func TestDecodeFrameRejectsMalformedFrames(t *testing.T) {
	cases := map[string]string{
		"not JSON":                     `{"type":`,
		"no type":                      `{"from":"usr_a","payload":"eA=="}`,
		"message no from":              `{"type":"message","payload":"eA=="}`,
		"message no payload":           `{"type":"message","from":"usr_a"}`,
		"message payload not a string": `{"type":"message","from":"usr_a","payload":{"nested":true}}`,
		"message payload not base64":   `{"type":"message","from":"usr_a","payload":"!!!"}`,
		"node_request no from":         `{"type":"node_request"}`,
		"node_accepted no from":        `{"type":"node_accepted"}`,
		"error no reason":              `{"type":"error"}`,
	}
	for name, frame := range cases {
		if _, err := DecodeFrame([]byte(frame)); err == nil {
			t.Errorf("%s (%s): want error, got nil", name, frame)
		}
	}
}

// Every error must name the ws package and never mention a Go-internal
// wrapper, so a frame problem surfaced in the TUI's status line reads
// as coming from this client rather than from encoding/json.
func TestDecodeFrameErrorsAreWrapped(t *testing.T) {
	_, err := DecodeFrame([]byte(`{"type":`))
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.HasPrefix(err.Error(), "ws: ") {
		t.Errorf("error %q does not start with the package prefix", err)
	}
}

func TestRoomFramesDecode(t *testing.T) {
	cases := []struct {
		name  string
		frame string
		check func(Event) bool
	}{
		{"message", `{"type":"room_message","room":"r1","seq":7,"from":"u1","payload":"AAE="}`,
			func(e Event) bool {
				return e.Room == "r1" && e.Seq == 7 && e.From == "u1" && bytes.Equal(e.Payload, []byte{0, 1})
			}},
		{"ack", `{"type":"room_ack","room":"r1","cid":"abc","seq":3}`,
			func(e Event) bool { return e.Room == "r1" && e.Cid == "abc" && e.Seq == 3 }},
		{"invite", `{"type":"room_invite","room":"r1","name":"standup","from":"u9"}`,
			func(e Event) bool { return e.Room == "r1" && e.Name == "standup" && e.From == "u9" }},
		{"joined", `{"type":"room_joined","room":"r1","user":"u2"}`,
			func(e Event) bool { return e.Type == EventTypeRoomJoined && e.User == "u2" }},
		{"left", `{"type":"room_left","room":"r1","user":"u2"}`,
			func(e Event) bool { return e.Type == EventTypeRoomLeft && e.User == "u2" }},
		{"direct", `{"type":"room_direct","room":"r1","from":"u1","payload":"AAE="}`,
			func(e Event) bool {
				return e.Type == EventTypeRoomDirect && e.Room == "r1" && e.From == "u1" && bytes.Equal(e.Payload, []byte{0, 1})
			}},
		{"offer", `{"type":"room_transfer_offer","room":"r1","from":"u1"}`,
			func(e Event) bool { return e.Type == EventTypeRoomTransferOffer && e.Room == "r1" && e.From == "u1" }},
		{"cancelled", `{"type":"room_transfer_cancelled","room":"r1"}`,
			func(e Event) bool { return e.Type == EventTypeRoomTransferCancelled && e.Room == "r1" }},
		{"owner changed", `{"type":"room_owner_changed","room":"r1","owner":"u2","from":"u1"}`,
			func(e Event) bool { return e.Type == EventTypeRoomOwnerChanged && e.Owner == "u2" && e.From == "u1" }},
		{"ended", `{"type":"room_ended","room":"r1","reason":"owner_gone"}`,
			func(e Event) bool { return e.Room == "r1" && e.Reason == "owner_gone" }},
	}
	for _, c := range cases {
		e, err := DecodeFrame([]byte(c.frame))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !c.check(e) {
			t.Errorf("%s: decoded %+v", c.name, e)
		}
	}
}

func TestMalformedRoomFramesAreErrors(t *testing.T) {
	for _, frame := range []string{
		`{"type":"room_message","seq":1,"from":"u","payload":"AA=="}`,
		`{"type":"room_message","room":"r","from":"u","payload":"AA=="}`,
		`{"type":"room_message","room":"r","seq":1,"from":"u"}`,
		`{"type":"room_message","room":"r","seq":1,"from":"u","payload":"!!"}`,
		`{"type":"room_ack","room":"r","seq":1}`,
		`{"type":"room_invite","room":"r"}`,
		`{"type":"room_direct","from":"u","payload":"AA=="}`,
		`{"type":"room_direct","room":"r","from":"u"}`,
		`{"type":"room_transfer_offer","room":"r"}`,
		`{"type":"room_transfer_cancelled"}`,
		`{"type":"room_owner_changed","room":"r"}`,
		`{"type":"room_joined","room":"r"}`,
		`{"type":"room_ended"}`,
	} {
		if _, err := DecodeFrame([]byte(frame)); err == nil {
			t.Errorf("accepted %s", frame)
		}
	}
}

func TestEncodeRoomSendShape(t *testing.T) {
	frame, err := EncodeRoomSend("r1", "cid1", []byte{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	var shape map[string]any
	if err := json.Unmarshal(frame, &shape); err != nil {
		t.Fatal(err)
	}
	if len(shape) != 3 || shape["room"] != "r1" || shape["cid"] != "cid1" || shape["payload"] != "AQI=" {
		t.Fatalf("shape = %v", shape)
	}
	if _, has := shape["to"]; has {
		t.Fatal("a room frame must not carry `to`")
	}
	if _, err := EncodeRoomSend("", "c", nil); err == nil {
		t.Error("empty room accepted")
	}
	if _, err := EncodeRoomSend("r", "", nil); err == nil {
		t.Error("empty cid accepted")
	}
}

func TestEncodeRoomDirect(t *testing.T) {
	b, err := EncodeRoomDirect("r1", "u2", []byte{0, 1})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"to":"u2","room":"r1","payload":"AAE="}` {
		t.Fatalf("frame = %s", b)
	}
	if _, err := EncodeRoomDirect("", "u2", nil); err == nil {
		t.Fatal("empty room accepted")
	}
	if _, err := EncodeRoomDirect("r1", "", nil); err == nil {
		t.Fatal("empty recipient accepted")
	}
}
