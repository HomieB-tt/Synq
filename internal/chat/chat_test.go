package chat

import (
	"bytes"
	"sync"
	"testing"
	"time"
)

func TestStoreAppendAndMessagesRoundTrip(t *testing.T) {
	s := NewStore()
	contact := ContactKey("aa")

	s.Append(contact, Message{Body: []byte("hi"), At: time.Now(), Outgoing: true})
	s.Append(contact, Message{Body: []byte("how are you"), At: time.Now(), Outgoing: false})

	got := s.Messages(contact)
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2", len(got))
	}
	if string(got[0].Body) != "hi" || !got[0].Outgoing {
		t.Errorf("message 0 = %+v", got[0])
	}
	if string(got[1].Body) != "how are you" || got[1].Outgoing {
		t.Errorf("message 1 = %+v", got[1])
	}
}

func TestStoreMessagesReturnsACopy(t *testing.T) {
	s := NewStore()
	contact := ContactKey("aa")
	s.Append(contact, Message{Body: []byte("original")})

	got := s.Messages(contact)
	got[0].Body[0] = 'X'

	again := s.Messages(contact)
	if string(again[0].Body) != "original" {
		t.Fatalf("mutating a Messages() result affected the store: got %q", again[0].Body)
	}
}

func TestStoreMessagesForUnknownContactIsNil(t *testing.T) {
	s := NewStore()
	if got := s.Messages(ContactKey("never-seen")); got != nil {
		t.Fatalf("got %v, want nil", got)
	}
}

func TestStoreThreadsOrderedByFirstMessage(t *testing.T) {
	s := NewStore()
	s.Append(ContactKey("bob"), Message{Body: []byte("1")})
	s.Append(ContactKey("alice"), Message{Body: []byte("2")})
	s.Append(ContactKey("bob"), Message{Body: []byte("3")}) // second message to an existing thread must not re-order it

	got := s.Threads()
	want := []ContactKey{"bob", "alice"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestStoreHasThread(t *testing.T) {
	s := NewStore()
	contact := ContactKey("aa")

	if s.HasThread(contact) {
		t.Fatal("HasThread true before any message was appended")
	}
	s.Append(contact, Message{Body: []byte("hi")})
	if !s.HasThread(contact) {
		t.Fatal("HasThread false after a message was appended")
	}
}

func TestStorePurgeClearsEverythingAndZeroes(t *testing.T) {
	s := NewStore()
	body := []byte("sensitive plaintext")
	s.Append(ContactKey("aa"), Message{Body: body})

	s.Purge()

	if got := s.Threads(); len(got) != 0 {
		t.Fatalf("Threads() after Purge = %v, want empty", got)
	}
	if got := s.Messages(ContactKey("aa")); got != nil {
		t.Fatalf("Messages() after Purge = %v, want nil", got)
	}
	// Purge zeroes the message's own backing array in place - since
	// Append did not copy body, the original slice should now be all
	// zero bytes too.
	if !bytes.Equal(body, make([]byte, len(body))) {
		t.Fatalf("original backing bytes not zeroed after Purge: %q", body)
	}
}

func TestStoreConcurrentAppendAndRead(t *testing.T) {
	s := NewStore()
	const goroutines = 50
	const perGoroutine = 20

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)
	for g := 0; g < goroutines; g++ {
		contact := ContactKey(rune('a' + g%5))
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				s.Append(contact, Message{Body: []byte("x")})
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				s.Messages(contact)
				s.Threads()
				s.HasThread(contact)
			}
		}()
	}
	wg.Wait()

	total := 0
	for _, c := range s.Threads() {
		total += len(s.Messages(c))
	}
	if want := goroutines * perGoroutine; total != want {
		t.Fatalf("got %d total messages across all threads, want %d", total, want)
	}
}

func TestStoreLen(t *testing.T) {
	s := NewStore()
	if got := s.Len("alice"); got != 0 {
		t.Fatalf("Len of an unknown thread = %d, want 0", got)
	}
	s.Append("alice", Message{Body: []byte("one")})
	s.Append("alice", Message{Body: []byte("two")})
	s.Append("bob", Message{Body: []byte("x")})
	if got := s.Len("alice"); got != 2 {
		t.Errorf("Len(alice) = %d, want 2", got)
	}
}

func TestStoreSliceClampsAndOrders(t *testing.T) {
	s := NewStore()
	for _, b := range []string{"a", "b", "c", "d", "e"} {
		s.Append("alice", Message{Body: []byte(b)})
	}

	bodies := func(ms []Message) string {
		out := ""
		for _, m := range ms {
			out += string(m.Body)
		}
		return out
	}

	cases := []struct {
		name     string
		from, to int
		want     string
	}{
		{"middle", 1, 4, "bcd"},
		{"whole thread", 0, 5, "abcde"},
		{"to past the end is clamped", 3, 99, "de"},
		{"from before the start is clamped", -4, 2, "ab"},
		{"empty range", 2, 2, ""},
		{"inverted range", 4, 1, ""},
	}
	for _, c := range cases {
		if got := bodies(s.Slice("alice", c.from, c.to)); got != c.want {
			t.Errorf("%s: Slice(%d, %d) = %q, want %q", c.name, c.from, c.to, got, c.want)
		}
	}
	if got := s.Slice("nobody", 0, 3); got != nil {
		t.Errorf("Slice of an unknown thread = %v, want nil", got)
	}
}

// Slice, like Messages, must hand back copies: a caller scribbling on a
// returned body (or Purge zeroing the real one) must not reach the other.
func TestStoreSliceReturnsACopy(t *testing.T) {
	s := NewStore()
	s.Append("alice", Message{Body: []byte("secret")})

	got := s.Slice("alice", 0, 1)
	got[0].Body[0] = 'X'

	if again := s.Slice("alice", 0, 1); string(again[0].Body) != "secret" {
		t.Errorf("mutating a Slice result changed the store: %q", again[0].Body)
	}
}
