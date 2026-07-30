// Package flight wraps the ART_OF_FLIGHT C library (vendor/art_of_flight) for the
// server's ship handling.
//
// It provides the *inner* loop of a flight controller: pilot input is read as a desired
// turn rate, and a PID per axis produces the torque that holds it. That is a different
// thing from the open-loop `torque = gain * input` it replaces, in one way that matters —
// the rate a given input asks for no longer depends on the ship's moment of inertia or on
// what else is pushing the ship around, so handling stays the same whether you are empty
// or hauling a rock heavier than you are.
//
// Game meaning — what a ship is, what upgrades it has — stays in package sim. This
// package knows about rates and torques.
//
// Not safe for concurrent use; the server drives it from the single tick goroutine.
package flight

/*
#cgo CFLAGS: -I${SRCDIR}/../../vendor/art_of_flight
#cgo CFLAGS: -std=c11 -O2
#cgo LDFLAGS: -lm

#include "bridge.h"
*/
import "C"

import "asteroidsalvage/physics"

// Params is one tick's gains and input shaping. The caller rebuilds this every tick from
// its own tuning, so a dev-console slider lands on the next tick.
//
// KP is what the ship's responsiveness is really made of: the torque applied per rad/s of
// rate error. KI and KD are zero in this game's defaults — see sim.DefaultTuning for what
// they buy and what they cost.
type Params struct {
	KP, KI, KD float32

	// MaxRate is the turn rate, in rad/s, that full deflection asks for. It is a
	// setpoint, not a limit the physics enforces: a collision can still spin the ship
	// faster, and the loop will then work to bring it back down.
	MaxRate float32

	// TorqueScale bounds the integrator, in N·m — the rule of thumb from
	// art_of_flight.h is max_actuator_output / ki. Ignored when KI is zero.
	TorqueScale float32

	// Deadzone and Exponent shape input before it becomes a rate. Deadzone is a
	// fraction of stick travel; Exponent 1 is linear, higher trades fine control near
	// centre for reach at the edges.
	Deadzone float32
	Exponent float32

	// DerivativeOnMeasurement makes the D term respond to how fast the ship is turning
	// rather than to how fast the setpoint is moving. This game's input is a mouse, so
	// the setpoint jumps every frame and this should be true; derivative-on-error would
	// turn each jump into a torque spike.
	DerivativeOnMeasurement bool
}

// Controller is one ship's rate loop. Its zero value is ready to use, and safe to embed
// by value — there is no allocation behind it and nothing to close.
type Controller struct {
	c C.afl_rate_ctl
}

// Reset drops the integrator and derivative history. Call it whenever the ship stops
// being the same ship in the same situation: spawn, respawn, round start.
func (ctl *Controller) Reset() {
	C.afl_reset(&ctl.c)
}

// Update turns normalised rotation input into a body-frame torque.
//
// inPitch, inYaw and inRoll are the wire protocol's -1..1 axes. bodyRate is the measured
// angular velocity **in the body frame** — the caller must rotate the world-frame value
// from a physics snapshot into it, because a PID integrating per axis only means anything
// if the axes stay attached to the ship. The returned torque is likewise body-frame, and
// has to be rotated back to world before it goes to physics.ApplyForces.
func (ctl *Controller) Update(p *Params,
	inPitch, inYaw, inRoll float32,
	bodyRate physics.Vec3, dt float32) physics.Vec3 {

	ilimit := float32(1)
	if p.KI > 0 {
		ilimit = p.TorqueScale / p.KI
	}

	dom := C.int(0)
	if p.DerivativeOnMeasurement {
		dom = 1
	}

	cp := C.afl_params{
		kp: C.float(p.KP), ki: C.float(p.KI), kd: C.float(p.KD),
		integral_limit: C.float(ilimit),
		max_rate:       C.float(p.MaxRate),
		deadzone:       C.float(p.Deadzone),
		exponent:       C.float(p.Exponent),
		use_dom:        dom,
	}

	var tPitch, tYaw, tRoll C.float

	// The axis mapping, in the one place it exists.
	//
	// art_of_flight numbers its axes (pitch, yaw, roll). This game's body frame is +X
	// right, +Y forward, +Z up (shared/protocol.md), so pitch is rotation about X, yaw
	// about Z and roll about Y — the library's y and z channels are the game's z and y.
	// Swapping these two by accident is not a subtle bug to fly but it is a subtle one to
	// read: the ship would barrel-roll when you moved the mouse sideways.
	C.afl_update(&ctl.c, &cp,
		C.float(inPitch), C.float(inYaw), C.float(inRoll),
		C.float(bodyRate.X), C.float(bodyRate.Z), C.float(bodyRate.Y),
		C.float(dt),
		&tPitch, &tYaw, &tRoll)

	return physics.Vec3{
		X: float32(tPitch),
		Y: float32(tRoll),
		Z: float32(tYaw),
	}
}
