package sim

import (
	"math"

	"asteroidsalvage/physics"
)

// An object's collision shape, and the ray tests that have to agree with it.
//
// WHY THIS EXISTS
// ---------------
// Two systems decide whether something is solid, and they have to give the same answer:
//
//   - the physics world, which stops ships (server/physics)
//   - boltSweep, which stops laser rounds (projectiles.go)
//
// While every collider was a sphere of Object.Radius, both could just use that radius and
// they agreed by construction. They no longer can. A sphere around the capital wreck is 69%
// of its radius larger than the model in Z — at centrepiece scale that is 276 m of invisible
// wall you die against — so its collider is now a capsule, and the ray test has to follow it
// or shots stop in empty space instead.
//
// So the shape lives on the Object, set once at spawn beside the physics collider, and the
// ray test dispatches on it. Adding a shape means adding a spawn site, a Collider value and
// a ray test together — which is the point.
type Collider struct {
	Shape physics.Shape

	// Radius is the sphere/capsule/cylinder radius, in metres.
	Radius float32

	// HalfHeight is the half length of a capsule's straight section, or half a cylinder's
	// thickness. Unused by a sphere.
	HalfHeight float32

	// Axis is the body-local axis a capsule or cylinder runs along, matching what was
	// passed to the physics spawn. Getting this out of step with physics is the bug this
	// whole file exists to prevent.
	Axis physics.Axis
}

// SphereCollider is the default: what almost everything in the game is, and what
// Object.Radius alone used to imply.
func SphereCollider(radius float32) Collider {
	return Collider{Shape: physics.ShapeSphere, Radius: radius}
}

// CapsuleCollider is for elongated scenery — a wreck's spine.
func CapsuleCollider(radius, halfHeight float32, axis physics.Axis) Collider {
	return Collider{
		Shape: physics.ShapeCapsule, Radius: radius, HalfHeight: halfHeight, Axis: axis,
	}
}

// CylinderCollider is for discs — a saucer hull, a ring station.
func CylinderCollider(radius, halfHeight float32, axis physics.Axis) Collider {
	return Collider{
		Shape: physics.ShapeCylinder, Radius: radius, HalfHeight: halfHeight, Axis: axis,
	}
}

// localAxis is the collider's long direction in the body's own frame.
func (c Collider) localAxis() physics.Vec3 {
	if c.Axis == physics.AxisZ {
		return physics.Vec3{X: 0, Y: 0, Z: 1}
	}
	return physics.Vec3{X: 0, Y: 1, Z: 0}
}

// Ray returns the distance along dir at which a ray from origin first meets this collider,
// placed at centre with rotation rot and inflated by pad.
//
// pad is the moving object's own radius, so a bolt is swept as a sphere rather than a point
// — the same trick the sphere test always used.
func (c Collider) Ray(origin, dir, centre physics.Vec3, rot physics.Quat, pad float32) (float32, bool) {
	switch c.Shape {
	case physics.ShapeCapsule:
		return rayCapsule(origin, dir, centre, rot.Rotate(c.localAxis()),
			c.HalfHeight, c.Radius+pad)
	case physics.ShapeCylinder:
		return rayCylinder(origin, dir, centre, rot.Rotate(c.localAxis()),
			c.HalfHeight, c.Radius, pad)
	default:
		return raySphere(origin, dir, centre, c.Radius+pad)
	}
}

// rayCapsule intersects a ray with a capsule: the segment centre ± axis*halfHeight, swept
// by radius.
//
// Solved as an infinite cylinder about the segment, then the two end caps, taking the
// nearest hit. The cylinder solution is only accepted where it lands *between* the caps;
// outside that the caps are what the ray actually meets, which is what makes the ends
// round instead of chopped flat.
func rayCapsule(origin, dir, centre, axis physics.Vec3, halfHeight, radius float32) (float32, bool) {
	a := axis.Norm()
	if a.LenSq() < 0.5 { // degenerate axis: it is a sphere
		return raySphere(origin, dir, centre, radius)
	}

	// Work relative to the capsule's centre, splitting everything into the component along
	// the axis and the component perpendicular to it.
	m := origin.Sub(centre)

	md := m.Dot(a)
	dd := dir.Dot(a)

	// Perpendicular components: what the infinite-cylinder test actually operates on.
	mPerp := m.Sub(a.Scale(md))
	dPerp := dir.Sub(a.Scale(dd))

	best := float32(math.MaxFloat32)
	found := false

	qa := dPerp.LenSq()
	// Outside the radius and moving away from the axis means both roots are behind the
	// origin, and a negative root must be rejected rather than clamped to zero — raySphere
	// makes the same check for the same reason. Clamping it instead reported a hit at the
	// muzzle against anything anywhere behind the shooter, which is how a laser came to
	// "strike" a station a kilometre the wrong way down the barrel.
	if qa > 1e-12 {
		qb := mPerp.Dot(dPerp)
		qc := mPerp.LenSq() - radius*radius
		if qc <= 0 || qb <= 0 {
			disc := qb*qb - qa*qc
			if disc >= 0 {
				t := (-qb - float32(math.Sqrt(float64(disc)))) / qa
				if t < 0 {
					t = 0 // genuinely started inside
				}
				// Only valid where the hit is level with the straight section.
				if h := md + t*dd; h >= -halfHeight && h <= halfHeight {
					best, found = t, true
				}
			}
		}
	}

	// End caps. A sphere at each end covers the rounded ends and the case of a ray running
	// parallel to the axis, which the cylinder branch cannot see at all.
	for _, sign := range [2]float32{-1, 1} {
		cap := centre.Add(a.Scale(sign * halfHeight))
		if t, ok := raySphere(origin, dir, cap, radius); ok && t < best {
			best, found = t, true
		}
	}

	if !found {
		return 0, false
	}
	return best, true
}

// rayCylinder intersects a ray with a flat-capped cylinder about centre ± axis*halfHeight.
//
// pad inflates it the way a sphere test inflates its radius. A cylinder has no single
// inflated form — the true offset surface has rounded edges — so the radius grows and the
// caps move out, which over-covers the rim by at most pad. Erring outward is deliberate:
// a bolt that stops a hair early is invisible, while one that sails through solid scenery
// is not.
func rayCylinder(origin, dir, centre, axis physics.Vec3, halfHeight, radius, pad float32) (float32, bool) {
	a := axis.Norm()
	if a.LenSq() < 0.5 {
		return raySphere(origin, dir, centre, radius+pad)
	}

	r := radius + pad
	hh := halfHeight + pad

	m := origin.Sub(centre)
	md := m.Dot(a)
	dd := dir.Dot(a)

	mPerp := m.Sub(a.Scale(md))
	dPerp := dir.Sub(a.Scale(dd))

	best := float32(math.MaxFloat32)
	found := false

	// Side wall, accepted only between the caps. Same guard as rayCapsule: outside the
	// radius and receding from the axis means both roots are behind the origin.
	qa := dPerp.LenSq()
	if qa > 1e-12 {
		qb := mPerp.Dot(dPerp)
		qc := mPerp.LenSq() - r*r
		if qc <= 0 || qb <= 0 {
			disc := qb*qb - qa*qc
			if disc >= 0 {
				t := (-qb - float32(math.Sqrt(float64(disc)))) / qa
				if t < 0 {
					t = 0
				}
				if h := md + t*dd; h >= -hh && h <= hh {
					best, found = t, true
				}
			}
		}
	}

	// Flat caps: the plane crossing, kept only where it lands inside the disc.
	if float32(math.Abs(float64(dd))) > 1e-6 {
		for _, sign := range [2]float32{-1, 1} {
			t := (sign*hh - md) / dd
			if t < 0 {
				continue
			}
			// Distance from the axis at the crossing.
			p := mPerp.Add(dPerp.Scale(t))
			if p.LenSq() <= r*r && t < best {
				best, found = t, true
			}
		}
	}

	// Origin already inside: a bolt spawned within scenery hits immediately.
	if !found {
		if float32(math.Abs(float64(md))) <= hh && mPerp.LenSq() <= r*r {
			return 0, true
		}
		return 0, false
	}
	return best, true
}
