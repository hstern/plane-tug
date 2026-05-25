// Copyright 2026 The plane-tug Authors
// SPDX-License-Identifier: Apache-2.0

package bridge

import (
	"encoding/json"
	"errors"
)

// Event is the minimal projection of a Plane webhook payload that the bridge
// forwards to subscribers. The bridge stays dumb on purpose — it does not
// model Plane's full event vocabulary. Clients receive enough to know what
// changed in broad strokes and decide whether to act.
//
// Project is the project UUID extracted from data.project. It is the key the
// hub fans out on; an empty Project means "workspace-level event" and is
// broadcast to all subscribers.
type Event struct {
	Event   string `json:"event"`
	Action  string `json:"action,omitempty"`
	Project string `json:"project,omitempty"`
}

// ErrMalformedPayload is returned when the webhook body is not valid JSON
// or lacks the minimum {event} field.
var ErrMalformedPayload = errors.New("malformed webhook payload")

// ParseEvent extracts {event, action, data.project} from a raw webhook body.
// Unknown fields are ignored — Plane is free to add event types or payload
// fields without breaking the bridge.
func ParseEvent(body []byte) (Event, error) {
	var raw struct {
		Event  string          `json:"event"`
		Action string          `json:"action"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Event{}, ErrMalformedPayload
	}
	if raw.Event == "" {
		return Event{}, ErrMalformedPayload
	}
	evt := Event{Event: raw.Event, Action: raw.Action}
	if len(raw.Data) > 0 {
		var d struct {
			Project string `json:"project"`
		}
		// A non-object data block (e.g. an array for bulk events) is not an
		// error — we just have no project to key on.
		_ = json.Unmarshal(raw.Data, &d)
		evt.Project = d.Project
	}
	return evt, nil
}
