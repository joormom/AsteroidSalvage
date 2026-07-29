package physics

import (
	"math"
	"testing"
)

const tickHz = 30
const dt = 1.0 / float32(tickHz)

func newTestWorld(t *testing.T, maxEntities int) *World {
	t.Helper()
	w, err := NewWorld(dt, maxEntities)
	if err != nil {
		t.Fatalf("NewWorld: %v", err)
	}
	t.Cleanup(w.Close)
	return w
}

func finite(v Vec3) bool {
	return !math.IsNaN(float64(v.X)) && !math.IsInf(float64(v.X), 0) &&
		!math.IsNaN(float64(v.Y)) && !math.IsInf(float64(v.Y), 0) &&
		!math.IsNaN(float64(v.Z)) && !math.IsInf(float64(v.Z), 0)
}

func TestWorldLifecycle(t *testing.T) {
	w := newTestWorld(t, 64)
	if got := w.Count(); got != 0 {
		t.Fatalf("new world has %d bodies, want 0", got)
	}
	w.Close()
	w.Close() // must be idempotent
}

// Space has no ambient gravity: an untouched body must not drift. A stray -9.81 would
// quietly ruin the feel of everything built on top of this.
func TestNoAmbientGravity(t *testing.T) {
	w := newTestWorld(t, 16)
	e := w.SpawnSphere(10, 1, Vec3{0, 0, 0})
	if e == 0 {
		t.Fatal("spawn failed")
	}

	for i := 0; i < 300; i++ {
		w.Step()
	}

	b, ok := w.GetBody(e)
	if !ok {
		t.Fatal("body vanished")
	}
	const eps = 1e-4
	if math.Abs(float64(b.Pos.X)) > eps || math.Abs(float64(b.Pos.Y)) > eps || math.Abs(float64(b.Pos.Z)) > eps {
		t.Errorf("body drifted to %+v after 300 idle steps, want origin", b.Pos)
	}
}

// A force sustained across ticks must accelerate the body along that axis.
// lagrange clears accumulated forces every step, so this also verifies that callers
// re-applying each tick is the correct usage.
func TestSustainedForceAccelerates(t *testing.T) {
	w := newTestWorld(t, 16)
	e := w.SpawnSphere(2, 1, Vec3{0, 0, 0})
	w.SetSleepAllowed(e, false)

	cmds := []ForceCmd{{Entity: e, Force: Vec3{0, 100, 0}}}
	for i := 0; i < 30; i++ {
		w.ApplyForces(cmds)
		w.Step()
	}

	b, _ := w.GetBody(e)
	if b.Pos.Y <= 0 {
		t.Errorf("Y = %v after 1s of +Y thrust, want > 0", b.Pos.Y)
	}
	if b.Vel.Y <= 0 {
		t.Errorf("velocity Y = %v, want > 0", b.Vel.Y)
	}
}

// The core of the game's feel: under identical force, a heavier body must accelerate
// less. If this ever fails, hauling will not feel heavy no matter how it is tuned.
func TestHeavierMassAcceleratesLess(t *testing.T) {
	w := newTestWorld(t, 16)
	light := w.SpawnSphere(1, 1, Vec3{0, 0, 0})
	heavy := w.SpawnSphere(50, 1, Vec3{500, 0, 0}) // far apart so they never touch
	w.SetSleepAllowed(light, false)
	w.SetSleepAllowed(heavy, false)

	cmds := []ForceCmd{
		{Entity: light, Force: Vec3{0, 200, 0}},
		{Entity: heavy, Force: Vec3{0, 200, 0}},
	}
	for i := 0; i < 30; i++ {
		w.ApplyForces(cmds)
		w.Step()
	}

	lb, _ := w.GetBody(light)
	hb, _ := w.GetBody(heavy)
	if !(lb.Vel.Y > hb.Vel.Y*5) {
		t.Errorf("light vel %v vs heavy vel %v — expected light to be far faster", lb.Vel.Y, hb.Vel.Y)
	}
}

// ApplyForces batches through a transient hash keyed by entity id; verify every command
// in a batch actually lands, including when the batch is larger than one hash bucket.
func TestApplyForcesBatchHitsEveryEntity(t *testing.T) {
	w := newTestWorld(t, 256)

	const n = 100
	ents := make([]EntityID, n)
	cmds := make([]ForceCmd, n)
	for i := range ents {
		// Spread far apart so collisions cannot contaminate the result.
		ents[i] = w.SpawnSphere(1, 0.5, Vec3{float32(i) * 100, 0, 0})
		w.SetSleepAllowed(ents[i], false)
		cmds[i] = ForceCmd{Entity: ents[i], Force: Vec3{0, 0, 50}}
	}

	for i := 0; i < 10; i++ {
		w.ApplyForces(cmds)
		w.Step()
	}

	for i, e := range ents {
		b, ok := w.GetBody(e)
		if !ok {
			t.Fatalf("entity %d (index %d) missing", e, i)
		}
		if b.Vel.Z <= 0 {
			t.Errorf("entity index %d got no force: vel.Z = %v", i, b.Vel.Z)
		}
	}
}

// Two commands for the same entity must sum, not replace. A ship hauling an asteroid
// gets one command for engine thrust and another for the grab reaction; when these
// overwrote instead of accumulating, grabbing anything silently cut the engines.
func TestApplyForcesAccumulatesDuplicates(t *testing.T) {
	w := newTestWorld(t, 16)
	e := w.SpawnSphere(1, 1, Vec3{0, 0, 0})
	w.SetSleepAllowed(e, false)

	// +100 then -100 on the same entity must cancel to zero net force.
	w.ApplyForces([]ForceCmd{
		{Entity: e, Force: Vec3{0, 100, 0}},
		{Entity: e, Force: Vec3{0, -100, 0}},
	})
	w.Step()

	b, _ := w.GetBody(e)
	if math.Abs(float64(b.Vel.Y)) > 1e-3 {
		t.Errorf("opposing forces did not cancel: vel.Y = %v (one command overwrote the other)", b.Vel.Y)
	}

	// Two forces in the same direction must add.
	w2 := newTestWorld(t, 16)
	single := w2.SpawnSphere(1, 1, Vec3{0, 0, 0})
	double := w2.SpawnSphere(1, 1, Vec3{500, 0, 0})
	w2.SetSleepAllowed(single, false)
	w2.SetSleepAllowed(double, false)

	w2.ApplyForces([]ForceCmd{
		{Entity: single, Force: Vec3{0, 50, 0}},
		{Entity: double, Force: Vec3{0, 50, 0}},
		{Entity: double, Force: Vec3{0, 50, 0}},
	})
	w2.Step()

	sb, _ := w2.GetBody(single)
	db, _ := w2.GetBody(double)
	if db.Vel.Y <= sb.Vel.Y*1.5 {
		t.Errorf("two 50 N commands (%v) did not roughly double one (%v)", db.Vel.Y, sb.Vel.Y)
	}
}

// Damage depends on impact speed, so collisions must be reported with a sane RelSpeed.
func TestCollisionReported(t *testing.T) {
	w := newTestWorld(t, 16)

	a := w.SpawnSphere(1, 1, Vec3{-5, 0, 0})
	b := w.SpawnSphere(1, 1, Vec3{5, 0, 0})
	w.SetSleepAllowed(a, false)
	w.SetSleepAllowed(b, false)

	// Aim them at each other; closing speed 20 m/s.
	w.SetVelocity(a, Vec3{10, 0, 0})
	w.SetVelocity(b, Vec3{-10, 0, 0})

	var seen bool
	var speed float32
	for i := 0; i < 60; i++ {
		w.Step()
		for _, c := range w.DrainCollisions() {
			if (c.A == a && c.B == b) || (c.A == b && c.B == a) {
				seen = true
				speed = c.RelSpeed
			}
		}
		if seen {
			break
		}
	}

	if !seen {
		t.Fatal("head-on collision was never reported")
	}
	// Captured pre-resolution, so it should be close to the 20 m/s closing speed.
	if speed < 10 {
		t.Errorf("RelSpeed = %v, want ~20 (pre-resolution closing speed)", speed)
	}
}

// Entity ids are recycled through a free list; repeated churn must not grow the world.
// This is the closest thing to a leak check available from Go.
func TestSpawnDespawnChurnIsStable(t *testing.T) {
	w := newTestWorld(t, 512)

	base := w.SpawnSphere(1, 1, Vec3{0, 0, 0})
	if base == 0 {
		t.Fatal("spawn failed")
	}

	for cycle := 0; cycle < 200; cycle++ {
		ents := make([]EntityID, 0, 50)
		for i := 0; i < 50; i++ {
			e := w.SpawnSphere(1, 0.5, Vec3{float32(i) * 50, float32(cycle) * 50, 0})
			if e == 0 {
				t.Fatalf("cycle %d: storage exhausted — entity ids are not being recycled", cycle)
			}
			ents = append(ents, e)
		}
		w.Step()
		for _, e := range ents {
			w.Despawn(e)
		}
	}

	if got := w.Count(); got != 1 {
		t.Errorf("after 200 churn cycles Count() = %d, want 1", got)
	}
}

// The Phase 1 gate from the plan: 500 bodies, 600 steps, stable and finite.
func TestPhase1Gate_500Bodies600Steps(t *testing.T) {
	const bodies = 500
	const steps = 600

	w := newTestWorld(t, bodies+16)

	// A loose 3D lattice, spaced so they interact only occasionally.
	ents := make([]EntityID, 0, bodies)
	side := 8
	for i := 0; i < bodies; i++ {
		x := float32((i % side) * 12)
		y := float32(((i / side) % side) * 12)
		z := float32((i / (side * side)) * 12)
		e := w.SpawnSphere(1+float32(i%5), 1.5, Vec3{x, y, z})
		if e == 0 {
			t.Fatalf("spawn %d failed", i)
		}
		ents = append(ents, e)
	}

	if got := w.Count(); got != bodies {
		t.Fatalf("Count() = %d, want %d", got, bodies)
	}

	// Give everything a small initial drift so the sim is doing real work.
	for i, e := range ents {
		w.SetVelocity(e, Vec3{float32(i%7) - 3, float32(i%5) - 2, float32(i%3) - 1})
	}

	for s := 0; s < steps; s++ {
		w.Step()
		_ = w.DrainCollisions()
	}

	if got := w.Count(); got != bodies {
		t.Errorf("after %d steps Count() = %d, want %d", steps, got, bodies)
	}

	snap := w.Snapshot()
	if len(snap) != bodies {
		t.Fatalf("Snapshot returned %d bodies, want %d", len(snap), bodies)
	}
	for _, b := range snap {
		if !finite(b.Pos) || !finite(b.Vel) {
			t.Fatalf("entity %d went non-finite: pos=%+v vel=%+v", b.Entity, b.Pos, b.Vel)
		}
	}

	t.Logf("500 bodies / 600 steps OK; last step %.3f ms", w.LastStepMs())
}

// Measures the cost of a step as body count grows. lg_broad_phase is an O(n^2)
// all-pairs loop (sim.h:711) and lg_resolve_contact does linear-scan lookups, so this
// benchmark is what decides whether libspatial broadphase is needed.
func benchmarkStepN(b *testing.B, n int) {
	w, err := NewWorld(dt, n+16)
	if err != nil {
		b.Fatal(err)
	}
	defer w.Close()

	side := 10
	for i := 0; i < n; i++ {
		x := float32((i % side) * 10)
		y := float32(((i / side) % side) * 10)
		z := float32((i / (side * side)) * 10)
		e := w.SpawnSphere(1, 1.5, Vec3{x, y, z})
		w.SetSleepAllowed(e, false) // worst case: nothing sleeps
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.Step()
		_ = w.Snapshot()
		_ = w.DrainCollisions()
	}
	b.ReportMetric(w.LastStepMs(), "ms/step")
}

func BenchmarkStep100(b *testing.B)  { benchmarkStepN(b, 100) }
func BenchmarkStep250(b *testing.B)  { benchmarkStepN(b, 250) }
func BenchmarkStep500(b *testing.B)  { benchmarkStepN(b, 500) }
func BenchmarkStep1000(b *testing.B) { benchmarkStepN(b, 1000) }
