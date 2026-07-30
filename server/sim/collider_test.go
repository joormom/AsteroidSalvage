package sim

import (
	"math"
	"testing"

	"asteroidsalvage/physics"
)

func identity() physics.Quat { return physics.QuatIdentity() }

// The bug this file's guard exists for.
//
// A ray pointing away from a capsule or cylinder has both roots behind its origin. Clamping
// a negative root to zero — which is what "started inside" needs — turns every such case
// into a hit at distance 0, so a laser struck the first bit of scenery *behind* the shooter
// and never left the muzzle. raySphere has always guarded this; the new tests did not.
func TestRayIgnoresGeometryBehindTheOrigin(t *testing.T) {
	shapes := map[string]Collider{
		"capsule":  CapsuleCollider(30, 40, physics.AxisY),
		"cylinder": CylinderCollider(30, 12.6, physics.AxisZ),
		"sphere":   SphereCollider(30),
	}

	for name, c := range shapes {
		t.Run(name, func(t *testing.T) {
			// Sitting 970 m along +Y of the shape and firing further along +Y: away from it.
			origin := physics.Vec3{Y: 970}
			away := physics.Vec3{Y: 1}

			if d, hit := c.Ray(origin, away, physics.Vec3{}, identity(), 0.5); hit {
				t.Errorf("reported a hit at %v on geometry %v m behind the origin",
					d, origin.Y)
			}

			// Sanity: firing back at it must still hit, or the guard has gone too far.
			toward := physics.Vec3{Y: -1}
			if _, hit := c.Ray(origin, toward, physics.Vec3{}, identity(), 0.5); !hit {
				t.Error("firing back toward the shape missed; the guard rejects real hits")
			}
		})
	}
}

// The whole point of giving scenery a tighter collider: a shot must pass where a ship
// passes. These offsets are the ones a sphere of the model's radius used to make solid.
func TestShotsPassWhereTheColliderIsNotSolid(t *testing.T) {
	// A capital wreck at centrepiece scale. 0.40 radius / 0.93 half-height on Y, per the
	// measured model bounds in props.go.
	const R = 400
	wreck := CapsuleCollider(R*0.40, R*0.93, physics.AxisY)

	// Straight across the wreck's waist in X, at a series of heights.
	shoot := func(z float32) (float32, bool) {
		return wreck.Ray(physics.Vec3{X: -1200, Z: z}, physics.Vec3{X: 1},
			physics.Vec3{}, identity(), 0.5)
	}

	// Level with the spine: solid.
	if _, hit := shoot(0); !hit {
		t.Error("a shot straight through the spine missed — the wreck is not solid at all")
	}

	// Above the capsule but well inside the old sphere. This is the invisible wall that was
	// being reported: 0.40*R is 160 m, while the sphere reached 400 m.
	for _, z := range []float32{R * 0.5, R * 0.7, R * 0.95} {
		if _, hit := shoot(z); hit {
			t.Errorf("shot at Z=%.0f still blocked; the collider only reaches %.0f m, so "+
				"this is the invisible area the sphere had", z, R*0.40)
		}
	}
}

// Discs are the other half: a station ruin is wide and thin, and a shot over the top has to
// clear it at a height the ring never reaches.
func TestShotsClearAFlatDisc(t *testing.T) {
	const R = 110
	ruin := CylinderCollider(R*0.96, R*0.25, physics.AxisZ)

	// Through the ring's plane: solid.
	if _, hit := ruin.Ray(physics.Vec3{X: -400}, physics.Vec3{X: 1},
		physics.Vec3{}, identity(), 0.5); !hit {
		t.Error("a shot through the ring plane missed")
	}

	// Over the top, above the disc's thickness but inside the old sphere.
	for _, z := range []float32{R * 0.4, R * 0.8} {
		if _, hit := ruin.Ray(physics.Vec3{X: -400, Z: z}, physics.Vec3{X: 1},
			physics.Vec3{}, identity(), 0.5); hit {
			t.Errorf("shot at Z=%.0f blocked over a disc only %.0f m thick", z, R*0.25)
		}
	}
}

// The collider's axis has to follow the body's rotation, or a tumbling wreck would be solid
// in a direction it does not point.
func TestColliderAxisFollowsBodyRotation(t *testing.T) {
	c := CapsuleCollider(10, 100, physics.AxisY)

	// Unrotated the capsule runs along Y, so a ray along X at Y=80 meets the straight
	// section.
	if _, hit := c.Ray(physics.Vec3{X: -400, Y: 80}, physics.Vec3{X: 1},
		physics.Vec3{}, identity(), 0); !hit {
		t.Fatal("unrotated capsule was not solid 80 m along its own axis")
	}

	// Rotated 90 degrees about Z, the long axis becomes X — so the same ray now runs
	// straight down the length and the point at Y=80 is far outside the 10 m radius.
	s, co := float32(math.Sin(math.Pi/4)), float32(math.Cos(math.Pi/4))
	rot := physics.Quat{X: 0, Y: 0, Z: s, W: co}
	if _, hit := c.Ray(physics.Vec3{X: -400, Y: 80}, physics.Vec3{X: 1},
		physics.Vec3{}, rot, 0); hit {
		t.Error("capsule still solid 80 m off-axis after being rotated onto X — the ray " +
			"test is ignoring the body's rotation")
	}
}

// Props are only as good as the shape chosen per kind, and those choices came from measuring
// the real models. This pins which kinds get a tighter collider so that a change to the
// models cannot silently leave a sphere behind.
func TestPropCollidersMatchTheirModels(t *testing.T) {
	s := newBareSim(t, nil)

	cases := []struct {
		kind  PropKind
		shape physics.Shape
		why   string
	}{
		{PropBattlestation, physics.ShapeSphere, "measures 1.03/1.02/1.00 — it is a ball"},
		{PropCapitalWreck, physics.ShapeCapsule, "measures 0.55/1.33/0.31 — long and thin"},
		{PropPlanetChunk, physics.ShapeSphere, "measures 1.12/1.08/1.10 — it is a ball"},
		{PropStationRuin, physics.ShapeCylinder, "measures 0.96/1.04/0.70 — a disc"},
	}

	for _, c := range cases {
		e, col := s.spawnPropBody(c.kind, 100, physics.Vec3{X: float32(c.kind) * 5000})
		if e == 0 {
			t.Fatalf("%v: spawn failed", c.kind)
		}
		if col.Shape != c.shape {
			t.Errorf("prop kind %v got a %v collider, want %v (%s)",
				c.kind, col.Shape, c.shape, c.why)
		}
		// Whatever the shape, it must not reach further than the model does.
		if col.Radius > 100*1.15 {
			t.Errorf("prop kind %v collider radius %v exceeds the model's own extent",
				c.kind, col.Radius)
		}
	}
}

// The tighter colliders are only a fix if they are actually smaller in the direction that
// was wrong. This states the improvement as a number so a regression is obvious.
func TestWreckColliderIsMuchShorterThanASphere(t *testing.T) {
	s := newBareSim(t, nil)

	const R = 400 // centrepiece scale, where the old error was worst
	_, col := s.spawnPropBody(PropCapitalWreck, R, physics.Vec3{X: 9000})

	// Vertical reach of the capsule versus the sphere it replaced.
	sphereReach := float32(R)
	capsuleReach := col.Radius

	t.Logf("at R=%v: capsule reaches %.0f m in Z, the old sphere reached %.0f m — "+
		"%.0f m of invisible wall removed", R, capsuleReach, sphereReach,
		sphereReach-capsuleReach)

	if capsuleReach > sphereReach*0.5 {
		t.Errorf("capsule still reaches %.0f m of the sphere's %.0f m; the model is only "+
			"%.0f m tall so this is still mostly invisible collision",
			capsuleReach, sphereReach, R*0.31)
	}
}
