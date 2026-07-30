package physics

import (
	"math"
	"testing"
)

// Collider tests.
//
// These exist because the old failure mode was *silence*: a box collided with nothing at
// all, and a body spawned as one fell through the world without an error, a log line, or a
// visible symptom beyond "the scenery is not solid". Every test here therefore asserts a
// bounce actually happened, not merely that nothing crashed.

// fireSphereAt launches a unit sphere from `from` with velocity `vel` and steps until it
// either reverses along the dominant axis of travel or the step budget runs out. It returns
// the sphere's final state and whether it was deflected.
func fireSphereAt(t *testing.T, w *World, from, vel Vec3, steps int) (BodyState, bool) {
	t.Helper()

	e := w.SpawnSphere(1, 1, from)
	if e == 0 {
		t.Fatal("sphere spawn failed")
	}
	w.SetSleepAllowed(e, false)
	w.SetMaterial(e, 0.9, 0)
	w.SetVelocity(e, vel)

	// Which component carries the motion, so "did it turn around" is a sign check.
	axis := func(v Vec3) float32 { return v.X }
	if math.Abs(float64(vel.Y)) > math.Abs(float64(vel.X)) &&
		math.Abs(float64(vel.Y)) >= math.Abs(float64(vel.Z)) {
		axis = func(v Vec3) float32 { return v.Y }
	} else if math.Abs(float64(vel.Z)) > math.Abs(float64(vel.X)) {
		axis = func(v Vec3) float32 { return v.Z }
	}
	initial := axis(vel)

	for i := 0; i < steps; i++ {
		w.Step()
		b, ok := w.GetBody(e)
		if !ok {
			t.Fatal("sphere vanished mid-flight")
		}
		if !finite(b.Pos) || !finite(b.Vel) {
			t.Fatalf("sphere went non-finite: pos=%+v vel=%+v", b.Pos, b.Vel)
		}
		// Reversed along the axis of travel: it bounced.
		if initial > 0 && axis(b.Vel) < 0 {
			return b, true
		}
		if initial < 0 && axis(b.Vel) > 0 {
			return b, true
		}
	}

	b, _ := w.GetBody(e)
	return b, false
}

// A sphere fired at a static box must bounce. This is the case that was silently broken:
// ag_resolve_collisions skipped every non-sphere collider, so the box was furniture the
// world could not see.
func TestSphereBouncesOffBox(t *testing.T) {
	w := newTestWorld(t, 16)

	box := w.SpawnBox(0, Vec3{5, 5, 5}, Vec3{0, 0, 0}) // static, 10 m cube
	if box == 0 {
		t.Fatal("box spawn failed")
	}
	w.SetMaterial(box, 0.9, 0)

	b, bounced := fireSphereAt(t, w, Vec3{-20, 0, 0}, Vec3{15, 0, 0}, 120)
	if !bounced {
		t.Fatalf("sphere was not deflected by the box; ended at %+v moving %+v "+
			"(it flew straight through)", b.Pos, b.Vel)
	}
	// And it must be turned away on the side it arrived from, not teleported through.
	if b.Pos.X > 0 {
		t.Errorf("sphere ended at X = %v, on the far side of a box centred at 0 — "+
			"it passed through before being pushed back", b.Pos.X)
	}
}

// The contact normal has to push the pair APART. lagrange's own resolver reads the normal
// in the opposite direction to the one sphere-sphere produces, and the symptom of getting
// this wrong is not "no collision" but an attractive impulse: bodies snap together, which
// looks like a grab bug rather than a sign error.
func TestContactPushesBodiesApartNotTogether(t *testing.T) {
	w := newTestWorld(t, 16)

	// Two spheres already overlapping and at rest. The only thing that can move them is
	// penetration correction plus the contact impulse.
	a := w.SpawnSphere(1, 1, Vec3{-0.5, 0, 0})
	b := w.SpawnSphere(1, 1, Vec3{0.5, 0, 0})
	w.SetSleepAllowed(a, false)
	w.SetSleepAllowed(b, false)

	before := float64(1.0) // centres 1 m apart, radii sum 2 m: 1 m of overlap
	for i := 0; i < 30; i++ {
		w.Step()
	}

	ab, _ := w.GetBody(a)
	bb, _ := w.GetBody(b)
	after := math.Abs(float64(bb.Pos.X - ab.Pos.X))

	if after <= before {
		t.Errorf("overlapping spheres went from %.3f m apart to %.3f m — the contact "+
			"normal is inverted and is pulling them together", before, after)
	}
}

// A rotated box must deflect according to its rotation. lagrange's sphere-box routine is
// AABB-only and never reads the rotation, so this is what proves the bridge's
// rotate-into-box-space wrapper is doing its job. Without it, a tumbling derelict would
// collide as though it were axis-aligned.
func TestBoxRotationIsHonoured(t *testing.T) {
	// A long thin slab, 1 m thick in X and 20 m tall in Z. Fired at from +Z, the sphere
	// meets a 1 m x 40 m face when the slab is upright, and misses entirely once the slab
	// is rolled 90 degrees about X so its long axis lies along Y.
	half := Vec3{0.5, 20, 20}

	// Upright: the slab spans Z from -20 to +20, so a sphere coming down +Z hits it.
	up := newTestWorld(t, 16)
	slab := up.SpawnBox(0, half, Vec3{0, 0, 0})
	up.SetMaterial(slab, 0.9, 0)
	_, hitUpright := fireSphereAt(t, up, Vec3{0, 0, 40}, Vec3{0, 0, -15}, 120)
	if !hitUpright {
		t.Fatal("sphere passed through an upright slab that spans its whole path")
	}

	// Now rotate the slab 90 degrees about Y, which turns the 0.5 m X half-extent into a
	// 0.5 m Z half-extent: the slab becomes a thin sheet lying flat, and a sphere fired
	// from far out along X no longer meets 20 m of material.
	rot := newTestWorld(t, 16)
	slab2 := rot.SpawnBox(0, half, Vec3{0, 0, 0})
	rot.SetMaterial(slab2, 0.9, 0)
	// 90 degrees about Y: q = (sin(45) about Y, cos(45)).
	s, c := float32(math.Sin(math.Pi/4)), float32(math.Cos(math.Pi/4))
	rot.SetRotation(slab2, Quat{X: 0, Y: s, Z: 0, W: c})

	// Fired along X through the slab's centre height. Unrotated the slab is only 0.5 m
	// thick in X and would stop the sphere at about X = -1.5; rotated, the 20 m half-extent
	// now lies along X, so the sphere meets it far sooner. The contact position is the
	// observable that tells the two apart.
	e := rot.SpawnSphere(1, 1, Vec3{-40, 0, 0})
	rot.SetSleepAllowed(e, false)
	rot.SetMaterial(e, 0.9, 0)
	rot.SetVelocity(e, Vec3{15, 0, 0})

	var contactX float32 = -1e9
	for i := 0; i < 120; i++ {
		rot.Step()
		b, _ := rot.GetBody(e)
		if b.Vel.X < 0 {
			contactX = b.Pos.X
			break
		}
	}
	if contactX < -1e8 {
		t.Fatal("sphere passed through the rotated slab entirely")
	}
	// Rotated, the slab reaches out to X = -20. An AABB test that ignored the rotation
	// would only stop the sphere near X = -1.5 (0.5 m half-extent + 1 m radius).
	if contactX > -10 {
		t.Errorf("bounced at X = %.2f; a slab rotated to span X from -20 to +20 should "+
			"have been hit far sooner. The box's rotation is being ignored", contactX)
	}
}

// Capsules are the shape for anything long and thin a ship should slide along. lagrange
// takes the axis as a rotated vector, so this is mostly checking the bridge wires the
// half-height and axis through correctly — including that half-height means half.
func TestSphereBouncesOffCapsule(t *testing.T) {
	w := newTestWorld(t, 16)

	// Radius 2, half-height 15: a 34 m spar running along local +Y, which is the axis
	// lagrange uses for capsules (see SpawnCapsule) — not the game's up.
	cap := w.SpawnCapsule(0, 2, 15, AxisY, Vec3{0, 0, 0})
	if cap == 0 {
		t.Fatal("capsule spawn failed")
	}
	w.SetMaterial(cap, 0.9, 0)

	// Fired along X at Y = 10: partway along the straight section, and beyond where the
	// sphere could reach if the half-height had been halved a second time.
	b, bounced := fireSphereAt(t, w, Vec3{-20, 10, 0}, Vec3{15, 0, 0}, 120)
	if !bounced {
		t.Fatalf("sphere was not deflected by the capsule; ended at %+v moving %+v",
			b.Pos, b.Vel)
	}
	if b.Pos.X > 0 {
		t.Errorf("sphere ended at X = %v, past the capsule's axis", b.Pos.X)
	}
}

func TestSphereBouncesOffCylinder(t *testing.T) {
	w := newTestWorld(t, 16)

	cyl := w.SpawnCylinder(0, 4, 10, AxisY, Vec3{0, 0, 0})
	if cyl == 0 {
		t.Fatal("cylinder spawn failed")
	}
	w.SetMaterial(cyl, 0.9, 0)

	b, bounced := fireSphereAt(t, w, Vec3{-25, 0, 0}, Vec3{15, 0, 0}, 120)
	if !bounced {
		t.Fatalf("sphere was not deflected by the cylinder; ended at %+v moving %+v",
			b.Pos, b.Vel)
	}
}

// A plane is the one shape that makes a hard arena boundary: unlike a big sphere, it cannot
// be flown around.
func TestSphereBouncesOffPlane(t *testing.T) {
	w := newTestWorld(t, 16)

	// Solid below Z = 0, normal pointing up.
	wall := w.SpawnPlane(Vec3{0, 0, 1}, 0)
	if wall == 0 {
		t.Fatal("plane spawn failed")
	}
	w.SetMaterial(wall, 0.9, 0)

	b, bounced := fireSphereAt(t, w, Vec3{0, 0, 20}, Vec3{0, 0, -15}, 120)
	if !bounced {
		t.Fatalf("sphere was not deflected by the plane; ended at %+v moving %+v",
			b.Pos, b.Vel)
	}
	if b.Pos.Z < -2 {
		t.Errorf("sphere reached Z = %v, well past a plane at Z = 0", b.Pos.Z)
	}
}

// Sphere-sphere is the pair the whole game already ran on, and it is the one pair whose
// normal lagrange points the other way. Rewriting the narrow phase must not have touched
// it.
func TestSphereSphereStillBounces(t *testing.T) {
	w := newTestWorld(t, 16)

	target := w.SpawnSphere(0, 5, Vec3{0, 0, 0}) // static
	w.SetMaterial(target, 0.9, 0)

	b, bounced := fireSphereAt(t, w, Vec3{-25, 0, 0}, Vec3{15, 0, 0}, 120)
	if !bounced {
		t.Fatalf("sphere-sphere stopped working; ended at %+v moving %+v", b.Pos, b.Vel)
	}
}

// AxisZ exists so a disc-shaped collider can lie in the body's XY plane without rotating
// the body — because the client draws every body at its snapshot rotation, so rotating one
// to aim its collider visibly tips the model over. This is what proves the axis is really
// respected rather than quietly ignored.
func TestColliderAxisSelectsTheLongDirection(t *testing.T) {
	// A tall thin cylinder: radius 2, half-height 30.
	const r, hh = float32(2), float32(30)

	// Along Y, a sphere fired down the Y axis from far away meets an end cap at y ≈ -31,
	// while one fired along X meets the side wall at x ≈ -3.
	y := newTestWorld(t, 16)
	cy := y.SpawnCylinder(0, r, hh, AxisY, Vec3{0, 0, 0})
	y.SetMaterial(cy, 0.9, 0)
	byY, okY := fireSphereAt(t, y, Vec3{0, -60, 0}, Vec3{0, 15, 0}, 200)
	if !okY {
		t.Fatal("AxisY cylinder did not stop a sphere fired along Y")
	}

	// Along Z, the same cylinder is long in Z instead: a sphere fired along Y now meets the
	// side wall close in, not an end cap 30 m out.
	z := newTestWorld(t, 16)
	cz := z.SpawnCylinder(0, r, hh, AxisZ, Vec3{0, 0, 0})
	z.SetMaterial(cz, 0.9, 0)
	bzY, okZ := fireSphereAt(t, z, Vec3{0, -60, 0}, Vec3{0, 15, 0}, 200)
	if !okZ {
		t.Fatal("AxisZ cylinder did not stop a sphere fired along Y")
	}

	t.Logf("sphere fired along Y stopped at y = %.2f (AxisY) vs %.2f (AxisZ)",
		byY.Pos.Y, bzY.Pos.Y)

	// The AxisY cylinder reaches out to y = -30, so it stops the sphere far from the origin.
	// The AxisZ one is only 2 m thick in Y, so the sphere gets much closer before bouncing.
	if !(byY.Pos.Y < bzY.Pos.Y-10) {
		t.Errorf("AxisY stopped the sphere at y = %.2f and AxisZ at y = %.2f; the two "+
			"should differ by roughly the 30 m half-height. The axis is being ignored",
			byY.Pos.Y, bzY.Pos.Y)
	}
}

// A Z-axis body's inertia has to be swizzled to match its geometry, or it collides as a
// long shape while resisting spin as a short one.
func TestAxisZSwizzlesInertia(t *testing.T) {
	spin := func(axis Axis, torque Vec3, read func(Vec3) float32) float64 {
		w := newTestWorld(t, 16)
		e := w.SpawnCapsule(100, 1, 20, axis, Vec3{0, 0, 0})
		w.SetSleepAllowed(e, false)
		w.SetDamping(e, 0, 0)
		for i := 0; i < 30; i++ {
			w.ApplyForces([]ForceCmd{{Entity: e, Torque: torque}})
			w.Step()
		}
		b, _ := w.GetBody(e)
		return math.Abs(float64(read(b.AngVel)))
	}

	// For AxisY the easy spin is about Y; for AxisZ it must move to Z.
	easyY := spin(AxisY, Vec3{0, 500, 0}, func(v Vec3) float32 { return v.Y })
	easyZ := spin(AxisZ, Vec3{0, 0, 500}, func(v Vec3) float32 { return v.Z })
	hardZ := spin(AxisZ, Vec3{0, 500, 0}, func(v Vec3) float32 { return v.Y })

	t.Logf("AxisY spin about Y %.4f | AxisZ spin about Z %.4f | AxisZ about Y %.4f",
		easyY, easyZ, hardZ)

	if math.Abs(easyY-easyZ) > easyY*0.05 {
		t.Errorf("spinning about its own length gives %.4f rad/s on AxisY but %.4f on "+
			"AxisZ; the tensor was not swizzled with the geometry", easyY, easyZ)
	}
	if hardZ >= easyZ {
		t.Errorf("an AxisZ capsule tumbles end-over-end at %.4f rad/s, no slower than the "+
			"%.4f it spins about its own length at — the tensor is still Y-major",
			hardZ, easyZ)
	}
}

// PairSupported must tell the truth about the gaps, because the alternative to an honest
// "no" is a body that quietly falls through the world. If a pair is implemented later,
// this test is the reminder to update it.
func TestPairSupportedMatchesWhatIsImplemented(t *testing.T) {
	supported := [][2]Shape{
		{ShapeSphere, ShapeSphere},
		{ShapeSphere, ShapeBox},
		{ShapeSphere, ShapeCapsule},
		{ShapeSphere, ShapeCylinder},
		{ShapeSphere, ShapePlane},
		{ShapeBox, ShapeBox},
		{ShapeBox, ShapePlane},
		{ShapeCapsule, ShapeCapsule},
	}
	unsupported := [][2]Shape{
		{ShapeBox, ShapeCapsule},
		{ShapeBox, ShapeCylinder},
		{ShapeCapsule, ShapeCylinder},
		{ShapeCylinder, ShapeCylinder},
		{ShapeCylinder, ShapePlane},
		{ShapeCapsule, ShapePlane},
		{ShapePlane, ShapePlane},
	}

	for _, p := range supported {
		if !PairSupported(p[0], p[1]) {
			t.Errorf("%v/%v reported unsupported but is implemented", p[0], p[1])
		}
		if !PairSupported(p[1], p[0]) {
			t.Errorf("%v/%v unsupported in reverse order — order must not matter",
				p[1], p[0])
		}
	}
	for _, p := range unsupported {
		if PairSupported(p[0], p[1]) {
			t.Errorf("%v/%v reported supported, but ag_narrow_phase has no case for it",
				p[0], p[1])
		}
	}
}

// Inertia comes from the collider shape, so a long capsule must resist spinning about its
// own axis differently than end-over-end. Without this, lg_body() leaves the tensor as a
// unit vector and a 200 m derelict spins like a marble.
func TestNonSphereInertiaComesFromTheShape(t *testing.T) {
	w := newTestWorld(t, 16)

	// A long capsule. Its axis is local +Y, so Y is the easy "spin about its own length"
	// direction and X is the hard "tumble end-over-end" one.
	e := w.SpawnCapsule(100, 1, 20, AxisY, Vec3{0, 0, 0})
	w.SetSleepAllowed(e, false)
	w.SetDamping(e, 0, 0)

	const torque = 500
	for i := 0; i < 30; i++ {
		w.ApplyForces([]ForceCmd{{Entity: e, Torque: Vec3{torque, 0, 0}}})
		w.Step()
	}
	tumble, _ := w.GetBody(e)

	w2 := newTestWorld(t, 16)
	e2 := w2.SpawnCapsule(100, 1, 20, AxisY, Vec3{0, 0, 0})
	w2.SetSleepAllowed(e2, false)
	w2.SetDamping(e2, 0, 0)
	for i := 0; i < 30; i++ {
		w2.ApplyForces([]ForceCmd{{Entity: e2, Torque: Vec3{0, torque, 0}}})
		w2.Step()
	}
	spin, _ := w2.GetBody(e2)

	tumbleRate := math.Abs(float64(tumble.AngVel.X))
	spinRate := math.Abs(float64(spin.AngVel.Y))
	t.Logf("same torque: end-over-end %.4f rad/s, about the long axis %.4f rad/s",
		tumbleRate, spinRate)

	if spinRate <= tumbleRate {
		t.Errorf("a 40 m capsule spins about its long axis at %.4f rad/s but tumbles "+
			"end-over-end at %.4f — the inertia tensor is not coming from the shape",
			spinRate, tumbleRate)
	}
}
