package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestEventWakeNotifyWakesBeforeTimeout(t *testing.T) {
	wake := newEventWake()
	go func() {
		time.Sleep(20 * time.Millisecond)
		wake.Notify("proj-1")
	}()
	started := time.Now()
	if !wake.Wait(context.Background(), "proj-1", 5*time.Second) {
		t.Fatal("Wait must report the wake-up")
	}
	if elapsed := time.Since(started); elapsed >= 5*time.Second {
		t.Fatalf("Wait returned after %s, the wake-up must come earlier", elapsed)
	}
}

func TestEventWakeTimeout(t *testing.T) {
	wake := newEventWake()
	started := time.Now()
	if wake.Wait(context.Background(), "proj-1", 50*time.Millisecond) {
		t.Fatal("with no Notify, Wait must time out")
	}
	if elapsed := time.Since(started); elapsed < 40*time.Millisecond {
		t.Fatalf("Wait returned after %s, want the full maxWait", elapsed)
	}
}

func TestEventWakeContextCancel(t *testing.T) {
	wake := newEventWake()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	if wake.Wait(ctx, "proj-1", 10*time.Second) {
		t.Fatal("a canceled context must not count as a wake-up")
	}
}

func TestEventWakeProjectIsolation(t *testing.T) {
	wake := newEventWake()
	go func() {
		time.Sleep(10 * time.Millisecond)
		wake.Notify("proj-other")
	}()
	if wake.Wait(context.Background(), "proj-1", 80*time.Millisecond) {
		t.Fatal("another project's notification must not wake this waiter")
	}
}

func TestEventWakeBroadcastWakesAll(t *testing.T) {
	wake := newEventWake()
	const waiters = 4
	results := make(chan bool, waiters)
	for i := 0; i < waiters; i++ {
		go func(i int) {
			results <- wake.Wait(context.Background(), fmt.Sprintf("proj-%d", i), 2*time.Second)
		}(i)
	}
	time.Sleep(20 * time.Millisecond)
	wake.Notify("") // empty NOTIFY payload — wake everyone
	for i := 0; i < waiters; i++ {
		if !<-results {
			t.Fatal("a broadcast must wake every waiter")
		}
	}
}

func TestEventWakeWildcardWaiter(t *testing.T) {
	wake := newEventWake()
	go func() {
		time.Sleep(10 * time.Millisecond)
		wake.Notify("proj-1")
	}()
	if !wake.Wait(context.Background(), "", 2*time.Second) {
		t.Fatal("a wildcard waiter must be woken by any project notification")
	}
}

func TestEventWakeConcurrentWaitNotify(t *testing.T) {
	wake := newEventWake()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			wake.Wait(context.Background(), fmt.Sprintf("proj-%d", i%4), 20*time.Millisecond)
		}(i)
	}
	for i := 0; i < 64; i++ {
		wake.Notify(fmt.Sprintf("proj-%d", i%4))
	}
	wg.Wait()
	// The mux must stay usable after the churn: a fresh pair still works.
	done := make(chan bool, 1)
	go func() {
		done <- wake.Wait(context.Background(), "proj-late", 2*time.Second)
	}()
	time.Sleep(10 * time.Millisecond)
	wake.Notify("proj-late")
	if !<-done {
		t.Fatal("mux must keep working after concurrent churn")
	}
}
