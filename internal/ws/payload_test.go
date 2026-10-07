package ws

import (
	"bytes"
	"strings"
	"testing"
)

func TestHandshakeRoundTrip(t *testing.T) {
	var key [32]byte
	for i := range key {
		key[i] = byte(i)
	}

	payload, err := EncodeHandshake(&key)
	if err != nil {
		t.Fatalf("EncodeHandshake: %v", err)
	}
	if PayloadKind(payload[0]) != PayloadKindHandshake {
		t.Fatalf("kind byte = %#x, want %#x", payload[0], PayloadKindHandshake)
	}

	kind, body, err := DecodePayload(payload)
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if kind != PayloadKindHandshake {
		t.Errorf("kind = %#x, want %#x", kind, PayloadKindHandshake)
	}

	got, err := ParseHandshake(body)
	if err != nil {
		t.Fatalf("ParseHandshake: %v", err)
	}
	if *got != key {
		t.Error("round trip changed the ephemeral key")
	}
}

// A sealed blob is random bytes, so the tag on it is what keeps
// DecodePayload from ever mistaking a message for a handshake (and
// vice versa) - the two must come back distinguishable no matter what
// the ciphertext happens to start with.
func TestSealedPayloadStaysSealed(t *testing.T) {
	for _, first := range []byte{0x00, 0x01, 0x02, 0xff} {
		sealed := append([]byte{first}, bytes.Repeat([]byte{0xab}, 64)...)

		payload := EncodeSealed(sealed)
		kind, body, err := DecodePayload(payload)
		if err != nil {
			t.Fatalf("DecodePayload: %v", err)
		}
		if kind != PayloadKindSealed {
			t.Fatalf("kind = %#x, want %#x", kind, PayloadKindSealed)
		}
		if !bytes.Equal(body, sealed) {
			t.Errorf("body changed: got %x, want %x", body, sealed)
		}
	}
}

func TestDecodePayloadRejectsWhatItCannotUnderstand(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload []byte
		wantErr string
	}{
		{"empty", nil, "empty payload"},
		{"tag only handshake", []byte{byte(PayloadKindHandshake)}, "no body"},
		{"tag only sealed", []byte{byte(PayloadKindSealed)}, "no body"},
		{"unknown kind", []byte{0x7f, 'x'}, "unknown payload kind"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := DecodePayload(tc.payload)
			if err == nil {
				t.Fatal("DecodePayload accepted a payload it can't understand")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestParseHandshakeRejectsMalformedFrames(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		wantErr string
	}{
		{"not json", "definitely not json", "parse handshake"},
		{"future version", `{"v":2,"eph":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}`, "unsupported handshake version"},
		{"ephemeral not base64", `{"v":1,"eph":"!!!"}`, "not valid base64"},
		{"wrong key length", `{"v":1,"eph":"AQID"}`, "3 bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseHandshake([]byte(tc.body))
			if err == nil {
				t.Fatal("ParseHandshake accepted a malformed frame")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestEncodeHandshakeRejectsNilKey(t *testing.T) {
	if _, err := EncodeHandshake(nil); err == nil {
		t.Fatal("EncodeHandshake accepted a nil key")
	}
}
