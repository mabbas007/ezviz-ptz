package ptz

import (
	"math"
	"sync"
	"testing"
	"time"

	"frigate-onvif-go/internal/config"
	"frigate-onvif-go/internal/sdk"
)

type recordedCall struct {
	direction string
	action    string
	speed     int
	at        time.Time
}

type fakeCommander struct {
	mu    sync.Mutex
	calls []recordedCall
	err   error
}

func (f *fakeCommander) Command(direction, action string, speed int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, recordedCall{
		direction: direction,
		action:    action,
		speed:     speed,
		at:        time.Now(),
	})
	return f.err
}

func (f *fakeCommander) snapshot() []recordedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeCommander) count(direction, action string) int {
	n := 0
	for _, call := range f.snapshot() {
		if call.direction == direction && call.action == action {
			n++
		}
	}
	return n
}

func (f *fakeCommander) waitForCall(t *testing.T, what, direction, action string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f.count(direction, action) > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func testConfig() *config.Config {
	return &config.Config{
		DefaultSpeed:      4,
		PanSecondsPerFov:  0.9,
		TiltSecondsPerFov: 0.7,
		MinMoveSeconds:    0.10,
		MaxMoveSeconds:    4.0,
		ZoomEnabled:       true,
	}
}

func waitIdle(t *testing.T, controller *Controller) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !controller.Moving() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("controller never became idle")
}

func speed(v float64) *float64 { return &v }

func TestContinuousMoveStartsAndStops(t *testing.T) {
	fake := &fakeCommander{}
	controller := NewController(fake, testConfig())

	controller.ContinuousMove(0.5, 0.25, 0, speed(0.5))

	calls := fake.snapshot()
	want := []recordedCall{
		{direction: sdk.DirectionRight, action: "START", speed: 4},
		{direction: sdk.DirectionUp, action: "START", speed: 4},
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %+v, want %+v", calls, want)
	}
	for i := range want {
		if calls[i].direction != want[i].direction ||
			calls[i].action != want[i].action ||
			calls[i].speed != want[i].speed {
			t.Fatalf("calls = %+v, want %+v", calls, want)
		}
	}
	if !controller.PanTiltMoving() || !controller.Moving() {
		t.Fatal("controller reports idle while moving")
	}
	if controller.ZoomMoving() {
		t.Fatal("ZoomMoving() = true for a pan/tilt move")
	}

	// Dropping the tilt velocity stops only that axis.
	controller.ContinuousMove(0.5, 0, 0, speed(0.5))
	if fake.count(sdk.DirectionRight, "START") != 1 {
		t.Fatal("pan was restarted when only tilt changed")
	}
	if fake.count(sdk.DirectionUp, "STOP") != 1 {
		t.Fatal("tilt was not stopped when it left the request")
	}

	controller.Stop()
	if controller.Moving() {
		t.Fatal("controller still moving after Stop")
	}
	if fake.count(sdk.DirectionRight, "STOP") != 1 {
		t.Fatal("pan was not stopped by Stop")
	}
}

func TestContinuousMoveReversesAxis(t *testing.T) {
	fake := &fakeCommander{}
	controller := NewController(fake, testConfig())

	controller.ContinuousMove(0.5, 0, 0, speed(0.5))
	controller.ContinuousMove(-0.5, 0, 0, speed(0.5))

	calls := fake.snapshot()
	want := []struct {
		direction string
		action    string
	}{
		{sdk.DirectionRight, "START"},
		{sdk.DirectionRight, "STOP"},
		{sdk.DirectionLeft, "START"},
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %+v, want %+v", calls, want)
	}
	for i := range want {
		if calls[i].direction != want[i].direction || calls[i].action != want[i].action {
			t.Fatalf("calls = %+v, want %+v", calls, want)
		}
	}
	controller.Stop()
}

func TestContinuousMoveStopAll(t *testing.T) {
	fake := &fakeCommander{}
	controller := NewController(fake, testConfig())

	controller.ContinuousMove(0.5, 0.5, 0.5, speed(1))
	controller.ContinuousMove(0, 0, 0, nil)

	if controller.Moving() {
		t.Fatal("controller still moving after a zero velocity")
	}
	for _, direction := range []string{
		sdk.DirectionRight, sdk.DirectionUp, sdk.DirectionZoomIn,
	} {
		if fake.count(direction, "STOP") != 1 {
			t.Fatalf("%s was not stopped exactly once", direction)
		}
	}
}

func TestRelativeMoveTimesAndReportsMoving(t *testing.T) {
	fake := &fakeCommander{}
	controller := NewController(fake, testConfig())

	start := time.Now()
	controller.RelativeMove(0.5, 0, 0, nil)
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("RelativeMove blocked for %v, want an immediate return", elapsed)
	}
	if !controller.PanTiltMoving() {
		t.Fatal("PanTiltMoving() = false right after RelativeMove")
	}

	fake.waitForCall(t, "the pan start", sdk.DirectionRight, "START")
	waitIdle(t, controller)

	elapsed := time.Since(start)
	if elapsed < 400*time.Millisecond {
		t.Fatalf("relative move took %v, want at least 0.45s", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("relative move took %v, want about 0.45s", elapsed)
	}

	calls := fake.snapshot()
	want := []struct {
		direction string
		action    string
	}{
		{sdk.DirectionRight, "START"},
		{sdk.DirectionRight, "STOP"},
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %+v, want %+v", calls, want)
	}
	for i := range want {
		if calls[i].direction != want[i].direction || calls[i].action != want[i].action {
			t.Fatalf("calls = %+v, want %+v", calls, want)
		}
	}
}

func TestRelativeMoveRunsAxesSequentially(t *testing.T) {
	fake := &fakeCommander{}
	controller := NewController(fake, testConfig())

	controller.RelativeMove(0.5, 0.5, 0, nil)
	waitIdle(t, controller)

	calls := fake.snapshot()
	want := []struct {
		direction string
		action    string
	}{
		{sdk.DirectionRight, "START"},
		{sdk.DirectionRight, "STOP"},
		{sdk.DirectionUp, "START"},
		{sdk.DirectionUp, "STOP"},
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %+v, want %+v", calls, want)
	}
	for i := range want {
		if calls[i].direction != want[i].direction || calls[i].action != want[i].action {
			t.Fatalf("calls = %+v, want pan then tilt in order", calls)
		}
	}
}

func TestStopCancelsTimedMove(t *testing.T) {
	fake := &fakeCommander{}
	controller := NewController(fake, testConfig())

	controller.RelativeMove(1, 0, 0, nil)
	fake.waitForCall(t, "the pan start", sdk.DirectionRight, "START")

	controller.Stop()
	if controller.Moving() {
		t.Fatal("controller still moving after Stop")
	}

	time.Sleep(150 * time.Millisecond)
	if got := fake.count(sdk.DirectionRight, "STOP"); got != 1 {
		t.Fatalf("right STOP count = %d, want 1 (the timed goroutine must not stop it twice)", got)
	}
}

func TestContinuousMoveTakesOverTimedAxis(t *testing.T) {
	fake := &fakeCommander{}
	controller := NewController(fake, testConfig())

	controller.RelativeMove(1, 0, 0, nil)
	fake.waitForCall(t, "the pan start", sdk.DirectionRight, "START")

	controller.ContinuousMove(-0.5, 0, 0, speed(0.5))

	fake.waitForCall(t, "the continuous left start", sdk.DirectionLeft, "START")
	if got := fake.count(sdk.DirectionRight, "STOP"); got != 1 {
		t.Fatalf("right STOP count = %d, want 1", got)
	}
	time.Sleep(150 * time.Millisecond)
	if got := fake.count(sdk.DirectionRight, "STOP"); got != 1 {
		t.Fatalf("right STOP count = %d after the timed move woke up, want 1", got)
	}
	if !controller.PanTiltMoving() {
		t.Fatal("PanTiltMoving() = false during the continuous move")
	}
	controller.Stop()
}

func TestRelativeMoveIgnoresZoomAndSmallTranslations(t *testing.T) {
	fake := &fakeCommander{}
	controller := NewController(fake, testConfig())

	controller.RelativeMove(0, 0, 0.5, nil)
	controller.RelativeMove(0.01, 0.01, 0, nil)
	time.Sleep(50 * time.Millisecond)

	if calls := fake.snapshot(); len(calls) != 0 {
		t.Fatalf("calls = %+v, want none", calls)
	}
	if controller.Moving() {
		t.Fatal("controller reports moving after ignored moves")
	}
}

func TestContinuousZoomHonoursZoomEnabled(t *testing.T) {
	fake := &fakeCommander{}
	cfg := testConfig()
	controller := NewController(fake, cfg)

	controller.ContinuousMove(0, 0, 0.5, speed(0.5))
	if !controller.ZoomMoving() {
		t.Fatal("ZoomMoving() = false after a zoom velocity")
	}
	if controller.PanTiltMoving() {
		t.Fatal("PanTiltMoving() = true for a zoom-only move")
	}
	if fake.count(sdk.DirectionZoomIn, "START") != 1 {
		t.Fatal("zoom was not started")
	}
	controller.Stop()

	fake = &fakeCommander{}
	cfg.ZoomEnabled = false
	controller = NewController(fake, cfg)
	controller.ContinuousMove(0, 0, 0.5, speed(0.5))
	if calls := fake.snapshot(); len(calls) != 0 {
		t.Fatalf("calls = %+v, want none when zoom is disabled", calls)
	}
}

func TestSDKSpeedConversion(t *testing.T) {
	controller := NewController(&fakeCommander{}, testConfig())

	cases := []struct {
		name  string
		value *float64
		want  int
	}{
		{"nil uses the default", nil, 4},
		{"half speed", speed(0.5), 4},
		{"full speed", speed(1), 7},
		{"low speed", speed(0.14), 1},
		{"out of range", speed(2), 7},
		{"negative", speed(-0.5), 4},
		{"zero", speed(0), 4},
		{"NaN", speed(math.NaN()), 4},
		{"+Inf", speed(math.Inf(1)), 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := controller.sdkSpeed(tc.value); got != tc.want {
				t.Fatalf("sdkSpeed(%v) = %d, want %d", tc.value, got, tc.want)
			}
		})
	}
}

func TestMoveDurationClamps(t *testing.T) {
	controller := NewController(&fakeCommander{}, testConfig())

	cases := []struct {
		name     string
		fraction float64
		perFov   float64
		want     time.Duration
	}{
		{"half a field of view", 0.5, 0.9, 450 * time.Millisecond},
		{"clamped to the minimum", 0.05, 0.9, 100 * time.Millisecond},
		{"clamped to the maximum", 1, 10, 4 * time.Second},
		{"zero", 0, 0.9, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := controller.moveDuration(tc.fraction, tc.perFov); got != tc.want {
				t.Fatalf("moveDuration(%v, %v) = %v, want %v",
					tc.fraction, tc.perFov, got, tc.want)
			}
		})
	}
}
