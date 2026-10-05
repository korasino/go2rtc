package xiaomi

import "testing"

func TestAutoCenterModelGuard(t *testing.T) {
	for _, tc := range []struct {
		source string
		want   bool
	}{
		{"xiaomi://user:de@192.0.2.1?did=TEST&model=xiaomi.camera.c01a01", true},
		{"xiaomi://user:de@192.0.2.1?did=TEST&model=chuangmi.camera.72ac1", false},
		{"rtsp://192.0.2.1/camera?model=xiaomi.camera.c01a01", false},
		{"xiaomi://user:de@192.0.2.1?model=xiaomi.camera.c01a01-malicious", false},
	} {
		if got := isVerifiedC300Source(tc.source); got != tc.want {
			t.Errorf("isVerifiedC300Source(%q) = %t; want %t", tc.source, got, tc.want)
		}
	}
}

func TestPTZActionExclusion(t *testing.T) {
	const stream = "center-action-test"
	if !acquirePTZAction(stream) {
		t.Fatal("expected first action to acquire gate")
	}
	defer releasePTZAction(stream)
	if acquirePTZAction(stream) {
		t.Fatal("second action must not acquire the same stream")
	}
	if !acquirePTZAction("another-stream") {
		t.Fatal("independent streams should have their own gate")
	}
	releasePTZAction("another-stream")
	releasePTZAction(stream)
	if !acquirePTZAction(stream) {
		t.Fatal("released stream should allow a new action")
	}
}
