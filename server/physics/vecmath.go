package physics

import "math"

// Vector and quaternion helpers. These live here because this package owns Vec3 and
// Quat; the simulation and netcode both build on them.
//
// Convention (see shared/protocol.md): right-handed, Z-up. Ship-local axes are
// +Y forward, +X right, +Z up.

func V(x, y, z float32) Vec3 { return Vec3{x, y, z} }

func (a Vec3) Add(b Vec3) Vec3 { return Vec3{a.X + b.X, a.Y + b.Y, a.Z + b.Z} }
func (a Vec3) Sub(b Vec3) Vec3 { return Vec3{a.X - b.X, a.Y - b.Y, a.Z - b.Z} }
func (a Vec3) Neg() Vec3       { return Vec3{-a.X, -a.Y, -a.Z} }

func (a Vec3) Scale(s float32) Vec3 { return Vec3{a.X * s, a.Y * s, a.Z * s} }

func (a Vec3) Dot(b Vec3) float32 { return a.X*b.X + a.Y*b.Y + a.Z*b.Z }

func (a Vec3) Cross(b Vec3) Vec3 {
	return Vec3{
		a.Y*b.Z - a.Z*b.Y,
		a.Z*b.X - a.X*b.Z,
		a.X*b.Y - a.Y*b.X,
	}
}

func (a Vec3) LenSq() float32 { return a.Dot(a) }

func (a Vec3) Len() float32 { return float32(math.Sqrt(float64(a.LenSq()))) }

func (a Vec3) Norm() Vec3 {
	l := a.Len()
	if l < 1e-8 {
		return Vec3{}
	}
	return a.Scale(1 / l)
}

// ClampLen shortens a to at most max, preserving direction. Returns the clamped vector
// and whether clamping occurred — the grab constraint uses that flag to decide when the
// player has overloaded the tractor beam and the object should break free.
func (a Vec3) ClampLen(max float32) (Vec3, bool) {
	l := a.Len()
	if l <= max || l < 1e-8 {
		return a, false
	}
	return a.Scale(max / l), true
}

func (a Vec3) DistTo(b Vec3) float32 { return a.Sub(b).Len() }

// QuatIdentity is the zero rotation.
func QuatIdentity() Quat { return Quat{0, 0, 0, 1} }

// Rotate applies the rotation q to vector v (standard q*v*q^-1, expanded).
func (q Quat) Rotate(v Vec3) Vec3 {
	u := Vec3{q.X, q.Y, q.Z}
	s := q.W
	// v' = 2(u·v)u + (s² - u·u)v + 2s(u × v)
	return u.Scale(2 * u.Dot(v)).
		Add(v.Scale(s*s - u.Dot(u))).
		Add(u.Cross(v).Scale(2 * s))
}

// Forward, Right and Up are the ship-local basis vectors in world space.
func (q Quat) Forward() Vec3 { return q.Rotate(Vec3{0, 1, 0}) }
func (q Quat) Right() Vec3   { return q.Rotate(Vec3{1, 0, 0}) }
func (q Quat) Up() Vec3      { return q.Rotate(Vec3{0, 0, 1}) }

// Conjugate is the inverse rotation for a unit quaternion. Rotating a world-space
// direction by it converts that direction into the body's local frame, which is what
// turns "the target is over there" into "yaw left, pitch up".
func (q Quat) Conjugate() Quat { return Quat{-q.X, -q.Y, -q.Z, q.W} }

func (q Quat) Mul(r Quat) Quat {
	return Quat{
		q.W*r.X + q.X*r.W + q.Y*r.Z - q.Z*r.Y,
		q.W*r.Y - q.X*r.Z + q.Y*r.W + q.Z*r.X,
		q.W*r.Z + q.X*r.Y - q.Y*r.X + q.Z*r.W,
		q.W*r.W - q.X*r.X - q.Y*r.Y - q.Z*r.Z,
	}
}

func (q Quat) Norm() Quat {
	l := float32(math.Sqrt(float64(q.X*q.X + q.Y*q.Y + q.Z*q.Z + q.W*q.W)))
	if l < 1e-8 {
		return QuatIdentity()
	}
	return Quat{q.X / l, q.Y / l, q.Z / l, q.W / l}
}

// LookRotation builds the rotation whose forward axis (+Y) points along dir.
//
// Shortest-arc from +Y to the direction, which is all a bolt needs: it has no meaningful
// roll, so any rotation that aims the model down its flight path is correct. Used to give
// synthetic snapshot entities an orientation without inventing a full look-at basis.
func LookRotation(dir Vec3) Quat {
	const eps = 1e-6

	l := dir.Len()
	if l < eps {
		return Quat{W: 1}
	}
	b := dir.Scale(1 / l)
	a := Vec3{0, 1, 0}

	dot := a.Dot(b)
	if dot > 1-eps {
		return Quat{W: 1} // already pointing that way
	}
	if dot < -1+eps {
		// Exactly backwards: any axis perpendicular to +Y does, so pick one.
		return Quat{X: 0, Y: 0, Z: 1, W: 0}
	}

	axis := a.Cross(b)
	q := Quat{X: axis.X, Y: axis.Y, Z: axis.Z, W: 1 + dot}

	n := float32(math.Sqrt(float64(q.X*q.X + q.Y*q.Y + q.Z*q.Z + q.W*q.W)))
	if n < eps {
		return Quat{W: 1}
	}
	return Quat{X: q.X / n, Y: q.Y / n, Z: q.Z / n, W: q.W / n}
}
