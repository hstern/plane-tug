// Copyright 2026 The plane-tug Authors
// SPDX-License-Identifier: Apache-2.0

package bridge

import (
	"sync"
	"testing"
	"time"
)

func TestHubBroadcastToProject(t *testing.T) {
	t.Parallel()
	h := NewHub()
	subA, unsubA := h.Subscribe("p-1")
	defer unsubA()
	subB, unsubB := h.Subscribe("p-2")
	defer unsubB()

	h.Broadcast(Event{Event: "issue", Project: "p-1"})

	if got := recvWithin(subA.Events, 100*time.Millisecond); got.Project != "p-1" {
		t.Fatalf("subA got %+v, want project p-1", got)
	}
	select {
	case got := <-subB.Events:
		t.Fatalf("subB unexpectedly got %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestHubBroadcastEmptyProjectFansToAll(t *testing.T) {
	t.Parallel()
	h := NewHub()
	subA, unsubA := h.Subscribe("p-1")
	defer unsubA()
	subB, unsubB := h.Subscribe("p-2")
	defer unsubB()

	h.Broadcast(Event{Event: "workspace"})

	if got := recvWithin(subA.Events, 100*time.Millisecond); got.Event != "workspace" {
		t.Fatalf("subA got %+v", got)
	}
	if got := recvWithin(subB.Events, 100*time.Millisecond); got.Event != "workspace" {
		t.Fatalf("subB got %+v", got)
	}
}

func TestHubUnsubscribeRemoves(t *testing.T) {
	t.Parallel()
	h := NewHub()
	sub, unsub := h.Subscribe("p-1")
	if got := h.SubscriberCount("p-1"); got != 1 {
		t.Fatalf("count after subscribe = %d, want 1", got)
	}
	unsub()
	unsub() // idempotent
	if got := h.SubscriberCount("p-1"); got != 0 {
		t.Fatalf("count after unsubscribe = %d, want 0", got)
	}
	// Channel must be closed exactly once.
	if _, ok := <-sub.Events; ok {
		t.Fatal("Events channel must be closed after unsubscribe")
	}
}

func TestHubSlowSubscriberDoesNotBlock(t *testing.T) {
	t.Parallel()
	h := NewHub()
	// Subscriber never reads — fills buffer immediately.
	_, unsub := h.Subscribe("p-1")
	defer unsub()

	done := make(chan struct{})
	go func() {
		for range subscriberBuffer * 4 {
			h.Broadcast(Event{Event: "issue", Project: "p-1"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Broadcast blocked on slow subscriber")
	}
}

func TestHubConcurrentSubscribeBroadcast(t *testing.T) {
	t.Parallel()
	h := NewHub()
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			sub, unsub := h.Subscribe("p")
			defer unsub()
			timeout := time.After(200 * time.Millisecond)
			for {
				select {
				case <-sub.Events:
				case <-timeout:
					return
				}
			}
		})
	}
	for range 100 {
		go h.Broadcast(Event{Event: "issue", Project: "p"})
	}
	wg.Wait()
}

func recvWithin(ch <-chan Event, d time.Duration) Event {
	select {
	case e := <-ch:
		return e
	case <-time.After(d):
		return Event{}
	}
}
