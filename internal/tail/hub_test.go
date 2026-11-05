package tail

import (
	"sync"
	"testing"
	"time"

	"github.com/cragone/Super-Argus/internal/wal"
	"github.com/stretchr/testify/require"
)

func batch(n int) []wal.Record {
	rs := make([]wal.Record, n)
	for i := range rs {
		rs[i] = wal.Record{StreamID: 1, TS: int64(i), Line: "x"}
	}
	return rs
}

func TestFanOutToAllSubscribers(t *testing.T) {
	h := New(Config{Buffer: 4})
	a, _ := h.Subscribe("u1")
	b, _ := h.Subscribe("u2")
	h.Publish(batch(2))
	require.Len(t, <-a.C(), 2)
	require.Len(t, <-b.C(), 2)
	require.Equal(t, 2, h.Active())

	a.Close()
	h.Publish(batch(1))
	require.Len(t, <-b.C(), 1)
	select {
	case <-a.C():
		t.Fatal("closed subscriber still receives")
	default:
	}
	require.Equal(t, 1, h.Active())
}

func TestPublishWithoutSubscribersIsANoop(t *testing.T) {
	h := New(DefaultConfig())
	h.Publish(batch(10))
	h.Publish(nil)
	require.Zero(t, h.Active())
}

func TestSlowSubscriberDropsAndNeverBlocksPublish(t *testing.T) {
	h := New(Config{Buffer: 2})
	slow, _ := h.Subscribe("slow")
	fast, _ := h.Subscribe("fast")

	got := 0
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // fast consumer keeps up
		defer wg.Done()
		for b := range fast.C() {
			got += len(b)
			if got >= 500 {
				return
			}
		}
	}()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ { // 5 lines each; `slow` never reads
			h.Publish(batch(5))
			time.Sleep(time.Millisecond)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a stalled subscriber")
	}
	wg.Wait()
	require.Equal(t, 500, got, "fast subscriber is unaffected")
	require.EqualValues(t, 98*5, slow.TakeDropped(), "slow subscriber lost everything beyond its 2-batch buffer")
	require.Zero(t, slow.TakeDropped(), "counter resets")
}

func TestCaps(t *testing.T) {
	h := New(Config{PerUser: 2, Max: 3, Buffer: 1})
	a1, err := h.Subscribe("a")
	require.NoError(t, err)
	_, err = h.Subscribe("a")
	require.NoError(t, err)
	_, err = h.Subscribe("a")
	require.ErrorIs(t, err, ErrTooManyTails, "per-user cap")

	_, err = h.Subscribe("b")
	require.NoError(t, err)
	_, err = h.Subscribe("c")
	require.ErrorIs(t, err, ErrTooManyTails, "global cap")

	a1.Close()
	a1.Close() // idempotent
	_, err = h.Subscribe("c")
	require.NoError(t, err, "slot freed by Close")
	require.Equal(t, 3, h.Active())
}

func TestConcurrentPublishSubscribeClose(t *testing.T) {
	h := New(Config{Buffer: 8})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if s, err := h.Subscribe("u"); err == nil {
					select {
					case <-s.C():
					default:
					}
					s.Close()
				}
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				h.Publish(batch(3))
			}
		}()
	}
	wg.Wait()
	require.Zero(t, h.Active())
}
