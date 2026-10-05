package xiaomi

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/xiaomi/miss"
)

// centerController is deliberately smaller than ptzController. It uses the
// already-active camera session; starting another P2P session can drop video.
type centerController interface {
	RefreshPosition(ctx context.Context) (*miss.PTZPosition, error)
	SetPosition(angle, elevation int) error
}

// centerOptions is deliberately fixed for the first tested model,
// xiaomi.camera.c01a01. The coordinates are device units, not degrees.
type centerOptions struct {
	Angle       int
	Elevation   int
	AngleTol    int
	ElevTol     int
	MaxAttempts int
	Settle      time.Duration
}

func c300CenterOptions() centerOptions {
	return centerOptions{
		Angle:       50,
		Elevation:   50,
		AngleTol:    3,
		ElevTol:     5,
		MaxAttempts: 3,
		Settle:      2 * time.Second,
	}
}

var errCenterAttempts = errors.New("xiaomi ptz: auto-center: target position not reached")

// autoCenter uses absolute motor positioning followed by position feedback.
//
// On xiaomi.camera.c01a01 operation 13 was verified on real hardware:
// a requested position of 48/50 resulted in reported position 47/49.
//
// The command is therefore treated as a target request rather than proof that
// the camera reached the requested coordinates. Every movement is followed by
// a fresh 0x113 position request and checked against the configured tolerance.
func autoCenter(ctx context.Context, ctrl centerController, opt centerOptions) (*miss.PTZPosition, error) {
	if opt.MaxAttempts <= 0 ||
		opt.Settle < 0 ||
		opt.AngleTol < 0 ||
		opt.ElevTol < 0 {
		return nil, errors.New("xiaomi ptz: auto-center: invalid options")
	}

	position, err := centerRefresh(ctx, ctrl)
	if err != nil {
		return nil, err
	}

	if centerReached(position, opt) {
		return position, nil
	}

	for attempt := 0; attempt < opt.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return position, err
		}

		if err := ctrl.SetPosition(opt.Angle, opt.Elevation); err != nil {
			return position, fmt.Errorf(
				"xiaomi ptz: auto-center: set position %d/%d: %w",
				opt.Angle,
				opt.Elevation,
				err,
			)
		}

		if err := centerWait(ctx, opt.Settle); err != nil {
			return position, err
		}

		position, err = centerRefresh(ctx, ctrl)
		if err != nil {
			return position, err
		}

		if centerReached(position, opt) {
			return position, nil
		}
	}

	return position, fmt.Errorf(
		"%w: last position=%d/%d target=%d/%d",
		errCenterAttempts,
		position.Angle,
		position.Elevation,
		opt.Angle,
		opt.Elevation,
	)
}

func centerReached(position *miss.PTZPosition, opt centerOptions) bool {
	return centerAbs(opt.Angle-position.Angle) <= opt.AngleTol &&
		centerAbs(opt.Elevation-position.Elevation) <= opt.ElevTol
}

func centerRefresh(ctx context.Context, ctrl centerController) (*miss.PTZPosition, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	position, err := ctrl.RefreshPosition(ctx)
	if err != nil {
		return nil, fmt.Errorf("xiaomi ptz: auto-center: position read: %w", err)
	}

	if position == nil || position.Ret != 0 {
		return nil, errors.New("xiaomi ptz: auto-center: missing or unsuccessful position response")
	}

	if position.Angle < 0 ||
		position.Angle > 101 ||
		position.Elevation < 0 ||
		position.Elevation > 101 {
		return nil, errors.New("xiaomi ptz: auto-center: position outside supported range")
	}

	return position, nil
}

func centerWait(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func centerAbs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
