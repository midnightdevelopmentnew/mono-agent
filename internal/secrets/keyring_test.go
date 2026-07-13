package secrets

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

// resetKEKState clears the process-wide kekOnce/kekCache/kekErr memoization
// so each test starts as if in a fresh process, regardless of what earlier
// tests in this package did to the shared KEK state.
func resetKEKState(t *testing.T) {
	t.Helper()
	kekMu.Lock()
	kekOnce = &sync.Once{}
	kekCache = nil
	kekErr = nil
	kekMu.Unlock()
}

func TestGetOrCreateKEK_PersistsAcrossCalls(t *testing.T) {
	resetKEKState(t)
	keyring.MockInit() // in-memory mock backend, no real OS keychain touched in tests

	key1, err := getOrCreateKEK()
	if err != nil {
		t.Fatalf("getOrCreateKEK (first call): %v", err)
	}
	if len(key1) != 32 {
		t.Fatalf("expected 32-byte key, got %d bytes", len(key1))
	}

	key2, err := getOrCreateKEK()
	if err != nil {
		t.Fatalf("getOrCreateKEK (second call): %v", err)
	}
	if string(key1) != string(key2) {
		t.Fatal("second call must return the same KEK, not regenerate")
	}
}

// TestGetOrCreateKEK_FailedAttemptIsNotCached exercises the regression this
// change fixes: a transient failure on the very first getOrCreateKEK call in
// the process (e.g. a momentarily locked OS keychain) must not be memoized
// forever. The next call after the failure should retry from scratch and
// succeed, rather than replaying the stale cached error for the rest of the
// process's lifetime.
func TestGetOrCreateKEK_FailedAttemptIsNotCached(t *testing.T) {
	resetKEKState(t)
	keyring.MockInit()

	injectedErr := errors.New("injected transient failure")

	orig := fetchKEK
	t.Cleanup(func() { fetchKEK = orig })

	fetchKEK = func() ([]byte, error) {
		return nil, injectedErr
	}

	_, err := getOrCreateKEK()
	if !errors.Is(err, injectedErr) {
		t.Fatalf("first call: expected injected error, got %v", err)
	}

	// Restore the real fetch behavior; the NEXT call must retry from
	// scratch instead of replaying the cached failure.
	fetchKEK = orig

	key, err := getOrCreateKEK()
	if err != nil {
		t.Fatalf("second call after failure: expected success, got error: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("expected 32-byte KEK, got %d bytes", len(key))
	}
}

// TestGetOrCreateKEK_ConcurrentFailureIsSharedThenRetried proves multiple
// goroutines racing on the single in-flight (failing) first attempt observe
// the identical error together, and that only the NEXT call after that
// attempt completes gets a fresh retry.
func TestGetOrCreateKEK_ConcurrentFailureIsSharedThenRetried(t *testing.T) {
	resetKEKState(t)
	keyring.MockInit()

	injectedErr := errors.New("injected transient failure")

	orig := fetchKEK
	t.Cleanup(func() { fetchKEK = orig })

	const numGoroutines = 20

	// arrived holds the winning goroutine's fetchKEK call open until every
	// other goroutine has entered getOrCreateKEK and picked up the same
	// (not-yet-replaced) kekOnce, so they all share the one in-flight
	// attempt instead of racing ahead onto a freshly-swapped Once.
	var arrived sync.WaitGroup
	arrived.Add(numGoroutines)

	var calls int32
	fetchKEK = func() ([]byte, error) {
		atomic.AddInt32(&calls, 1)
		arrived.Wait()
		time.Sleep(50 * time.Millisecond)
		return nil, injectedErr
	}

	errs := make([]error, numGoroutines)

	var wg sync.WaitGroup
	var start sync.WaitGroup
	start.Add(1)
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			start.Wait()
			arrived.Done()
			_, errs[i] = getOrCreateKEK()
		}(i)
	}
	start.Done()
	wg.Wait()

	for i, err := range errs {
		if !errors.Is(err, injectedErr) {
			t.Fatalf("goroutine %d: expected injected error, got %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 fetch attempt for the concurrent failing first use, got %d", got)
	}

	// Now that the failing attempt is done, the next call must retry from
	// scratch and succeed once the fetch behavior is restored.
	fetchKEK = orig

	key, err := getOrCreateKEK()
	if err != nil {
		t.Fatalf("call after concurrent failure: expected success, got error: %v", err)
	}
	if len(key) != 32 {
		t.Fatalf("expected 32-byte KEK, got %d bytes", len(key))
	}
}
