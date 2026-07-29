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
