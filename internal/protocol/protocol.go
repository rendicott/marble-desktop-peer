package protocol

import "encoding/json"

const Version = 1

type Caps struct {
	Browser bool `json:"browser"`
	Desktop bool `json:"desktop"`
	Confirm bool `json:"confirm"`
	// Exec advertises computer_exec support (run a shell command on the peer,
	// return stdout/stderr/exit_code as text) — added per field report
	// peer-gui-loop-report (2026-09-23) so state (files, logs, config) is
	// legible without reading screenshot pixels.
	Exec bool `json:"exec"`
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
}
