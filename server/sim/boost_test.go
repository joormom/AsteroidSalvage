package sim

import (
	"testing"

	"asteroidsalvage/physics"
)

// Boost has to be a resource, not a permanent state: holding the key must drain the tank
// and eventually stop working.
func TestBoostDrainsAndRefills(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	if p.Boost != p.MaxBoost() {
		t.Fatalf("player joined with %v boost, want a full tank of %v",
			p.Boost, p.MaxBoost())
	}

	// Hold it down for exactly the length of the tank.
	s.SetInput(p.ID, Input{Seq: 1, ThrustFwd: 1, Boost: true})
	s.stepN(int(BoostMaxSeconds * TickHz))

	if p.Boost > 0.01 {
		t.Errorf("tank still has %v after a full burn", p.Boost)
	}
	if p.Boosting() {
		t.Error("engines still running hot on an empty tank")
	}

	// Keep holding. The tank does refill from here — you should not have to remember to
	// let go — but it must stay locked out, or the thruster flickers on for a tick every
	// time a sliver of charge lands.
	for i := 0; i < TickHz; i++ {
		s.stepN(1)
		if p.Boosting() {
			t.Fatalf("boost re-engaged %d ticks after running dry with the key still held",
				i+1)
		}
	}

	// Let go, and it comes back — but only after the forced stall has run out. The stall
	// is the whole point of the tank: without it a tapped key keeps a trickle of charge
	// arriving forever and boost is never actually unavailable.
	s.SetInput(p.ID, Input{Seq: 2, ThrustFwd: 1})
	s.stepN(int((BoostEmptyCooldownSecs + BoostRefillSecs) * TickHz))
	if p.Boost < p.MaxBoost()-0.01 {
		t.Errorf("tank refilled to %v of %v after the full refill window",
			p.Boost, p.MaxBoost())
	}

	// And works again on a fresh press.
	s.SetInput(p.ID, Input{Seq: 3, ThrustFwd: 1, Boost: true})
	s.stepN(2)
	if !p.Boosting() {
		t.Error("boost did not re-engage after releasing and refilling")
	}
}

// Held boost must keep working for its whole duration — the point is that you can hold
// it, not tap it.
func TestBoostSustainsWhileHeld(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	s.SetInput(p.ID, Input{Seq: 1, ThrustFwd: 1, Boost: true})

	// Sample most of the way through the tank; it must be boosting the entire time.
	ticks := int(BoostMaxSeconds*TickHz) - 4
	for i := 0; i < ticks; i++ {
		s.stepN(1)
		if !p.Boosting() {
			t.Fatalf("boost cut out after %.2fs of a %.1fs tank",
				float32(i)/TickHz, float32(BoostMaxSeconds))
		}
	}
}

// The tank has to actually make the ship go faster, or the bar is decoration.
func TestBoostMovesTheShipFaster(t *testing.T) {
	run := func(boost bool) float32 {
		s := newBareSim(t, nil)
		p := s.AddPlayer("pilot", 0)
		s.world.SetPosition(p.Ship, physics.Vec3{X: 0, Y: 0, Z: 900})
		s.world.SetVelocity(p.Ship, physics.Vec3{})
		s.stepN(1)

		start, _ := s.world.GetBody(p.Ship)
		s.SetInput(p.ID, Input{Seq: 1, ThrustFwd: 1, Boost: boost})
		// Two seconds, comfortably inside the tank.
		s.stepN(2 * TickHz)

		end, _ := s.world.GetBody(p.Ship)
		return end.Pos.DistTo(start.Pos)
	}

	plain := run(false)
	boosted := run(true)
	t.Logf("2s of thrust: %.1f m plain, %.1f m boosted", plain, boosted)

	if boosted <= plain*1.2 {
		t.Errorf("boosting covered %.1f m against %.1f m unboosted — the multiplier is "+
			"not reaching the engines", boosted, plain)
	}
}

// A dead player's tank must refill, or a respawn is also a wait for boost.
func TestBoostRefillsWhileDead(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	p.Boost = 0
	p.respawnIn = RespawnTicks
	s.stepN(TickHz)

	if p.Boost <= 0 {
		t.Error("the tank did not refill while waiting to respawn")
	}
}

// Running the tank dry has to stop boost dead for a while.
//
// This is the bug the stall exists to fix: with only a lockout-until-release, a pilot who
// tapped the key kept re-engaging on whatever had trickled in since the last tick, so
// boost was never actually unavailable — it just stuttered. Tapping is simulated here
// exactly as a player would do it.
func TestBoostStallsAfterRunningDry(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	s.SetInput(p.ID, Input{Seq: 1, ThrustFwd: 1, Boost: true})
	s.stepN(int(BoostMaxSeconds*TickHz) + 2)

	if p.Boost > 0.01 {
		t.Fatalf("tank not empty after a full burn: %v", p.Boost)
	}
	if p.BoostCooldown() <= 0 {
		t.Fatal("running the tank dry did not start the stall")
	}

	stallTicks := int(BoostEmptyCooldownSecs*TickHz) - 1
	for i := 0; i < stallTicks; i++ {
		// Hammer the key: down, up, down, up.
		s.SetInput(p.ID, Input{Seq: uint32(2 + i), ThrustFwd: 1, Boost: i%2 == 0})
		s.stepN(1)

		if p.Boosting() {
			t.Fatalf("tapping re-engaged boost %d ticks into a %.0fs stall",
				i+1, float32(BoostEmptyCooldownSecs))
		}
		if p.Boost > 0.01 {
			t.Fatalf("the tank refilled to %v during the stall", p.Boost)
		}
	}
}

// A drop of charge must not be spendable. The floor on starting a burn is the other half
// of the fix: without it the tank refills into scraps that a tapping pilot can spend the
// instant they land.
func TestBoostNeedsARealChargeToRestart(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	// An empty tank whose stall has already been served.
	p.Boost = 0
	p.boostCooldown = 0
	p.boostLocked = false

	// Refill to just short of the floor.
	s.SetInput(p.ID, Input{Seq: 1, ThrustFwd: 1})
	for i := 0; p.Boost < BoostReengageSeconds*0.9; i++ {
		if i > 10*TickHz {
			t.Fatal("the tank never refilled")
		}
		s.stepN(1)
	}

	s.SetInput(p.ID, Input{Seq: 2, ThrustFwd: 1, Boost: true})
	s.stepN(1)
	if p.Boosting() {
		t.Errorf("boost engaged on %v of charge, under the %v floor",
			p.Boost, float32(BoostReengageSeconds))
	}

	// Over the floor, it works again.
	s.SetInput(p.ID, Input{Seq: 3, ThrustFwd: 1})
	for i := 0; p.Boost < BoostReengageSeconds+0.05; i++ {
		if i > 10*TickHz {
			t.Fatal("the tank never reached the floor")
		}
		s.stepN(1)
	}
	s.SetInput(p.ID, Input{Seq: 4, ThrustFwd: 1, Boost: true})
	s.stepN(1)
	if !p.Boosting() {
		t.Errorf("boost refused to start on %v, over the %v floor",
			p.Boost, float32(BoostReengageSeconds))
	}
}
