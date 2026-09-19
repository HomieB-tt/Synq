package crypto

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
)

func genKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pub
}

func TestFingerprintIsSymmetric(t *testing.T) {
	a := genKey(t)
	b := genKey(t)

	fab := Fingerprint(a, b)
	fba := Fingerprint(b, a)

	if fab != fba {
		t.Errorf("fingerprint not symmetric: Fingerprint(a,b)=%q, Fingerprint(b,a)=%q", fab, fba)
	}
}

func TestFingerprintIsDeterministic(t *testing.T) {
	a := genKey(t)
	b := genKey(t)

	f1 := Fingerprint(a, b)
	f2 := Fingerprint(a, b)

	if f1 != f2 {
		t.Errorf("fingerprint not deterministic: got %q then %q", f1, f2)
	}
}

func TestFingerprintDiffersForDifferentKeys(t *testing.T) {
	a := genKey(t)
	b := genKey(t)
	c := genKey(t)

	fab := Fingerprint(a, b)
	fac := Fingerprint(a, c)

	if fab == fac {
		t.Error("expected different key pairs to produce different fingerprints")
	}
}

func TestFingerprintFormat(t *testing.T) {
	a := genKey(t)
	b := genKey(t)

	f := Fingerprint(a, b)

	groups := strings.Split(f, " ")
	if len(groups) != 10 {
		t.Fatalf("expected 10 groups, got %d (%q)", len(groups), f)
	}
	for _, g := range groups {
		if len(g) != 4 {
			t.Errorf("expected each group to be 4 chars, got %q", g)
		}
	}
	if f != strings.ToUpper(f) {
		t.Errorf("expected uppercase hex, got %q", f)
	}
}
