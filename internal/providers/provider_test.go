package providers

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leock9/gonotch/internal/usage"
)

type fake struct{ polls atomic.Int32 }

func (*fake) ID() string        { return "fake" }
func (*fake) Name() string      { return "Fake" }
func (*fake) UsagePage() string { return "" }
func (*fake) Present() bool     { return true }
func (*fake) Probe() string     { return "" }
func (f *fake) Poll(context.Context, usage.Snapshot) (usage.Snapshot, time.Duration) {
	f.polls.Add(1)
	return usage.Snapshot{Status: usage.StatusOK}, time.Hour
}

func TestADisabledProviderIsNotPolledUntilSwitchedBackOn(t *testing.T) {
	f := &fake{}
	var enabled atomic.Bool
	r := NewRunner(f, func(usage.Snapshot) {})
	r.Enabled = enabled.Load
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx, usage.Snapshot{})
	time.Sleep(50 * time.Millisecond)
	if n := f.polls.Load(); n != 0 {
		t.Fatalf("polled %d times while disabled", n)
	}
	enabled.Store(true)
	r.RequestRefresh()
	deadline := time.Now().Add(time.Second)
	for f.polls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if f.polls.Load() != 1 {
		t.Fatalf("switching on should poll once, got %d", f.polls.Load())
	}
}
