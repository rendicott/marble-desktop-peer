package protocol

import "encoding/json"

// Version 2 adds the peer lock (ADR-0021 multi-harness): harness → peer
// {"type":"lock","kind":"acquire"|"release"}, peer → harness "lock_state", and
// hello_ack instance_id / harness_name. A v1 harness gets an implicit lock.
const Version = 2

type Caps struct {
	Browser bool `json:"browser"`
	Desktop bool `json:"desktop"`
	Confirm bool `json:"confirm"`
	// Exec advertises computer_exec support (run a shell command on the peer,
	// return stdout/stderr/exit_code as text) — added per field report
	// peer-gui-loop-report (2026-09-23) so state (files, logs, config) is
	// legible without reading screenshot pixels.
	Exec bool `json:"exec"`
	// Lock advertises protocol-v2 locking: actions are refused unless the
	// sending harness holds the peer lock.
	Lock bool `json:"lock"`
	// Region advertises screenshot region capture: {region, space, scale, max_edge}
	// on "screenshot", meta.region/zoom/downscaled, and desktop_click zoom=true.
	Region bool `json:"region"`
}

type Envelope struct {
	Type            string                 `json:"type"`
	ProtocolVersion int                    `json:"protocol_version,omitempty"`
	ID              string                 `json:"id,omitempty"`
	DeviceID        string                 `json:"device_id,omitempty"`
	Token           string                 `json:"token,omitempty"`
	ComputerID      string                 `json:"computer_id,omitempty"`
	Kind            string                 `json:"kind,omitempty"`
	DeadlineMS      int64                  `json:"deadline_ms,omitempty"`
	Payload         json.RawMessage        `json:"payload,omitempty"`
	OK              bool                   `json:"ok,omitempty"`
	Error           string                 `json:"error,omitempty"`
	Caps            *Caps                  `json:"caps,omitempty"`
	OS              string                 `json:"os,omitempty"`
	PeerVersion     string                 `json:"peer_version,omitempty"`
	ScreenshotB64   string                 `json:"screenshot_b64,omitempty"`
	Text            string                 `json:"text,omitempty"`
	Meta            map[string]interface{} `json:"meta,omitempty"`
	// InstanceID (hello_ack) identifies one harness process lifetime; a new id
	// on reconnect means the harness restarted and any lock it held is stale.
	InstanceID string `json:"instance_id,omitempty"`
	// HarnessName (hello_ack) is a human label for the tray / mini UI.
	HarnessName string `json:"harness_name,omitempty"`
}
