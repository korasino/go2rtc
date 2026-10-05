package xiaomi

import (
	"net/url"
	"sync"

	"github.com/AlexxIT/go2rtc/internal/streams"
)

// All go2rtc Xiaomi PTZ HTTP actions are mutually exclusive per stream while
// Auto-Center is in progress. In particular, a manual move must not fight the
// feedback loop. This only coordinates the /api/xiaomi/ptz handler, not any
// external ONVIF controller added by a separate patch.
var ptzActionState = struct {
	mu   sync.Mutex
	busy map[string]bool
}{busy: make(map[string]bool)}

func acquirePTZAction(stream string) bool {
	ptzActionState.mu.Lock()
	defer ptzActionState.mu.Unlock()
	if ptzActionState.busy[stream] {
		return false
	}
	ptzActionState.busy[stream] = true
	return true
}

func releasePTZAction(stream string) {
	ptzActionState.mu.Lock()
	delete(ptzActionState.busy, stream)
	ptzActionState.mu.Unlock()
}

// Limit the first hardware-facing version to the Xiaomi C300 variant tested
// with MOTOR_RESP position telemetry (xiaomi.camera.c01a01). Other models need
// their direction and coordinate behavior verified before enabling movement.
func supportsAutoCenter(streamName string) bool {
	stream := streams.Get(streamName)
	if stream == nil {
		return false
	}
	for _, raw := range stream.Sources() {
		if isVerifiedC300Source(raw) {
			return true
		}
	}
	return false
}

func isVerifiedC300Source(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "xiaomi" && u.Query().Get("model") == "xiaomi.camera.c01a01"
}
