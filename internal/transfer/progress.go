package transfer

import (
	"context"
	"io"
	"sync/atomic"
)

// Phase is which part of a transfer is running.
type Phase int32

const (
	PhaseIdle Phase = iota
	PhaseEncrypting
	PhaseUploading
	PhaseDownloading
	PhaseVerifying
	PhaseDecrypting
)

func (p Phase) String() string {
	switch p {
	case PhaseEncrypting:
		return "encrypting"
	case PhaseUploading:
		return "uploading"
	case PhaseDownloading:
		return "downloading"
	case PhaseVerifying:
		return "verifying"
	case PhaseDecrypting:
		return "decrypting"
	default:
		return "starting"
	}
}

// Progress is a polled view of one running transfer: the work runs on a
// background goroutine and the UI reads Snapshot whenever it redraws, so
// nothing is sent between them and no lock is held. All methods are safe
// on a nil *Progress and then do nothing, so code that reports progress
// does not have to check whether anyone is listening.
type Progress struct {
	phase atomic.Int32
	done  atomic.Int64
	total atomic.Int64
}

// NewProgress returns a Progress in the idle phase.
func NewProgress() *Progress { return &Progress{} }

// Start begins a phase expected to move total bytes (0 if unknown).
func (p *Progress) Start(phase Phase, total int64) {
	if p == nil {
		return
	}
	p.done.Store(0)
	p.total.Store(total)
	p.phase.Store(int32(phase))
}

// Add records n more bytes done in the current phase.
func (p *Progress) Add(n int64) {
	if p != nil {
		p.done.Add(n)
	}
}

// Snapshot is the current phase and how far through it the transfer is.
func (p *Progress) Snapshot() (phase Phase, done, total int64) {
	if p == nil {
		return PhaseIdle, 0, 0
	}
	return Phase(p.phase.Load()), p.done.Load(), p.total.Load()
}

// Fraction is done/total in [0,1], or -1 when the total is unknown.
func (p *Progress) Fraction() float64 {
	_, done, total := p.Snapshot()
	if total <= 0 {
		return -1
	}
	return min(max(float64(done)/float64(total), 0), 1)
}

// reader wraps r so every byte read is counted. On a nil Progress it is r.
func (p *Progress) reader(r io.Reader) io.Reader {
	if p == nil {
		return r
	}
	return &countingReader{r: r, p: p}
}

type countingReader struct {
	r io.Reader
	p *Progress
}

func (c *countingReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.p.Add(int64(n))
	return n, err
}

type progressKey struct{}

// WithProgress returns a context that makes SendFile and ReceiveFile
// report to p. Carrying it in the context keeps their signatures - and
// every caller of them - as they were.
func WithProgress(ctx context.Context, p *Progress) context.Context {
	return context.WithValue(ctx, progressKey{}, p)
}

func progressFrom(ctx context.Context) *Progress {
	p, _ := ctx.Value(progressKey{}).(*Progress)
	return p
}
