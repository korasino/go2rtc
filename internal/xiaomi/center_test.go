package xiaomi

import (
	"context"
	"errors"
	"testing"

	"github.com/AlexxIT/go2rtc/pkg/xiaomi/miss"
)

type centerFake struct {
	angle     int
	elevation int
	ret       int

	failRead bool
	failSet  bool

	setCalls int
	scripted []miss.PTZPosition
}

func (f *centerFake) RefreshPosition(ctx context.Context) (*miss.PTZPosition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.failRead {
		return nil, errors.New("read error")
	}

	return &miss.PTZPosition{
		Angle:     f.angle,
		Elevation: f.elevation,
		Ret:       f.ret,
	}, nil
}

func (f *centerFake) SetPosition(angle, elevation int) error {
	if f.failSet {
		return errors.New("motor error")
	}

	f.setCalls++

	if len(f.scripted) >= f.setCalls {
		position := f.scripted[f.setCalls-1]
		f.angle = position.Angle
		f.elevation = position.Elevation
		f.ret = position.Ret
		return nil
	}

	f.angle = angle
	f.elevation = elevation
	f.ret = 0
	return nil
}

func fastCenter() centerOptions {
	o := c300CenterOptions()
	o.Settle = 0
	return o
}

func TestCenterFromPreviouslyObservedOffset(t *testing.T) {
	f := &centerFake{angle: 4, elevation: 0}

	pos, err := autoCenter(context.Background(), f, fastCenter())
	if err != nil {
		t.Fatal(err)
	}

	if !centerReached(pos, fastCenter()) {
		t.Fatalf("not centered: %+v", pos)
	}

	if f.setCalls != 1 {
		t.Fatalf("expected one absolute move, got %d", f.setCalls)
	}
}

func TestCenterNoOpWhenAlreadyCentered(t *testing.T) {
	f := &centerFake{angle: 49, elevation: 55}

	pos, err := autoCenter(context.Background(), f, fastCenter())
	if err != nil {
		t.Fatal(err)
	}

	if f.setCalls != 0 {
		t.Fatalf("expected no movement, got %d calls", f.setCalls)
	}

	if pos.Angle != 49 || pos.Elevation != 55 {
		t.Fatalf("unexpected position: %+v", pos)
	}
}

func TestCenterAcceptsObservedHardwareAccuracy(t *testing.T) {
	f := &centerFake{
		angle:     48,
		elevation: 44,
		scripted: []miss.PTZPosition{
			{Angle: 47, Elevation: 49, Ret: 0},
		},
	}

	pos, err := autoCenter(context.Background(), f, fastCenter())
	if err != nil {
		t.Fatal(err)
	}

	if pos.Angle != 47 || pos.Elevation != 49 {
		t.Fatalf("unexpected position: %+v", pos)
	}

	if f.setCalls != 1 {
		t.Fatalf("expected one absolute move, got %d", f.setCalls)
	}
}

func TestCenterRetriesUntilFeedbackReachesTarget(t *testing.T) {
	f := &centerFake{
		angle:     4,
		elevation: 0,
		scripted: []miss.PTZPosition{
			{Angle: 40, Elevation: 40, Ret: 0},
			{Angle: 49, Elevation: 52, Ret: 0},
		},
	}

	pos, err := autoCenter(context.Background(), f, fastCenter())
	if err != nil {
		t.Fatal(err)
	}

	if !centerReached(pos, fastCenter()) {
		t.Fatalf("not centered: %+v", pos)
	}

	if f.setCalls != 2 {
		t.Fatalf("expected two attempts, got %d", f.setCalls)
	}
}

func TestCenterMaxAttempts(t *testing.T) {
	f := &centerFake{
		angle:     4,
		elevation: 0,
		scripted: []miss.PTZPosition{
			{Angle: 4, Elevation: 0, Ret: 0},
			{Angle: 4, Elevation: 0, Ret: 0},
		},
	}

	o := fastCenter()
	o.MaxAttempts = 2

	pos, err := autoCenter(context.Background(), f, o)
	if !errors.Is(err, errCenterAttempts) {
		t.Fatalf("expected max-attempt error, got %v", err)
	}

	if f.setCalls != 2 {
		t.Fatalf("expected two attempts, got %d", f.setCalls)
	}

	if pos.Angle != 4 || pos.Elevation != 0 {
		t.Fatalf("unexpected final position: %+v", pos)
	}
}

func TestCenterCancelledBeforeMotion(t *testing.T) {
	f := &centerFake{angle: 4, elevation: 0}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := autoCenter(ctx, f, fastCenter())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}

	if f.setCalls != 0 {
		t.Fatalf("expected no movement, got %d calls", f.setCalls)
	}
}

func TestCenterRejectsNonzeroMotorStatus(t *testing.T) {
	f := &centerFake{angle: 4, elevation: 0, ret: 5}

	_, err := autoCenter(context.Background(), f, fastCenter())
	if err == nil {
		t.Fatal("expected error")
	}

	if f.setCalls != 0 {
		t.Fatalf("expected no movement, got %d calls", f.setCalls)
	}
}

func TestCenterReadAndMotorFailures(t *testing.T) {
	tests := []struct {
		name string
		fake centerFake
	}{
		{
			name: "read",
			fake: centerFake{
				angle:     4,
				elevation: 0,
				failRead:  true,
			},
		},
		{
			name: "set position",
			fake: centerFake{
				angle:     4,
				elevation: 0,
				failSet:   true,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.fake

			_, err := autoCenter(context.Background(), &f, fastCenter())
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestCenterInvalidOptions(t *testing.T) {
	f := &centerFake{angle: 4, elevation: 0}

	o := fastCenter()
	o.MaxAttempts = 0

	_, err := autoCenter(context.Background(), f, o)
	if err == nil {
		t.Fatal("expected invalid options error")
	}

	if f.setCalls != 0 {
		t.Fatalf("expected no movement, got %d calls", f.setCalls)
	}
}
