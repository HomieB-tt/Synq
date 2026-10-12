package ws

// Epochs keeps the replay-protection state for one contact across the
// life of this process. It exists as its own type, rather than as fields
// reset whenever a handshake is processed, because that reset is exactly
// what an attacker would try to provoke: if a relay could make the
// client forget which counters it had accepted - by replaying an old
// handshake, say - it could then replay old messages too.
//
// State is therefore keyed by the pair of ephemeral public keys that
// determine the session key (ours, fixed for a launch, and the
// contact's), and is only ever added to, never cleared. The same pair
// always means the same key, so its counters must keep counting no
// matter how many handshake frames arrive in between.
//
// The zero value is ready to use. Not safe for concurrent use.
type Epochs struct {
	byPair  map[epochPair]*Epoch
	retired map[[32]byte]struct{}
}

type epochPair struct {
	local, peer [32]byte
}

// Epoch is the counter state for one session key: the counter for what
// we send under it and the window of what we have accepted from the
// contact under it.
type Epoch struct {
	Send SendCounter
	Recv ReplayWindow
}

// keyOrZero lets callers pass a nil ephemeral key (a session that has no
// peer ephemeral recorded yet) without a special case; nil and the zero
// key share an epoch.
func keyOrZero(k *[32]byte) [32]byte {
	if k == nil {
		return [32]byte{}
	}
	return *k
}

// For returns the Epoch for the session key built from our ephemeral
// public key local and the contact's ephemeral public key peer, creating
// it on first use. The returned pointer stays valid and keeps its state.
func (e *Epochs) For(local, peer *[32]byte) *Epoch {
	if e.byPair == nil {
		e.byPair = make(map[epochPair]*Epoch)
	}
	pair := epochPair{local: keyOrZero(local), peer: keyOrZero(peer)}
	ep := e.byPair[pair]
	if ep == nil {
		ep = &Epoch{}
		e.byPair[pair] = ep
	}
	return ep
}

// Retire records that peer is a contact ephemeral key they have moved on
// from: a contact's ephemeral key changes only when they relaunch, and
// is never reused afterwards. A handshake frame carrying a retired key is
// therefore stale or replayed, and Retired reports it so the caller can
// ignore it rather than step backwards onto a key whose messages may
// also be replayed.
func (e *Epochs) Retire(peer *[32]byte) {
	if peer == nil {
		return
	}
	if e.retired == nil {
		e.retired = make(map[[32]byte]struct{})
	}
	e.retired[*peer] = struct{}{}
}

// Retired reports whether peer was passed to Retire.
func (e *Epochs) Retired(peer *[32]byte) bool {
	if peer == nil {
		return false
	}
	_, ok := e.retired[*peer]
	return ok
}
