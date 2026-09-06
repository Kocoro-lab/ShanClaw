// Nested module on purpose. gomobile pulls golang.org/x/mobile plus a chain of
// golang.org/x updates; keeping it here means the daemon's own go.mod never
// moves — in particular x/crypto, which the self-update signature check rides on.
//
// The replace is a relative path, NOT a version: the binding must always build
// against the checked-out daemon, or every koe change would need a release.
module github.com/Kocoro-lab/ShanClaw/mobile

go 1.25.7

require github.com/Kocoro-lab/ShanClaw v0.0.0

require (
	github.com/ebitengine/oto/v3 v3.4.0 // indirect
	github.com/ebitengine/purego v0.9.0 // indirect
	github.com/gen2brain/malgo v0.11.25 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/pion/datachannel v1.6.0 // indirect
	github.com/pion/dtls/v3 v3.1.4 // indirect
	github.com/pion/ice/v4 v4.2.7 // indirect
	github.com/pion/interceptor v0.1.45 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/mdns/v2 v2.1.0 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/rtcp v1.2.16 // indirect
	github.com/pion/rtp v1.10.2 // indirect
	github.com/pion/sctp v1.10.0 // indirect
	github.com/pion/sdp/v3 v3.0.18 // indirect
	github.com/pion/srtp/v3 v3.0.11 // indirect
	github.com/pion/stun/v3 v3.1.5 // indirect
	github.com/pion/transport/v4 v4.0.2 // indirect
	github.com/pion/turn/v5 v5.0.9 // indirect
	github.com/pion/webrtc/v4 v4.2.15 // indirect
	github.com/wlynxg/anet v0.0.5 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/mobile v0.0.0-20260813181013-1960c775504c // indirect
	golang.org/x/mod v0.39.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/time v0.14.0 // indirect
	golang.org/x/tools v0.49.0 // indirect
	gopkg.in/hraban/opus.v2 v2.0.0-20230925203106-0188a62cb302 // indirect
)

replace github.com/Kocoro-lab/ShanClaw => ../

tool golang.org/x/mobile/cmd/gobind
