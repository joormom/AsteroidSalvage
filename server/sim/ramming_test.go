package sim

import (
	"testing"

	"asteroidsalvage/physics"
)

// ramInto puts `a` on a collision course with `b` at the given closing speed and steps
// until they touch (or the window runs out).
func ramInto(s *Sim, a physics.EntityID, b physics.EntityID, speed float32, standoff float32) {
	tgt, _ := s.world.GetBody(b)
	o := s.objects[b]

	// Line them up on the Y axis with `a` below, closing upward.
	start := tgt.Pos.Sub(physics.Vec3{Y: o.Radius + standoff})
	s.world.SetPosition(a, start)
	s.world.SetVelocity(a, physics.Vec3{Y: speed})
	s.stepN(1)

	for i := 0; i < 240; i++ {
		s.stepN(1)
		if pl := s.playerByShip(a); pl != nil && pl.Dead() {
			return
		}
		// Keep driving: damping would otherwise bleed the closing speed away before
		// contact and the collision would land under the ram threshold.
		s.world.SetVelocity(a, physics.Vec3{Y: speed})
	}
}

// coastInto launches a ship at a target and lets it fly, without re-driving the velocity
// every tick. That distinction matters for collision damage: holding thrust against a
// surface is a sustained contact, while a player flying into a rock hits it once and
// bounces. ramInto models the former, this the latter.
func coastInto(s *Sim, a physics.EntityID, b physics.EntityID, speed float32,
	standoff float32) {

	tgt, _ := s.world.GetBody(b)
	o := s.objects[b]

	start := tgt.Pos.Sub(physics.Vec3{Y: o.Radius + standoff})
	s.world.SetPosition(a, start)
	s.world.SetVelocity(a, physics.Vec3{Y: speed})

	for i := 0; i < 120; i++ {
		s.stepN(1)
		if pl := s.playerByShip(a); pl != nil && pl.Dead() {
			return
		}
	}
}

// Two ships meeting at speed must both die. A rammer who walked away would make closing
// to tractor range suicidal against anyone with a boost tank.
func TestRammingAnEnemyShipKillsBoth(t *testing.T) {
	s := newBareSim(t, nil)
	rammer := s.AddPlayer("red", 0)
	victim := s.AddPlayer("blue", 1)

	// Park the victim still, well clear of anything else.
	s.world.SetPosition(victim.Ship, physics.Vec3{X: 0, Y: 0, Z: 400})
	s.world.SetVelocity(victim.Ship, physics.Vec3{})
	s.stepN(1)

	ramInto(s, rammer.Ship, victim.Ship, RamSpeedThreshold*2, 30)

	if !rammer.Dead() {
		t.Error("the ship that did the ramming survived it")
	}
	if !victim.Dead() {
		t.Error("the ship that was rammed survived it")
	}
}

// A gentle bump is not a ram, or jockeying for the same rock would be a bloodbath.
func TestSlowContactIsNotARam(t *testing.T) {
	s := newBareSim(t, nil)
	drifter := s.AddPlayer("red", 0)
	other := s.AddPlayer("blue", 1)

	s.world.SetPosition(other.Ship, physics.Vec3{X: 0, Y: 0, Z: 400})
	s.world.SetVelocity(other.Ship, physics.Vec3{})
	s.stepN(1)

	ramInto(s, drifter.Ship, other.Ship, RamSpeedThreshold*0.4, 12)

	if drifter.Dead() || other.Dead() {
		t.Errorf("a %.1f m/s nudge destroyed a ship (threshold is %.1f)",
			RamSpeedThreshold*0.4, float32(RamSpeedThreshold))
	}
}

// Teammates must be able to fly close without deleting each other.
func TestRammingATeammateIsHarmless(t *testing.T) {
	s := newBareSim(t, nil)
	a := s.AddPlayer("one", 0)
	b := s.AddPlayer("two", 0)
	if a.Team != b.Team {
		t.Fatalf("test needs both on one team, got %d and %d", a.Team, b.Team)
	}

	s.world.SetPosition(b.Ship, physics.Vec3{X: 0, Y: 0, Z: 400})
	s.world.SetVelocity(b.Ship, physics.Vec3{})
	s.stepN(1)

	ramInto(s, a.Ship, b.Ship, RamSpeedThreshold*2, 30)

	if a.Dead() || b.Dead() {
		t.Error("ramming a teammate killed somebody")
	}
}

// Flying into an enemy station trades your ship for a fixed bite out of its hull.
func TestRammingAStationDamagesItAndKillsTheRammer(t *testing.T) {
	s := newBareSim(t, nil)
	rammer := s.AddPlayer("red", 0)

	station, _ := s.MothershipFor(1)
	before := s.objects[station].Health

	ramInto(s, rammer.Ship, station, RamSpeedThreshold*2, 40)

	got := s.objects[station].Health
	if got != before-RamMothershipDamage {
		t.Errorf("station on %v after a ram, want %v", got, before-RamMothershipDamage)
	}
	if !rammer.Dead() {
		t.Error("the rammer survived flying into a station")
	}
}

// Your own station is somewhere you dock, not something you can wreck by arriving badly.
func TestRammingYourOwnStationIsHarmless(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	station, _ := s.MothershipFor(0)
	before := s.objects[station].Health

	ramInto(s, p.Ship, station, RamSpeedThreshold*2, 40)

	if got := s.objects[station].Health; got != before {
		t.Errorf("own station took %v damage from a docking bump", before-got)
	}
}

/*============================================================================
 * Flying into things
 *
 * Distinct from ramming: rocks and scenery are not decided by a threshold, they scale.
 * Every m/s over HullImpactThreshold costs a hull point, so a scrape is survivable and a
 * flat-out collision is not.
 *===========================================================================*/

func TestFlyingIntoARockDamagesTheHullByImpactSpeed(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	s.world.SetPosition(p.Ship, physics.Vec3{X: 0, Y: 0, Z: 400})
	s.world.SetVelocity(p.Ship, physics.Vec3{})
	s.stepN(1)

	// A big, heavy rock so it does not simply get shoved aside.
	rock := s.addRock(physics.Vec3{X: 0, Y: 120, Z: 400}, 4000, 12, 100)
	s.world.SetVelocity(rock, physics.Vec3{})
	s.stepN(1)

	const speed = 24.0 // 12 over the threshold, so about 12 damage of a 30-point hull
	full := s.objects[p.Ship].Health
	coastInto(s, p.Ship, rock, speed, 20)

	got := full - s.objects[p.Ship].Health
	if got <= 0 {
		t.Fatalf("a %.0f m/s collision with a rock did no damage", speed)
	}
	if p.Dead() {
		t.Fatalf("a %.0f m/s collision destroyed the ship; it should be survivable", speed)
	}
	// Generous bounds: the exact closing speed at contact depends on damping and on how
	// far the rock gives. The point is that it scales, not that it is to the decimal.
	if got < 4 || got > 26 {
		t.Errorf("took %.1f damage at %.0f m/s, want something proportionate", got, speed)
	}
}

func TestDriftingIntoARockIsHarmless(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	s.world.SetPosition(p.Ship, physics.Vec3{X: 0, Y: 0, Z: 400})
	s.world.SetVelocity(p.Ship, physics.Vec3{})
	s.stepN(1)

	rock := s.addRock(physics.Vec3{X: 0, Y: 120, Z: 400}, 4000, 12, 100)
	s.world.SetVelocity(rock, physics.Vec3{})
	s.stepN(1)

	full := s.objects[p.Ship].Health
	coastInto(s, p.Ship, rock, HullImpactThreshold*0.5, 12)

	if got := full - s.objects[p.Ship].Health; got != 0 {
		t.Errorf("nudging a rock at half the threshold cost %.1f hull", got)
	}
}

// Full speed into anything solid is death — the ceiling the scale was chosen to put there.
func TestFullSpeedIntoARockIsFatal(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	s.world.SetPosition(p.Ship, physics.Vec3{X: 0, Y: 0, Z: 400})
	s.world.SetVelocity(p.Ship, physics.Vec3{})
	s.stepN(1)

	rock := s.addRock(physics.Vec3{X: 0, Y: 300, Z: 400}, 8000, 16, 100)
	s.world.SetVelocity(rock, physics.Vec3{})
	s.stepN(1)

	// Comfortably past ShipMaxHealth/HullImpactScale + HullImpactThreshold.
	coastInto(s, p.Ship, rock, 60, 40)

	if !p.Dead() {
		t.Errorf("a 60 m/s collision left the pilot alive on %.1f health",
			s.objects[p.Ship].Health)
	}
}

// The shield is a shield: it eats collision damage too, not only weapon fire.
func TestTheShieldAbsorbsCollisionDamage(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)
	p.Effects.Shield = ShieldPool

	full := s.objects[p.Ship].Health
	s.damageHull(p, 20)

	if got := s.objects[p.Ship].Health; got != full {
		t.Errorf("hull dropped to %.1f through a full shield", got)
	}
	if p.Effects.Shield != ShieldPool-20 {
		t.Errorf("shield is on %.1f, want %.1f", p.Effects.Shield, ShieldPool-20)
	}
}
