module github.com/rendicott/marble-desktop-peer

// Local toolchain may be older; CI release builds use Go 1.25.x.
go 1.18

require (
	github.com/google/uuid v1.6.0
	github.com/gorilla/websocket v1.5.3
)
