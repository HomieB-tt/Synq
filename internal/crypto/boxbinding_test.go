package crypto

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
)

func newTestIdentity(t *testing.T) *Identity {
	t.Helper()
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	return id
}

func TestBoxKeyBindingMessageLayoutMatchesTheServer(t *testing.T) {
	var box [32]byte
	for i := range box {
		box[i] = 0xAB
	}
	want := append([]byte("synq-box-key-v1\x00al\x00"), bytes.Repeat([]byte{0xAB}, 32)...)
	if got := BoxKeyBindingMessage("al", &box); !bytes.Equal(got, want) {
		t.Errorf("message = %x, want %x (src/auth/boxkey.rs pins the same bytes)", got, want)
	}
}

func TestSignedBoxKeyVerifies(t *testing.T) {
	id := newTestIdentity(t)
	sig := id.SignBoxKey("alice")
	if err := VerifyBoxKeyBinding(id.SigningPublicHex(), "alice", id.BoxPublicHex(), sig); err != nil {
		t.Fatalf("VerifyBoxKeyBinding: %v", err)
	}
}

func TestMissingSignatureIsReportedAsUnsigned(t *testing.T) {
	id := newTestIdentity(t)
	err := VerifyBoxKeyBinding(id.SigningPublicHex(), "alice", id.BoxPublicHex(), "")
	if !errors.Is(err, ErrBoxKeyUnsigned) {
		t.Errorf("err = %v, want ErrBoxKeyUnsigned", err)
	}
}

func TestSwappedBoxKeyIsRejected(t *testing.T) {
	alice, attacker := newTestIdentity(t), newTestIdentity(t)
	sig := alice.SignBoxKey("alice")
	err := VerifyBoxKeyBinding(alice.SigningPublicHex(), "alice", attacker.BoxPublicHex(), sig)
	if !errors.Is(err, ErrBoxKeyBindingInvalid) {
		t.Errorf("a substituted box key verified: err = %v", err)
	}
}

func TestBindingForAnotherUsernameIsRejected(t *testing.T) {
	id := newTestIdentity(t)
	sig := id.SignBoxKey("alice")
	err := VerifyBoxKeyBinding(id.SigningPublicHex(), "mallory", id.BoxPublicHex(), sig)
	if !errors.Is(err, ErrBoxKeyBindingInvalid) {
		t.Errorf("err = %v, want ErrBoxKeyBindingInvalid", err)
	}
}

func TestBindingFromAnotherIdentityIsRejected(t *testing.T) {
	real, attacker := newTestIdentity(t), newTestIdentity(t)
	// The attacker signs the real user's box key under their own key,
	// then presents it alongside the real user's identity key.
	sig := attacker.SignBoxKey("alice")
	err := VerifyBoxKeyBinding(real.SigningPublicHex(), "alice", real.BoxPublicHex(), sig)
	if !errors.Is(err, ErrBoxKeyBindingInvalid) {
		t.Errorf("err = %v, want ErrBoxKeyBindingInvalid", err)
	}
}

func TestAuthChallengeSignatureCannotPassAsABinding(t *testing.T) {
	id := newTestIdentity(t)
	// Sign exactly the bytes a binding would hold, minus the domain tag,
	// the way a login challenge signature could be induced to look.
	raw := append([]byte("alice\x00"), id.BoxPublic[:]...)
	sig := id.SignChallenge(string(raw))
	err := VerifyBoxKeyBinding(id.SigningPublicHex(), "alice", id.BoxPublicHex(), sig)
	if !errors.Is(err, ErrBoxKeyBindingInvalid) {
		t.Errorf("err = %v, want ErrBoxKeyBindingInvalid", err)
	}
}

func TestMalformedInputsAreRejectedNotPanicked(t *testing.T) {
	id := newTestIdentity(t)
	good := id.SignBoxKey("alice")
	short := hex.EncodeToString([]byte{1, 2, 3})
	for name, args := range map[string][3]string{
		"identity not hex":    {"zz", id.BoxPublicHex(), good},
		"identity too short":  {short, id.BoxPublicHex(), good},
		"box key not hex":     {id.SigningPublicHex(), "zz", good},
		"box key too short":   {id.SigningPublicHex(), short, good},
		"signature not hex":   {id.SigningPublicHex(), id.BoxPublicHex(), "zz"},
		"signature too short": {id.SigningPublicHex(), id.BoxPublicHex(), short},
	} {
		if err := VerifyBoxKeyBinding(args[0], "alice", args[1], args[2]); !errors.Is(err, ErrBoxKeyBindingInvalid) {
			t.Errorf("%s: err = %v, want ErrBoxKeyBindingInvalid", name, err)
		}
	}
}
