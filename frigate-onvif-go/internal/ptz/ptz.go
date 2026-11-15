// Package ptz implements the ONVIF PTZ state machine on top of the Hikvision
// SDK daemon.
//
// The camera only understands "start moving in direction X" / "stop", so ONVIF
// relative moves are emulated here: start, wait for the requested fraction of a
// field of view, stop. The controller therefore knows whether the motor is
// moving, which is what Frigate's autotracking needs (moving frames are skipped
// and camera motion is compensated in software).
//
// This is a port of frigate-onvif/app/ptz.py.
package ptz

import (
	"log/slog"
	"math"
	"sync"
	"time"

	"frigate-onvif-go/internal/config"
	"frigate-onvif-go/internal/sdk"
)

// Commander is the subset of sdk.Client the controller needs.
type Commander interface {
	Command(direction, action string, speed int) error
}

// timedMove is the ownership token of one RelativeMove. Directions owned by a
// timed move are not stopped by the goroutine once another command took them
// over.
type timedMove struct {
	done chan struct{}
}

func newTimedMove() *timedMove {
	return &timedMove{done: make(chan struct{})}
}

func (m *timedMove) cancel() {
	select {
	case <-m.done:
	default:
		close(m.done)
	}
}

func (m *timedMove) cancelled() bool {
	select {
	case <-m.done:
		return true
	default:
		return false
	}
}

// wait sleeps for d unless the move is cancelled. It reports whether the full
// duration elapsed.
func (m *timedMove) wait(d time.Duration) bool {
	if d <= 0 {
		return !m.cancelled()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-m.done:
		return false
	}
}

type moveStep struct {
	direction string
	duration  time.Duration
}

// Controller tracks which axes are moving and emulates relative moves.
type Controller struct {
	client Commander
	cfg    *config.Config

	mu sync.Mutex
	// active maps a direction to its owner: nil for a continuous move, or the
	// timed move that owns it.
	active map[string]*timedMove
	// timed is true while a relative (timed) move goroutine is running.
	timed  bool
	cancel *timedMove
}

// NewController returns a controller driving client with cfg's tuning.
func NewController(client Commander, cfg *config.Config) *Controller {
	return &Controller{
		client: client,
		cfg:    cfg,
		active: make(map[string]*timedMove),
	}
}

// PanTiltMoving reports whether a pan/tilt axis is moving.
func (c *Controller) PanTiltMoving() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.timed {
		return true
	}
	for direction := range c.active {
		if !isZoom(direction) {
			return true
		}
	}
	return false
}

// ZoomMoving reports whether a zoom axis is moving.
func (c *Controller) ZoomMoving() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for direction := range c.active {
		if isZoom(direction) {
			return true
		}
	}
	return false
}

// Moving reports whether anything is moving.
func (c *Controller) Moving() bool {
	return c.PanTiltMoving() || c.ZoomMoving()
}

// Stop stops every active movement (ONVIF Stop).
func (c *Controller) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cancelTimedLocked()
	c.stopActiveLocked()
}

// ContinuousMove starts/stops directions so the active set matches the
// request. The zoom velocity is ignored when zoom is disabled.
func (c *Controller) ContinuousMove(pan, tilt, zoom float64, speed *float64) {
	speedSDK := c.sdkSpeed(speed)

	type target struct {
		direction string
		speed     int
	}
	var desired []target

	if math.Abs(pan) >= 0.01 {
		desired = append(desired, target{directionFor(pan, sdk.DirectionRight, sdk.DirectionLeft), speedSDK})
	}
	if math.Abs(tilt) >= 0.01 {
		desired = append(desired, target{directionFor(tilt, sdk.DirectionUp, sdk.DirectionDown), speedSDK})
	}
	if c.cfg.ZoomEnabled && math.Abs(zoom) >= 0.01 {
		desired = append(desired, target{directionFor(zoom, sdk.DirectionZoomIn, sdk.DirectionZoomOut), speedSDK})
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.cancelTimedLocked()

	if len(desired) == 0 {
		c.stopActiveLocked()
		return
	}

	wanted := make(map[string]struct{}, len(desired))
	for _, item := range desired {
		wanted[item.direction] = struct{}{}
	}

	for direction := range c.active {
		if _, ok := wanted[direction]; !ok {
			c.stopLocked(direction)
			delete(c.active, direction)
			continue
		}
		// Take over from a timed move that was just cancelled.
		c.active[direction] = nil
	}

	for _, item := range desired {
		if _, ok := c.active[item.direction]; ok {
			continue
		}
		if err := c.client.Command(item.direction, "START", item.speed); err != nil {
			slog.Warn("ContinuousMove failed", "direction", item.direction, "err", err)
			continue
		}
		c.active[item.direction] = nil
	}
}

// RelativeMove emulates an ONVIF RelativeMove (FOV space) with a timed move.
//
// pan and tilt are in units of the current field of view, matching the
// TranslationSpaceFov the bridge advertises (range -1..1). The move runs in the
// background so the SOAP call returns immediately, exactly like the Python
// bridge.
func (c *Controller) RelativeMove(pan, tilt, zoom float64, speed *float64) {
	if math.Abs(zoom) >= 0.01 {
		slog.Debug("Ignoring relative zoom (only continuous zoom is supported)", "zoom", zoom)
	}

	pan = clamp(pan, -1, 1)
	tilt = clamp(tilt, -1, 1)
	if math.Abs(pan) < 0.02 {
		pan = 0
	}
	if math.Abs(tilt) < 0.02 {
		tilt = 0
	}

	speedSDK := c.sdkSpeed(speed)

	c.mu.Lock()
	c.cancelTimedLocked()
	c.stopActiveLocked()

	if pan == 0 && tilt == 0 {
		c.mu.Unlock()
		return
	}

	move := newTimedMove()
	c.cancel = move
	c.timed = true
	c.mu.Unlock()

	go c.runTimedMove(pan, tilt, speedSDK, move)
}

func (c *Controller) runTimedMove(pan, tilt float64, speedSDK int, move *timedMove) {
	steps := make([]moveStep, 0, 2)
	if pan != 0 {
		steps = append(steps, moveStep{
			direction: directionFor(pan, sdk.DirectionRight, sdk.DirectionLeft),
			duration:  c.moveDuration(math.Abs(pan), c.cfg.PanSecondsPerFov),
		})
	}
	if tilt != 0 {
		steps = append(steps, moveStep{
			direction: directionFor(tilt, sdk.DirectionUp, sdk.DirectionDown),
			duration:  c.moveDuration(math.Abs(tilt), c.cfg.TiltSecondsPerFov),
		})
	}

	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.cancel == move {
			c.cancel = nil
			c.timed = false
		}
	}()

	for _, step := range steps {
		c.mu.Lock()
		if move.cancelled() {
			c.mu.Unlock()
			break
		}
		if err := c.client.Command(step.direction, "START", speedSDK); err != nil {
			slog.Warn("RelativeMove failed", "direction", step.direction, "err", err)
			c.mu.Unlock()
			continue
		}
		c.active[step.direction] = move
		c.mu.Unlock()

		move.wait(step.duration)

		c.mu.Lock()
		// Stop only if this move still owns the direction; a newer command
		// may have taken it over while we were waiting.
		if c.active[step.direction] == move {
			c.stopLocked(step.direction)
			delete(c.active, step.direction)
		}
		c.mu.Unlock()
	}
}

// moveDuration converts a fraction of the field of view to a timed-move
// duration, clamped to the configured minimum and maximum.
func (c *Controller) moveDuration(fractionOfFov, secondsPerFov float64) time.Duration {
	duration := fractionOfFov * secondsPerFov
	if duration <= 0 {
		return 0
	}
	duration = clamp(duration, c.cfg.MinMoveSeconds, c.cfg.MaxMoveSeconds)
	return time.Duration(duration * float64(time.Second))
}

// sdkSpeed converts an ONVIF velocity (-1..1) to the camera's 1..7 speed.
func (c *Controller) sdkSpeed(value *float64) int {
	if value == nil {
		return c.cfg.DefaultSpeed
	}
	speed := math.Abs(*value)
	if math.IsNaN(speed) || math.IsInf(speed, 0) || speed <= 0 {
		return c.cfg.DefaultSpeed
	}
	// ONVIF velocities are normalized -1..1; the camera takes 1..7. Python's
	// round() breaks ties to even, so RoundToEven keeps the mapping identical.
	return int(clamp(math.RoundToEven(speed*7), 1, 7))
}

func (c *Controller) stopLocked(direction string) {
	if err := c.client.Command(direction, "STOP", 0); err != nil {
		slog.Debug("Stop failed", "direction", direction, "err", err)
	}
}

func (c *Controller) stopActiveLocked() {
	for direction := range c.active {
		c.stopLocked(direction)
		delete(c.active, direction)
	}
}

func (c *Controller) cancelTimedLocked() {
	if c.cancel != nil {
		c.cancel.cancel()
		c.cancel = nil
	}
	c.timed = false
}

func isZoom(direction string) bool {
	return direction == sdk.DirectionZoomIn || direction == sdk.DirectionZoomOut
}

func directionFor(value float64, positive, negative string) string {
	if value > 0 {
		return positive
	}
	return negative
}

func clamp(value, low, high float64) float64 {
	return math.Max(low, math.Min(high, value))
}
