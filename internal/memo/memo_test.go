package memo

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fetchOf(v string, size int, store bool) func(context.Context) (string, int, bool, error) {
	return func(context.Context) (string, int, bool, error) { return v, size, store, nil }
}

func TestStoredValueIsPeekableAndUnstoredValueIsNot(t *testing.T) {
	c := New(100, 50, time.Hour)
	if _, err := Fetch(t.Context(), c, "kept", fetchOf("a", 10, true)); err != nil {
		t.Fatal(err)
	}
	if _, err := Fetch(t.Context(), c, "skipped", fetchOf("b", 10, false)); err != nil {
		t.Fatal(err)
	}
	if v, ok := Peek[string](t.Context(), c, "kept"); !ok || v != "a" {
		t.Fatalf("kept = %q, %v", v, ok)
	}
	if _, ok := Peek[string](t.Context(), c, "skipped"); ok {
		t.Fatal("a value the fetch declined to store was stored")
	}
}

func TestErrorsAreNeverStored(t *testing.T) {
	c := New(100, 50, time.Hour)
	_, err := Fetch(t.Context(), c, "k", func(context.Context) (string, int, bool, error) {
		return "partial", 1, true, errors.New("boom")
	})
	if err == nil {
		t.Fatal("want the error")
	}
	if _, ok := Peek[string](t.Context(), c, "k"); ok {
		t.Fatal("an errored fetch was stored")
	}
}

func TestEntryOverThePerEntryCapIsReturnedButNotStored(t *testing.T) {
	c := New(100, 20, time.Hour)
	v, err := Fetch(t.Context(), c, "big", fetchOf("x", 21, true))
	if err != nil || v != "x" {
		t.Fatalf("v=%q err=%v", v, err)
	}
	if c.Len() != 0 {
		t.Fatal("oversized entry stored")
	}
}

func TestLRUEvictsLeastRecentlyUsedByBytes(t *testing.T) {
	c := New(30, 30, time.Hour)
	for _, k := range []string{"a", "b", "c"} {
		_, _ = Fetch(t.Context(), c, k, fetchOf(k, 10, true))
	}
	Peek[string](t.Context(), c, "a") // a is now the most recent; b is the oldest
	_, _ = Fetch(t.Context(), c, "d", fetchOf("d", 10, true))
	if _, ok := Peek[string](t.Context(), c, "b"); ok {
		t.Fatal("b should have been evicted")
	}
	for _, k := range []string{"a", "c", "d"} {
		if _, ok := Peek[string](t.Context(), c, k); !ok {
			t.Fatalf("%s evicted", k)
		}
	}
	_, _ = Fetch(t.Context(), c, "e", fetchOf("e", 25, true)) // needs 25: evicts down to e alone
	if c.Len() != 1 {
		t.Fatalf("len = %d, want 1", c.Len())
	}
}

func TestEntriesExpireAfterMaxAge(t *testing.T) {
	c := New(100, 50, time.Hour)
	now := time.Now()
	c.now = func() time.Time { return now }
	_, _ = Fetch(t.Context(), c, "k", fetchOf("a", 1, true))
	now = now.Add(time.Hour + time.Second)
	if _, ok := Peek[string](t.Context(), c, "k"); ok {
		t.Fatal("expired entry served")
	}
	if c.Len() != 0 {
		t.Fatal("expired entry not dropped")
	}
}

func TestFlushDropsEntriesAndSuppressesInFlightStores(t *testing.T) {
	c := New(100, 50, time.Hour)
	_, _ = Fetch(t.Context(), c, "old", fetchOf("a", 1, true))
	c.Flush()
	if _, ok := Peek[string](t.Context(), c, "old"); ok {
		t.Fatal("flush kept an entry")
	}
	_, _ = Fetch(t.Context(), c, "racing", func(context.Context) (string, int, bool, error) {
		c.Flush() // a config reload lands while the fetch is in flight
		return "stale", 1, true, nil
	})
	if _, ok := Peek[string](t.Context(), c, "racing"); ok {
		t.Fatal("a fetch that began before the flush stored its result")
	}
}

func TestConcurrentFetchesOfOneKeyShareOneCall(t *testing.T) {
	c := New(100, 50, time.Hour)
	var calls atomic.Int32
	release := make(chan struct{})
	fn := func(context.Context) (string, int, bool, error) {
		calls.Add(1)
		<-release
		return "v", 1, false, nil
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			if v, err := Fetch(t.Context(), c, "k", fn); err != nil || v != "v" {
				t.Errorf("v=%q err=%v", v, err)
			}
		})
	}
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // let the others join the flight
	close(release)
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("fn ran %d times, want 1", n)
	}
}

func TestWaiterRetriesWhenTheLeaderFails(t *testing.T) {
	c := New(100, 50, time.Hour)
	leaderIn, release := make(chan struct{}), make(chan struct{})
	go func() {
		_, _ = Fetch(t.Context(), c, "k", func(context.Context) (string, int, bool, error) {
			close(leaderIn)
			<-release
			return "", 0, false, errors.New("leader cancelled")
		})
	}()
	<-leaderIn
	got := make(chan string, 1)
	go func() {
		v, _ := Fetch(t.Context(), c, "k", fetchOf("mine", 1, false))
		got <- v
	}()
	time.Sleep(20 * time.Millisecond)
	close(release)
	if v := <-got; v != "mine" {
		t.Fatalf("waiter got %q, want its own fetch", v)
	}
}
