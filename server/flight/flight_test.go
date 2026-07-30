package flight

import (
	"math"
	"testing"

	"asteroidsalvage/physics"
)

// gameDefaults mirrors sim.DefaultTuning as this package sees it: kp is the old angular
// damping, the rate setpoint is the old torque over that damping, and the integral and
// derivative terms are off.
func gameDefaults() Params {
	return Params{
		KP:                      900,
		MaxRate:                 1400.0 / 900.0,
		TorqueScale:             1400,
		Exponent:                1,
		DerivativeOnMeasurement: true,
	}
}

func near(t *testing.T, what string, got, want, tol float32) {
	t.Helper()
	if float32(math.Abs(float64(got-want))) > tol {
		t.Errorf("%s = %v, want %v (±%v)", what, got, want, tol)
	}
}

// The whole justification for routing ship rotation through this library rather than
// leaving the hand-rolled controller alone is that at ki = kd = 0 the two are the same
// arithmetic: `T·u − c·ω` is `c·(T/c·u − ω)`. If that stops being true the swap silently
// retuned the game, so it is pinned here rather than only argued in a comment.
func TestPOnlyLoopReproducesTheOpenLoopController(t *testing.T) {
	const (
		torque  = float32(1400) // old ship.torque
		damping = float32(900)  // old ship.angular_damping
	)
	p := gameDefaults()

	cases := []struct {
		name  string
		input float32
		omega float32
	}{
		{"full input from rest", 1, 0},
		{"full input while already turning", 1, 0.5},
		{"no input, coasting", 0, 0.8},
		{"input against the spin", -1, 1.0},
		{"half input", 0.5, 0.2},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var ctl Controller

			// What the old controller would have produced, about the yaw axis.
			want := torque*c.input - damping*c.omega

			got := ctl.Update(&p, 0, c.input, 0,
				physics.Vec3{Z: c.omega}, 1.0/30.0)

			near(t, "yaw torque", got.Z, want, 0.01)
			near(t, "pitch torque", got.X, 0, 1e-6)
			near(t, "roll torque", got.Y, 0, 1e-6)
		})
	}
}

// Crossing the library's (pitch, yaw, roll) with the game's (+X right, +Y forward, +Z up)
// is the one mistake this wrapper exists to prevent, and it is invisible in a diff: yaw
// and roll would simply trade places, and the ship would barrel-roll when you turned.
func TestEachInputAxisTorquesOnlyItsOwnBodyAxis(t *testing.T) {
	p := gameDefaults()

	t.Run("yaw turns about up (+Z)", func(t *testing.T) {
		var ctl Controller
		got := ctl.Update(&p, 0, 1, 0, physics.Vec3{}, 1.0/30.0)
		near(t, "Z", got.Z, 1400, 0.01)
		near(t, "X", got.X, 0, 1e-6)
		near(t, "Y", got.Y, 0, 1e-6)
	})

	t.Run("pitch turns about right (+X)", func(t *testing.T) {
		var ctl Controller
		got := ctl.Update(&p, 1, 0, 0, physics.Vec3{}, 1.0/30.0)
		near(t, "X", got.X, 1400, 0.01)
		near(t, "Y", got.Y, 0, 1e-6)
		near(t, "Z", got.Z, 0, 1e-6)
	})

	t.Run("roll turns about the nose (+Y)", func(t *testing.T) {
		var ctl Controller
		got := ctl.Update(&p, 0, 0, 1, physics.Vec3{}, 1.0/30.0)
		near(t, "Y", got.Y, 1400, 0.01)
		near(t, "X", got.X, 0, 1e-6)
		near(t, "Z", got.Z, 0, 1e-6)
	})

	// And the measured rate has to come back in on the matching channel. A rate about the
	// nose must oppose a roll command, not a yaw one — this is the half of the mapping
	// the three cases above cannot see, because they all pass a zero rate.
	t.Run("measured rate opposes its own axis", func(t *testing.T) {
		var ctl Controller
		got := ctl.Update(&p, 0, 0, 0, physics.Vec3{Y: 1}, 1.0/30.0)
		near(t, "Y", got.Y, -900, 0.01)
		near(t, "X", got.X, 0, 1e-6)
		near(t, "Z", got.Z, 0, 1e-6)
	})
}

// A deadzone has to be a fraction of stick travel, and the remaining travel has to be
// remapped to reach full rate — a deadzone that just clipped the low end would also cap
// the top rate at (1 − deadzone).
func TestDeadzoneEatsDriftWithoutCappingFullDeflection(t *testing.T) {
	p := gameDefaults()
	p.Deadzone = 0.1

	var ctl Controller
	if got := ctl.Update(&p, 0, 0.05, 0, physics.Vec3{}, 1.0/30.0); got.Z != 0 {
		t.Errorf("input inside the deadzone produced torque %v, want 0", got.Z)
	}

	ctl = Controller{}
	got := ctl.Update(&p, 0, 1, 0, physics.Vec3{}, 1.0/30.0)
	near(t, "full deflection torque with a deadzone", got.Z, 1400, 0.01)
}

func TestExponentSoftensSmallInputsOnly(t *testing.T) {
	p := gameDefaults()
	p.Exponent = 2

	var linear, curved Controller
	lin := gameDefaults()

	small := float32(0.5)
	gotLin := linear.Update(&lin, 0, small, 0, physics.Vec3{}, 1.0/30.0).Z
	gotCur := curved.Update(&p, 0, small, 0, physics.Vec3{}, 1.0/30.0).Z

	if gotCur >= gotLin {
		t.Errorf("exponent 2 gave %v at half input, want less than linear's %v",
			gotCur, gotLin)
	}

	// Full deflection must still reach full rate, or the curve is really a sensitivity cut.
	curved = Controller{}
	near(t, "full deflection under exponent 2",
		curved.Update(&p, 0, 1, 0, physics.Vec3{}, 1.0/30.0).Z, 1400, 0.01)
}

// The integrator is the reason a Controller is per-ship state rather than a pure function,
// and Reset is what keeps it from outliving the life it was wound up during.
func TestResetDropsTheIntegrator(t *testing.T) {
	p := gameDefaults()
	p.KI = 500

	var ctl Controller
	// Hold an unreachable rate so error, and therefore the integral, accumulates.
	for i := 0; i < 30; i++ {
		ctl.Update(&p, 0, 1, 0, physics.Vec3{}, 1.0/30.0)
	}

	// With the setpoint now zero and the ship at rest, only the integrator can speak.
	wound := ctl.Update(&p, 0, 0, 0, physics.Vec3{}, 1.0/30.0).Z
	if wound <= 1 {
		t.Fatalf("integral term contributed %v after a second of held input; "+
			"the test cannot show a reset it cannot see", wound)
	}

	ctl.Reset()
	if got := ctl.Update(&p, 0, 0, 0, physics.Vec3{}, 1.0/30.0).Z; got != 0 {
		t.Errorf("torque %v after Reset with no input and no rotation, want 0", got)
	}
}

// Anti-windup: the integrator is clamped so that ki·integral cannot run away past the
// torque the ship can actually produce, however long it is held against a rate it will
// never reach.
func TestIntegratorIsClamped(t *testing.T) {
	p := gameDefaults()
	p.KI = 500

	var ctl Controller
	// Ten seconds of full input against a ship pinned at zero rotation.
	for i := 0; i < 300; i++ {
		ctl.Update(&p, 0, 1, 0, physics.Vec3{}, 1.0/30.0)
	}

	// P contributes kp·MaxRate = 1400; the integral is limited to TorqueScale/ki, so its
	// contribution cannot exceed TorqueScale. Anything past the sum means no clamp.
	got := ctl.Update(&p, 0, 1, 0, physics.Vec3{}, 1.0/30.0).Z
	if got > 1400+p.TorqueScale+1 {
		t.Errorf("torque wound up to %v, past the %v the clamp should allow",
			got, 1400+p.TorqueScale)
	}
}

// Derivative-on-measurement is the difference between a mouse being usable and not: the
// setpoint jumps every frame, and a derivative taken on error would turn each jump into a
// torque spike. With the ship at rest, a jumping setpoint must produce no D contribution
// at all.
func TestDerivativeIgnoresSetpointJumps(t *testing.T) {
	p := gameDefaults()
	p.KD = 200

	var ctl Controller
	ctl.Update(&p, 0, 0, 0, physics.Vec3{}, 1.0/30.0)

	// Setpoint slams from 0 to full while the ship has not moved.
	got := ctl.Update(&p, 0, 1, 0, physics.Vec3{}, 1.0/30.0).Z
	near(t, "torque on a setpoint jump", got, 1400, 0.01) // P only, no D kick

	// Whereas the ship actually starting to turn *must* be damped.
	damped := ctl.Update(&p, 0, 1, 0, physics.Vec3{Z: 0.1}, 1.0/30.0).Z
	if damped >= got {
		t.Errorf("torque %v once the ship began turning, want less than %v — "+
			"the derivative is not damping measured rotation", damped, got)
	}
}
