package controller

import (
	"context"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/manager"
)

var _ manager.Runnable = (*ClusterInventoryNamespaceResolver)(nil)

func (r *ClusterInventoryNamespaceResolver) injectCacheForTest(key string, cancel context.CancelFunc, done <-chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.caches == nil {
		r.caches = map[string]*remoteNamespaceCache{}
	}
	r.caches[key] = &remoteNamespaceCache{
		cluster: key,
		cancel:  cancel,
		done:    done,
	}
}

func (r *ClusterInventoryNamespaceResolver) cacheCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.caches)
}

func TestResolverStartBlocksUntilContextCancelled(t *testing.T) {
	r := &ClusterInventoryNamespaceResolver{}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- r.Start(ctx)
	}()

	select {
	case err := <-done:
		t.Fatalf("Start returned before context cancellation: err=%v", err)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned non-nil error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start did not return after context cancellation")
	}
}

func TestResolverStartCancelsAllCaches(t *testing.T) {
	r := &ClusterInventoryNamespaceResolver{}

	cancelCalls := make(chan string, 3)
	for _, key := range []string{"alpha", "beta", "gamma"} {
		key := key
		entryCtx, entryCancel := context.WithCancel(context.Background())
		recordingCancel := func() {
			entryCancel()
			cancelCalls <- key
		}
		r.injectCacheForTest(key, recordingCancel, entryCtx.Done())
	}
	if got := r.cacheCount(); got != 3 {
		t.Fatalf("expected 3 seeded caches, got %d", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	startReturned := make(chan error, 1)
	go func() {
		startReturned <- r.Start(ctx)
	}()

	cancel()

	select {
	case err := <-startReturned:
		if err != nil {
			t.Fatalf("Start returned non-nil error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start did not return after context cancellation")
	}

	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		select {
		case key := <-cancelCalls:
			seen[key] = true
		case <-time.After(time.Second):
			t.Fatalf("only %d caches were cancelled (saw %v)", len(seen), seen)
		}
	}
	for _, key := range []string{"alpha", "beta", "gamma"} {
		if !seen[key] {
			t.Errorf("cache %q was not cancelled", key)
		}
	}

	if got := r.cacheCount(); got != 0 {
		t.Fatalf("expected caches map to be cleared, still has %d entries", got)
	}
}

func TestResolverCacheParentContextUsesManagerCtxAfterStart(t *testing.T) {
	r := &ClusterInventoryNamespaceResolver{}

	// Before Start: helper falls back to a non-nil background-rooted ctx that is NOT cancelled.
	pre := r.cacheParentContext()
	if pre == nil {
		t.Fatal("cacheParentContext returned nil before Start; expected non-nil background fallback")
	}
	if err := pre.Err(); err != nil {
		t.Fatalf("cacheParentContext before Start should be live; got Err=%v", err)
	}

	mgrCtx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- r.Start(mgrCtx)
	}()
	<-started

	// Wait for Start to capture mgrCtx into cacheParentCtx.
	deadline := time.After(time.Second)
	for r.cacheParentContext() != mgrCtx {
		select {
		case <-deadline:
			t.Fatal("cacheParentContext did not become mgrCtx after Start")
		case <-time.After(5 * time.Millisecond):
		}
	}

	// Cancelling mgrCtx must propagate to the captured ctx (proving informer cacheCtx,
	// built via context.WithCancel(r.cacheParentContext()), would also be cancelled).
	cancel()
	select {
	case <-r.cacheParentContext().Done():
	case <-time.After(time.Second):
		t.Fatal("manager ctx cancellation did not propagate to cacheParentContext")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start returned err: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start did not return after mgrCtx cancel")
	}
}
