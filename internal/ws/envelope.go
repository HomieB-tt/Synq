package ws

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// The wire formats implemented here are synq-server's, not invented
// locally - see that project's API.md, "WebSocket → Wire format":
//
//	client → server (the only outgoing shape):
//	  { "to": "<recipient-user-id>", "payload": <anything> }
//
//	server → client (always tagged with "type"):
//	  { "type": "message",      "from": "<sender-user-id>", "payload": <...> }
//	  { "type": "node_request", "from": "<requester-user-id>" }
//	  { "type": "node_accepted", "from": "<other-user-id>" }
//	  { "type": "error",        "error": "rate_limited" }
//
// The server relays "payload" opaquely - it never parses, stores, or
// inspects it, which is what lets the client pick its own encoding
// for what goes inside.
const (
	// EventTypeMessage carries an opaque chat payload from From.
	EventTypeMessage = "message"
	// EventTypeNodeRequest notifies From wants to be a node of yours.
	EventTypeNodeRequest = "node_request"
	// EventTypeNodeAccepted notifies From accepted your node request.
	EventTypeNodeAccepted = "node_accepted"
	// EventTypeError is the only failure signal the server ever sends
	// back over the socket.
	EventTypeError = "error"
)

// ErrorRateLimited is the only value EventTypeError carries today
// (API.md): a chat send that exceeded the per-user rate bucket. It's
// self-delivered - published to your own channel - so it arrives like
// any other incoming frame, and it's the sole exception to the
// "no delivery acknowledgment of any kind" rule.
const ErrorRateLimited = "rate_limited"

// Event is one decoded server → client frame. Payload is the raw
// (still encrypted) bytes of a "message" event's payload, nil for the
// event types that don't carry one.
type Event struct {
	Type    string
	From    string
	Payload []byte
	Error   string
}

// IsRateLimited reports whether e is the server's self-delivered
// rate-limit rejection of a send we just made.
func (e Event) IsRateLimited() bool {
	return e.Type == EventTypeError && e.Error == ErrorRateLimited
}

// outboundFrame is the client → server shape. Kept separate from
// inboundFrame rather than one shared struct with omitempty fields:
// the two directions have genuinely disjoint field sets, and sharing
// would let a typo (a "type" on an outgoing frame, say) compile.
type outboundFrame struct {
	To      string `json:"to"`
	Payload string `json:"payload"`
}

// inboundFrame is the raw server → client shape before Event's
// payload is decoded. Payload stays json.RawMessage here so a message
// frame with a malformed payload can produce a specific error instead
// of failing the whole unmarshal.
type inboundFrame struct {
	Type    string          `json:"type"`
	From    string          `json:"from"`
	Error   string          `json:"error"`
	Payload json.RawMessage `json:"payload"`
}

// EncodeSend builds the single outgoing frame shape synq-server
// accepts, addressed to recipientUserID's server-side user id (the
// "user_id" field of a GetPublicKeys response - not a username, and
// not a box public key) and carrying sealed, already-encrypted bytes.
// Those bytes go out base64-encoded: they're binary, which doesn't
// survive a JSON round trip otherwise, and the server's indifference
// to the payload's content (see above) means that choice needs no
// server-side change. Encoding rather than encrypting keeps this
// file's job to the wire format alone - what goes in the payload is
// SealMessage's business.
func EncodeSend(recipientUserID string, sealed []byte) ([]byte, error) {
	if recipientUserID == "" {
		return nil, errors.New("ws: EncodeSend: empty recipient user id")
	}
	return json.Marshal(outboundFrame{
		To:      recipientUserID,
		Payload: base64.StdEncoding.EncodeToString(sealed),
	})
}

// DecodeFrame parses one raw frame read off the connection into an
// Event. A frame the server is known to send but that's missing a
// required field (or whose payload isn't base64) is an error - that's
// a contract violation worth surfacing, not silently ignoring. A frame
// with an unrecognized "type" is not: synq-server's protocol has
// grown before (the key-exchange types in API.md), and a client that
// hard-fails on the next new event type would break every connection
// the moment the server adds one. Those come back with Type set and
// the rest empty for the caller to ignore.
func DecodeFrame(data []byte) (Event, error) {
	var f inboundFrame
	if err := json.Unmarshal(data, &f); err != nil {
		return Event{}, fmt.Errorf("ws: decode frame: %w", err)
	}
	if f.Type == "" {
		return Event{}, errors.New("ws: frame has no type")
	}

	switch f.Type {
	case EventTypeMessage:
		if f.From == "" {
			return Event{}, errors.New("ws: message frame has no from")
		}
		payload, err := decodePayload(f.Payload)
		if err != nil {
			return Event{}, err
		}
		return Event{Type: f.Type, From: f.From, Payload: payload}, nil

	case EventTypeNodeRequest, EventTypeNodeAccepted:
		if f.From == "" {
			return Event{}, fmt.Errorf("ws: %s frame has no from", f.Type)
		}
		return Event{Type: f.Type, From: f.From}, nil

	case EventTypeError:
		if f.Error == "" {
			return Event{}, errors.New("ws: error frame has no error")
		}
		return Event{Type: f.Type, Error: f.Error}, nil

	default:
		return Event{Type: f.Type, From: f.From}, nil
	}
}

// decodePayload pulls a message frame's base64 payload back into the
// raw bytes it was made from (see EncodeSend).
func decodePayload(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return nil, errors.New("ws: message frame has no payload")
	}
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return nil, errors.New("ws: message payload must be a base64 string")
	}
	payload, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("ws: message payload is not valid base64: %w", err)
	}
	return payload, nil
}
