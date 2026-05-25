// Copyright 2026 The plane-tug Authors
// SPDX-License-Identifier: Apache-2.0

package bridge

import "sync"

// subscriberBuffer is the per-client event buffer depth. Events past the
// buffer are dropped — the channel is a push hint, not an authoritative
// log; a client that falls behind will catch up on its next reload.
const subscriberBuffer = 16

// Hub fans webhook events out to SSE subscribers, keyed by project UUID.
// The zero value is not usable; call NewHub.
//
// A subscriber for project "P" receives events whose Project is "P"
// AND events whose Project is "" (workspace-level or un-projected
// events — broadcast to all subscribers because the bridge cannot
// know which projects are affected).
type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[*Subscriber]struct{}
}

// Subscriber is a single client's view of the hub. Receive on Events;
// the channel is buffered and a slow consumer drops events rather than
// blocking the broadcaster.
type Subscriber struct {
	Events chan Event
}

// NewHub returns an empty hub ready for Subscribe/Broadcast.
func NewHub() *Hub {
	return &Hub{subs: make(map[string]map[*Subscriber]struct{})}
}

// Subscribe registers a new subscriber for project. The returned unsubscribe
// function MUST be called when the client disconnects; it removes the
// subscriber from the hub and closes the Events channel.
func (h *Hub) Subscribe(project string) (*Subscriber, func()) {
	sub := &Subscriber{Events: make(chan Event, subscriberBuffer)}
	h.mu.Lock()
	if h.subs[project] == nil {
		h.subs[project] = make(map[*Subscriber]struct{})
	}
	h.subs[project][sub] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return sub, func() {
		once.Do(func() {
			h.mu.Lock()
			if set, ok := h.subs[project]; ok {
				delete(set, sub)
				if len(set) == 0 {
					delete(h.subs, project)
				}
			}
			h.mu.Unlock()
			close(sub.Events)
		})
	}
}

// Broadcast fans evt out to every subscriber for evt.Project, plus every
// subscriber for any project if evt.Project is empty. Sends are
// non-blocking: if a subscriber's buffer is full, the event is dropped
// for that subscriber.
func (h *Hub) Broadcast(evt Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if evt.Project == "" {
		for _, set := range h.subs {
			for sub := range set {
				sendOrDrop(sub.Events, evt)
			}
		}
		return
	}
	for sub := range h.subs[evt.Project] {
		sendOrDrop(sub.Events, evt)
	}
}

// SubscriberCount returns the number of subscribers for project. Useful
// for tests and a future /metrics endpoint.
func (h *Hub) SubscriberCount(project string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs[project])
}

func sendOrDrop(ch chan Event, evt Event) {
	select {
	case ch <- evt:
	default:
	}
}
