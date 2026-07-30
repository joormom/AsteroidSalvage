package sim

import (
	"math"
	"testing"

	"asteroidsalvage/physics"
)

// newBareSim builds a sim with the asteroid field cleared, so tests control exactly
// what is in the world. New() spawns a wave by design; that would otherwise add noise.
func newBareSim(t *testing.T, mutate func(*Config)) *Sim {
	t.Helper()
	cfg := DefaultConfig()
	cfg.AutoRestock = false // a cleared field must stay cleared
	// No scenery: mechanics tests place bodies at exact coordinates, and a 190 m
	// derelict generated into one of them is not a useful surprise.
	cfg.PropCount = 0
	// Sandbox: one endless round with no warmup, clock or intermissions, so mechanics
	// tests are not fighting the tournament state machine.
	cfg.Match.DisableMatchFlow = true
	if mutate != nil {
		mutate(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)
	s.clearField()
	return s
}

// clearField removes every asteroid, leaving the mothership and any ships.
func (s *Sim) clearField() {
	for e, o := range s.objects {
		if o.Kind == KindAsteroid || o.Kind == KindSalvage {
			s.world.Despawn(e)
			delete(s.objects, e)
		}
	}
}

// homeOf is where a player's own team parks. Since every team has its own mothership on
// a ring, no test may assume the origin — positions have to be expressed relative to the
// home the player under test actually delivers to.
func (s *Sim) homeOf(p *Player) physics.Vec3 {
	e, ok := s.motherships[p.Team]
	if !ok {
		return physics.Vec3{}
	}
	b, _ := s.world.GetBody(e)
	return b.Pos
}

// addRock places a salvage body at an exact position.
func (s *Sim) addRock(pos physics.Vec3, mass, radius, value float32) physics.EntityID {
	e := s.world.SpawnSphere(mass, radius, pos)
	s.objects[e] = &Object{
		Entity: e, Kind: KindAsteroid, Radius: radius, Value: value, Integrity: 1,
		RequiredBeams: 1, Team: NoTeam,
		Health: HealthFor(TierIron, radius), MaxHealth: HealthFor(TierIron, radius),
	}
	s.world.SetSleepAllowed(e, false)
	return e
}

// addMassive places a rock that one beam cannot move.
func (s *Sim) addMassive(pos physics.Vec3, radius float32) physics.EntityID {
	e := s.world.SpawnSphere(MassFor(TierMassive, radius), radius, pos)
	s.objects[e] = &Object{
		Entity: e, Kind: KindAsteroid, Tier: TierMassive, Radius: radius,
		Value: ValueFor(TierMassive, radius), Integrity: 1, RequiredBeams: 2,
	}
	s.world.SetSleepAllowed(e, false)
	return e
}

// stepN advances the sim, holding input steady.
func (s *Sim) stepN(n int) {
	for i := 0; i < n; i++ {
		s.Step()
	}
}

func TestAddPlayerAutoBalancesTeams(t *testing.T) {
	s := newBareSim(t, nil) // 4 teams of 4

	counts := map[uint8]int{}
	for i := 0; i < 16; i++ {
		p := s.AddPlayer("p", 0xFF)
		counts[p.Team]++
	}

	if len(counts) != 4 {
		t.Fatalf("used %d teams, want 4", len(counts))
	}
	for id, n := range counts {
		if n != 4 {
			t.Errorf("team %d has %d players, want 4", id, n)
		}
	}
}

func TestCoopPutsEveryoneOnOneTeam(t *testing.T) {
	s := newBareSim(t, func(c *Config) { c.Coop = true })

	for i := 0; i < 9; i++ {
		p := s.AddPlayer("p", 0xFF)
		if p.Team != 0 {
			t.Fatalf("co-op player %d got team %d, want 0", i, p.Team)
		}
	}
	if len(s.teams) != 1 {
		t.Errorf("co-op has %d teams, want 1", len(s.teams))
	}
}

func TestThrustMovesShip(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	start, _ := s.world.GetBody(p.Ship)
	s.SetInput(p.ID, Input{Seq: 1, ThrustFwd: 1})
	s.stepN(30)
	end, _ := s.world.GetBody(p.Ship)

	if end.Pos.DistTo(start.Pos) < 1 {
		t.Errorf("ship moved only %v m under full thrust for 1s", end.Pos.DistTo(start.Pos))
	}
}

// Handling is governed by the rotational time constant, not by the raw torque and
// damping numbers. With damping capped at 10 the constant was 35 seconds: the ship wound
// up slowly under a nudge and then would not stop, which reads to a player as "way too
// sensitive". This pins both ends — it must reach a usable turn rate, and it must stop
// promptly when input ceases.
func TestShipTurnsAtAUsableRateAndSettles(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	// Hold full yaw for a second and measure the sustained turn rate.
	s.SetInput(p.ID, Input{Seq: 1, Yaw: 1})
	s.stepN(TickHz)

	b, _ := s.world.GetBody(p.Ship)
	rate := b.AngVel.Len()
	t.Logf("sustained turn rate under full input: %.2f rad/s (%.0f deg/s)",
		rate, rate*180/math.Pi)

	// Below ~0.5 rad/s lining up on a rock is a chore; above ~4 it is uncontrollable.
	if rate < 0.5 {
		t.Errorf("turn rate %.2f rad/s is too sluggish to aim with", rate)
	}
	if rate > 4.0 {
		t.Errorf("turn rate %.2f rad/s is uncontrollably fast", rate)
	}

	// Release, and it must actually stop rather than coast on.
	s.SetInput(p.ID, Input{Seq: 2})
	s.stepN(TickHz / 2)

	b, _ = s.world.GetBody(p.Ship)
	residual := b.AngVel.Len()
	t.Logf("residual spin 0.5 s after release: %.3f rad/s", residual)

	if residual > rate*0.15 {
		t.Errorf("still turning at %.2f rad/s half a second after release (was %.2f) — "+
			"the ship does not settle", residual, rate)
	}
}

// Rotation runs through package flight, which maps the library's (pitch, yaw, roll) onto
// this game's +X right / +Y forward / +Z up body frame. That mapping is invisible in a
// diff and survives every test above — a yaw/roll swap still turns the ship, just about
// the wrong axis — so it is pinned here through the real physics path.
//
// A freshly spawned ship has no rotation applied, so its body frame is the world frame and
// the expected axis is world-space.
func TestEachRotationInputTurnsAboutItsOwnAxis(t *testing.T) {
	axes := []struct {
		name  string
		input Input
		// axis picks the component of AngVel that should carry the rotation.
		axis func(physics.Vec3) float32
		rest func(physics.Vec3) (float32, float32)
	}{
		{"yaw about up", Input{Seq: 1, Yaw: 1},
			func(v physics.Vec3) float32 { return v.Z },
			func(v physics.Vec3) (float32, float32) { return v.X, v.Y }},
		{"pitch about right", Input{Seq: 1, Pitch: 1},
			func(v physics.Vec3) float32 { return v.X },
			func(v physics.Vec3) (float32, float32) { return v.Y, v.Z }},
		{"roll about the nose", Input{Seq: 1, Roll: 1},
			func(v physics.Vec3) float32 { return v.Y },
			func(v physics.Vec3) (float32, float32) { return v.X, v.Z }},
	}

	for _, a := range axes {
		t.Run(a.name, func(t *testing.T) {
			s := newBareSim(t, nil)
			p := s.AddPlayer("pilot", 0)

			s.SetInput(p.ID, a.input)
			s.stepN(TickHz / 2)

			b, _ := s.world.GetBody(p.Ship)
			on := a.axis(b.AngVel)
			off1, off2 := a.rest(b.AngVel)

			if on < 0.5 {
				t.Errorf("only %.3f rad/s about the intended axis", on)
			}
			if math.Abs(float64(off1)) > 0.01 || math.Abs(float64(off2)) > 0.01 {
				t.Errorf("leaked %.3f / %.3f rad/s onto the other two axes — "+
					"the pitch/yaw/roll mapping in package flight is crossed", off1, off2)
			}
		})
	}
}

// ship.rate_kd is virtual rotational inertia: it resists change in turn rate, so the ship
// winds up more gradually. What it must *not* do is change where the ship ends up — at a
// steady rate there is no change to resist, so the top speed of a turn is the P term's
// business alone. Both halves matter, and the second is the one a careless D term breaks.
func TestRateDerivativeSlowsTheWindUpButNotTheTopRate(t *testing.T) {
	spinUp := func(kd float32, ticks int) float32 {
		s := newBareSim(t, nil)
		s.Tuning.SetByID(ParamShipRateKD, kd)
		p := s.AddPlayer("pilot", 0)

		s.SetInput(p.ID, Input{Seq: 1, Yaw: 1})
		s.stepN(ticks)

		b, _ := s.world.GetBody(p.Ship)
		return b.AngVel.Len()
	}

	const kd = 90 // a realistic setting, and inside ship.rate_kd's range

	// Two ticks in, the damped ship must still be behind the undamped one.
	quick, damped := spinUp(0, 2), spinUp(kd, 2)
	t.Logf("after 2 ticks: kd=0 %.3f rad/s, kd=%v %.3f rad/s", quick, kd, damped)
	if damped >= quick {
		t.Errorf("kd=%v reached %.3f rad/s against kd=0's %.3f — "+
			"the derivative is not resisting the wind-up", kd, damped, quick)
	}

	// Given time, both settle on the same rate.
	quickTop, dampedTop := spinUp(0, TickHz*4), spinUp(kd, TickHz*4)
	t.Logf("after 4 s: kd=0 %.3f rad/s, kd=%v %.3f rad/s", quickTop, kd, dampedTop)
	if math.Abs(float64(quickTop-dampedTop)) > 0.02 {
		t.Errorf("settled at %.3f rad/s with kd=%v against %.3f with kd=0 — "+
			"kd is moving the top turn rate, which is ship.torque's job",
			dampedTop, kd, quickTop)
	}
}

// A dev-console slider that can destabilise the ship is a trap, and this one can: the D
// term is divided by dt, so at 30 Hz a large kd makes the discrete rate loop oscillate and
// diverge rather than damp. ship.rate_kd's maximum is picked to sit below that threshold
// for the ship's moment of inertia — see the derivation in tuning.go — and the threshold
// moves if ShipMass, ShipRadius or TickHz ever change.
//
// So this walks the whole published range instead of trusting the arithmetic.
func TestRateDerivativeIsStableAcrossItsWholeRange(t *testing.T) {
	def, ok := paramByID[ParamShipRateKD]
	if !ok {
		t.Fatal("ship.rate_kd is not in paramDefs")
	}

	// Full input for four seconds is long enough for divergence to be unmistakable, and
	// the settled rate is bounded by ship.torque/ship.angular_damping ≈ 1.56 rad/s.
	for _, kd := range []float32{def.Min, def.Max / 4, def.Max / 2, def.Max} {
		s := newBareSim(t, nil)
		s.Tuning.SetByID(ParamShipRateKD, kd)
		p := s.AddPlayer("pilot", 0)

		s.SetInput(p.ID, Input{Seq: 1, Yaw: 1})
		s.stepN(TickHz * 4)

		b, _ := s.world.GetBody(p.Ship)
		rate := b.AngVel.Len()
		t.Logf("kd=%-5v settled at %.3f rad/s", kd, rate)

		if math.IsNaN(float64(rate)) || rate > 4.0 {
			t.Errorf("kd=%v diverged to %v rad/s — the rate loop is unstable inside "+
				"ship.rate_kd's own range (max %v)", kd, rate, def.Max)
		}
	}
}

func TestStaleInputIsIgnored(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	s.SetInput(p.ID, Input{Seq: 10, ThrustFwd: 1})
	s.SetInput(p.ID, Input{Seq: 4, ThrustFwd: -1}) // reordered, must be dropped

	if s.players[p.ID].input.ThrustFwd != 1 {
		t.Errorf("stale input was applied: ThrustFwd = %v, want 1",
			s.players[p.ID].input.ThrustFwd)
	}
}

// Grab must attach when the target is inside the forward cone and in reach.
func TestGrabAttachesToRockInFront(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	ship, _ := s.world.GetBody(p.Ship)
	fwd := ship.Rot.Forward()
	rock := s.addRock(ship.Pos.Add(fwd.Scale(5)), 20, 2, 100)

	s.SetInput(p.ID, Input{Seq: 1, Grab: true})
	s.stepN(2)

	if p.Held != rock {
		t.Fatalf("Held = %d, want %d", p.Held, rock)
	}
	if !s.objects[rock].HeldByPlayer(p.ID) {
		t.Errorf("rock holders = %v, want to include %d", s.objects[rock].Holders, p.ID)
	}
}

// A rock behind the ship must not be grabbable — the cone is what makes aiming matter.
func TestGrabIgnoresRockBehindShip(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	ship, _ := s.world.GetBody(p.Ship)
	fwd := ship.Rot.Forward()
	s.addRock(ship.Pos.Add(fwd.Scale(-5)), 20, 2, 100)

	s.SetInput(p.ID, Input{Seq: 1, Grab: true})
	s.stepN(2)

	if p.Held != 0 {
		t.Errorf("grabbed a rock behind the ship (Held = %d)", p.Held)
	}
}

// Holding grab while flying toward a rock must pick it up on arrival. Requiring a fresh
// button press meant a player who held the button on approach could never grab anything
// — and 16 load-test bots holding grab for 60 seconds managed zero pickups.
func TestGrabWorksWhileButtonAlreadyHeld(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	ship, _ := s.world.GetBody(p.Ship)
	fwd := ship.Rot.Forward()

	// Hold grab with nothing in reach for a while, so the "already pressed" state is
	// firmly established.
	s.SetInput(p.ID, Input{Seq: 1, Grab: true})
	s.stepN(20)
	if p.Held != 0 {
		t.Fatal("grabbed something that should have been out of reach")
	}

	// Now a rock appears in front, button still held.
	rock := s.addRock(ship.Pos.Add(fwd.Scale(5)), 20, 2, 100)
	s.stepN(3)

	if p.Held != rock {
		t.Errorf("Held = %d, want %d — holding the grab button prevented acquisition",
			p.Held, rock)
	}
}

// Losing cargo must lock re-acquisition briefly, or the break condition costs nothing
// now that acquisition no longer needs a fresh press.
func TestBreakFreeImposesCooldown(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	ship, _ := s.world.GetBody(p.Ship)
	rock := s.addRock(ship.Pos.Add(ship.Rot.Forward().Scale(5)), 20, 2, 100)

	s.SetInput(p.ID, Input{Seq: 1, Grab: true})
	s.stepN(2)
	if p.Held != rock {
		t.Fatal("failed to grab")
	}

	// Force a break directly rather than flying, so the test is about the cooldown.
	s.release(p, EventDropped)
	p.grabCooldown = breakCooldownTicks

	s.stepN(2)
	if p.Held != 0 {
		t.Error("re-grabbed during the cooldown")
	}

	s.stepN(breakCooldownTicks + 2)
	if p.Held != rock {
		t.Errorf("Held = %d after the cooldown expired, want %d", p.Held, rock)
	}
}

// The beam must reach across its (short) range: nose up to a rock and it locks on.
func TestTractorBeamGrabsAtRange(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	ship, _ := s.world.GetBody(p.Ship)
	fwd := ship.Rot.Forward()

	// Near the far edge of grab.range (15 m).
	far := s.addRock(ship.Pos.Add(fwd.Scale(13)), 30, 2.5, 100)

	s.SetInput(p.ID, Input{Seq: 1, Grab: true})
	s.stepN(3)

	if p.Held != far {
		t.Fatalf("Held = %d, want %d — the beam did not reach 13 m", p.Held, far)
	}
}

// Beyond grab.range it must not lock on, or the beam would have no range limit at all.
func TestTractorBeamRespectsMaxRange(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	ship, _ := s.world.GetBody(p.Ship)
	s.addRock(ship.Pos.Add(ship.Rot.Forward().Scale(45)), 30, 2, 100)

	s.SetInput(p.ID, Input{Seq: 1, Grab: true})
	s.stepN(3)

	if p.Held != 0 {
		t.Errorf("grabbed a rock 45 m away with grab.range at 15")
	}
}

// Selection is by aim, not proximity: a nearer rock inside the cone must not steal the
// lock from the one lined up on the crosshair.
func TestTractorBeamPicksWhatYouAimAt(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	ship, _ := s.world.GetBody(p.Ship)
	fwd := ship.Rot.Forward()
	right := ship.Rot.Right()

	// Nearer and inside the cone, but off the crosshair (~27 degrees off-axis).
	s.addRock(ship.Pos.Add(fwd.Scale(5)).Add(right.Scale(2.5)), 30, 1.5, 100)
	// Further, but dead ahead.
	aimed := s.addRock(ship.Pos.Add(fwd.Scale(13)), 30, 1.5, 100)

	s.SetInput(p.ID, Input{Seq: 1, Grab: true})
	s.stepN(3)

	if p.Held != aimed {
		t.Errorf("Held = %d, want %d — the beam grabbed a closer rock instead of the "+
			"one being aimed at", p.Held, aimed)
	}
}

// Reeling cargo in from range must not wreck it. The spring-damper was underdamped with
// a flat damping coefficient, so a long-range grab slammed the rock into the hull and
// deliveries banked zero credits.
func TestReelInDoesNotDestroyCargo(t *testing.T) {
	for _, radius := range []float32{1.5, 3.0, 5.5} {
		mass := radius * radius * radius * 0.9

		s := newBareSim(t, nil)
		p := s.AddPlayer("pilot", 0)

		ship, _ := s.world.GetBody(p.Ship)
		rock := s.addRock(
			ship.Pos.Add(ship.Rot.Forward().Scale(14)), mass, radius, 500,
		)

		s.SetInput(p.ID, Input{Seq: 1, Grab: true})
		s.stepN(150) // 5 s: long enough to reel all the way in and settle

		o := s.objects[rock]
		if o == nil {
			t.Fatalf("radius %.1f: rock vanished", radius)
		}
		t.Logf("radius %.1f (%.0f kg): integrity %.2f after reel-in", radius, mass, o.Integrity)

		if o.Integrity < 0.8 {
			t.Errorf("radius %.1f: reeling in cost %.0f%% of the cargo's value — "+
				"the tractor beam is slamming it into the hull",
				radius, (1-o.Integrity)*100)
		}
	}
}

// A lock at the edge of range must survive being reeled in. The break check is written
// against the hold distance, so without the slack allowance a grab made further out
// than the hold point would tear off on the very next tick.
func TestEdgeOfRangeGrabSurvivesReelIn(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	ship, _ := s.world.GetBody(p.Ship)
	rock := s.addRock(ship.Pos.Add(ship.Rot.Forward().Scale(14)), 25, 2, 100)

	s.SetInput(p.ID, Input{Seq: 1, Grab: true})
	s.stepN(3)
	if p.Held != rock {
		t.Fatal("failed to lock on at the edge of range")
	}

	// Hold station and let the beam settle it into the hold point.
	s.stepN(90)

	if p.Held != rock {
		t.Fatalf("lost the rock during reel-in (Held = %d)", p.Held)
	}

	shipNow, _ := s.world.GetBody(p.Ship)
	rockNow, _ := s.world.GetBody(rock)
	gap := rockNow.Pos.DistTo(shipNow.Pos)
	// Hold point is GrabHoldDistance + both radii = 6 + 2 + 2 = 10 m here.
	if gap > 16 {
		t.Errorf("rock settled %.1f m away, expected it drawn in to about 10 m", gap)
	}
}

// A massive asteroid must resist a lone pilot and yield to a crew. This is the whole
// point of the tier — it is what makes teammates matter.
func TestMassiveAsteroidNeedsTwoBeams(t *testing.T) {
	// Measures how far the beam manages to reel the rock in from the edge of range
	// toward the hold point. Measuring raw displacement instead is misleading: a rock
	// already sitting near the hold point barely moves however strong the beam is.
	const startRange = 21.0 // grab.range 15 + the rock's 7 m radius allows this
	reeledIn := func(pilots, beamStrength int) float32 {
		s := newBareSim(t, nil)

		var ps []*Player
		for i := 0; i < pilots; i++ {
			p := s.AddPlayer("pilot", 0)
			p.BeamStrength = beamStrength
			ps = append(ps, p)
		}

		lead, _ := s.world.GetBody(ps[0].Ship)
		rock := s.addMassive(lead.Pos.Add(lead.Rot.Forward().Scale(startRange)), 7)

		// Stack the extra pilots beside the lead so the rock sits in every forward cone.
		for i, p := range ps {
			if i > 0 {
				s.world.SetPosition(p.Ship, lead.Pos.Add(lead.Rot.Right().Scale(float32(i)*5)))
			}
		}
		s.stepN(1)
		for _, p := range ps {
			s.SetInput(p.ID, Input{Seq: 1, Grab: true})
		}
		s.stepN(3)

		if !s.objects[rock].IsHeld() {
			t.Fatalf("%d pilot(s): never locked on", pilots)
		}

		s.stepN(120)

		shipNow, _ := s.world.GetBody(ps[0].Ship)
		rockNow, _ := s.world.GetBody(rock)
		// How much closer it got. Bigger = the beam is winning.
		return startRange - rockNow.Pos.DistTo(shipNow.Pos)
	}

	solo := reeledIn(1, 1)
	crew := reeledIn(2, 1)
	upgraded := reeledIn(1, 2)

	t.Logf("massive rock reeled in by: solo %.2f m, two pilots %.2f m, upgraded solo %.2f m",
		solo, crew, upgraded)

	// A lone stock beam must not move it at all. Space is frictionless, so anything
	// above zero would eventually haul it home given enough patience.
	if solo > 0.25 {
		t.Errorf("a single stock beam reeled the massive rock in %.2f m — it should be "+
			"immovable alone", solo)
	}
	if crew < 1.0 {
		t.Errorf("two beams only managed %.2f m; a crew should be able to move it", crew)
	}
	if upgraded < 1.0 {
		t.Errorf("the tractor upgrade only managed %.2f m; it should match a crew",
			upgraded)
	}
}

// Two players beaming the same rock must both be recorded as holders.
func TestTwoPlayersCanHoldTheSameRock(t *testing.T) {
	s := newBareSim(t, nil)
	a := s.AddPlayer("alpha", 0)
	b := s.AddPlayer("bravo", 0)

	shipA, _ := s.world.GetBody(a.Ship)
	rock := s.addMassive(shipA.Pos.Add(shipA.Rot.Forward().Scale(10)), 7)

	// Put bravo next to alpha so the rock is in both forward cones.
	s.world.SetPosition(b.Ship, shipA.Pos.Add(shipA.Rot.Right().Scale(5)))
	s.stepN(1)

	s.SetInput(a.ID, Input{Seq: 1, Grab: true})
	s.SetInput(b.ID, Input{Seq: 1, Grab: true})
	s.stepN(3)

	o := s.objects[rock]
	if len(o.Holders) != 2 {
		t.Errorf("Holders = %v, want both players", o.Holders)
	}
	if s.beamStrengthOn(o) != 2 {
		t.Errorf("beam strength = %d, want 2", s.beamStrengthOn(o))
	}
}

// Tier balance has to give each class a reason to exist.
//
// Note that value-per-kg falls with radius for any fixed tier (value scales with r^2,
// mass with r^3), so "bigger tier = better per kg" is not achievable and is not the
// design. What matters is: payout per rock climbs with tier, so a better rock is always
// worth the detour; and crystal is the standout per kilogram, which is what makes the
// small blue ones worth hunting when your ship is already loaded.
func TestTierBalanceGivesEachTierAPurpose(t *testing.T) {
	mid := func(tier Tier) float32 {
		spec := tier.Spec()
		return (spec.MinRadius + spec.MaxRadius) / 2
	}
	perRock := func(tier Tier) float32 { return ValueFor(tier, mid(tier)) }
	perKg := func(tier Tier) float32 {
		r := mid(tier)
		return ValueFor(tier, r) / MassFor(tier, r)
	}

	order := []Tier{TierRubble, TierIron, TierCrystal, TierGold, TierMassive}
	for _, tr := range order {
		t.Logf("%-8s %7.0f per rock, %6.2f per kg, %6.0f kg",
			tr.Spec().Name, perRock(tr), perKg(tr), MassFor(tr, mid(tr)))
	}

	// Payout per rock must climb strictly, so a rarer rock is always the better target.
	for i := 1; i < len(order); i++ {
		lo, hi := order[i-1], order[i]
		if perRock(hi) <= perRock(lo) {
			t.Errorf("%s pays %.0f per rock, not more than %s at %.0f",
				hi.Spec().Name, perRock(hi), lo.Spec().Name, perRock(lo))
		}
	}

	// Crystal is the efficiency play — best value per kilogram of anything in the belt.
	for _, other := range []Tier{TierRubble, TierIron, TierGold, TierMassive} {
		if perKg(TierCrystal) <= perKg(other) {
			t.Errorf("crystal (%.2f/kg) should beat %s (%.2f/kg)",
				perKg(TierCrystal), other.Spec().Name, perKg(other))
		}
	}

	// Massive is the jackpot: biggest single payout, which is what justifies the crew.
	for _, other := range []Tier{TierRubble, TierIron, TierCrystal, TierGold} {
		if perRock(TierMassive) <= perRock(other) {
			t.Errorf("massive (%.0f) should out-pay %s (%.0f) per rock",
				perRock(TierMassive), other.Spec().Name, perRock(other))
		}
	}
}

// Cargo must bank when the *ship* reaches the mothership, even if the rock is trailing.
// Requiring the trailing end to cross the ring was why deliveries silently failed.
func TestDepositTriggersOnShipProximity(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("hauler", 1)

	s.world.SetPosition(p.Ship, s.homeOf(p).Add(physics.Vec3{X: 40, Y: 0, Z: 0}))
	s.world.SetVelocity(p.Ship, physics.Vec3{})
	s.stepN(1)

	ship, _ := s.world.GetBody(p.Ship)
	rock := s.addRock(ship.Pos.Add(ship.Rot.Forward().Scale(10)), 30, 2, 400)

	s.SetInput(p.ID, Input{Seq: 1, Grab: true})
	s.stepN(6)

	if s.teams[1].Score <= 0 {
		t.Errorf("team 1 banked nothing; deposit did not trigger")
	}
	if _, still := s.objects[rock]; still {
		t.Error("deposited rock was not removed")
	}
	if p.Credits <= 0 {
		t.Errorf("player earned no personal credits (%v)", p.Credits)
	}
}

// Flying into a rock must throw both of you apart. lagrange combines restitution with
// fminf, so a single dull surface anywhere flattens every impact against it — this
// catches a material that was left on the default.
func TestShipsBounceOffAsteroids(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	ship, _ := s.world.GetBody(p.Ship)
	fwd := ship.Rot.Forward()
	rock := s.addRock(ship.Pos.Add(fwd.Scale(14)), 40, 3, 100)
	s.world.SetMaterial(rock, ShipRestitution, 0.25)

	// Ram it at 25 m/s with no beam on it.
	s.world.SetVelocity(p.Ship, fwd.Scale(25))
	s.SetInput(p.ID, Input{Seq: 1})
	s.stepN(60)

	sb, _ := s.world.GetBody(p.Ship)
	rb, _ := s.world.GetBody(rock)
	shipSpeed := sb.Vel.Dot(fwd)
	t.Logf("120 kg ship into a 40 kg rock at 25 m/s -> ship %.2f m/s, rock %.2f m/s",
		shipSpeed, rb.Vel.Len())

	// A heavy ship hitting a light rock shoves it aside and carries on — it should NOT
	// rebound, and asserting that it does was simply wrong physics. What must happen is
	// a real exchange of momentum.
	if shipSpeed > 20 {
		t.Errorf("ship barely slowed (%.2f m/s of 25) — it is passing through", shipSpeed)
	}
	if rb.Vel.Len() < 5 {
		t.Errorf("the rock only reached %.2f m/s; a 25 m/s impact should fling it",
			rb.Vel.Len())
	}
}

// Hitting something heavier than you must actually throw you backwards.
func TestShipReboundsOffHeavyAsteroid(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	ship, _ := s.world.GetBody(p.Ship)
	fwd := ship.Rot.Forward()
	// Five times the ship's mass.
	rock := s.addRock(ship.Pos.Add(fwd.Scale(16)), 600, 5, 100)
	s.world.SetMaterial(rock, ShipRestitution, 0.25)

	s.world.SetVelocity(p.Ship, fwd.Scale(25))
	s.SetInput(p.ID, Input{Seq: 1})

	var rebounded bool
	for i := 0; i < 120; i++ {
		s.stepN(1)
		sb, _ := s.world.GetBody(p.Ship)
		if sb.Vel.Dot(fwd) < -1 {
			rebounded = true
			break
		}
	}

	sb, _ := s.world.GetBody(p.Ship)
	t.Logf("after hitting a 600 kg rock: ship %.2f m/s along approach",
		sb.Vel.Dot(fwd))

	if !rebounded {
		t.Errorf("ship did not rebound off a rock five times its mass")
	}
}

// The mothership is a static body, so a ship must bounce off it rather than grind along.
func TestShipsBounceOffMothership(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	// Aim straight at this player's own mothership.
	home := s.homeOf(p)
	start := home.Add(physics.Vec3{X: 60, Y: 0, Z: 0})
	s.world.SetPosition(p.Ship, start)
	inward := physics.Vec3{X: -1}
	s.world.SetVelocity(p.Ship, inward.Scale(30))
	s.SetInput(p.ID, Input{Seq: 1})

	var bounced bool
	for i := 0; i < 120; i++ {
		s.stepN(1)
		sb, _ := s.world.GetBody(p.Ship)
		if sb.Vel.X > 1 { // moving back outward
			bounced = true
			break
		}
	}

	if !bounced {
		sb, _ := s.world.GetBody(p.Ship)
		t.Errorf("ship did not rebound off the mothership (vel %+v, pos %+v)",
			sb.Vel, sb.Pos)
	}
}

// The mothership must be solid: you cannot fly into it, at any speed, from any angle.
// Bouncing off a fast approach is easy; the failure mode that matters is grinding
// slowly against it under continuous thrust until you creep through the shell.
func TestCannotFlyInsideMothership(t *testing.T) {
	// The collider is now a flat cylinder matching the drawn saucer, so the two are the
	// same size in the plane and this can assert against the *hull* rather than against a
	// shrunken stand-in for it.
	//
	// It used to check MothershipColliderRadius — 12.6 m, the largest sphere that fits
	// inside a flattened saucer — because the physics bridge could only resolve
	// sphere-sphere contacts. Flying 17 m inside the drawn rim was the accepted cost of
	// nothing invisible being solid. Both compromises are gone; see MothershipHalfHeight.
	//
	// The approach below is along Y with the ship at the station's own height, so what it
	// meets is the cylinder's rim at MothershipRadius.
	const hull = MothershipRadius
	const minGap = hull + ShipRadius - 1.5

	// Drives with real thrust input, the way a player does. Forcing velocity directly
	// each tick instead would simply overwrite whatever the collision response did and
	// prove nothing about the game.
	for _, boost := range []bool{false, true} {
		s := newBareSim(t, nil)
		p := s.AddPlayer("pilot", 0)

		// Park it on the -Y side of the player's own mothership with default (identity)
		// rotation, so the ship's +Y forward axis points straight at the hull.
		home := s.homeOf(p)
		s.world.SetPosition(p.Ship, home.Add(physics.Vec3{X: 0, Y: -110, Z: 0}))
		s.world.SetVelocity(p.Ship, physics.Vec3{})
		s.stepN(1)

		s.SetInput(p.ID, Input{Seq: 1, ThrustFwd: 1, Boost: boost})

		closest := float32(1e9)
		for i := 0; i < 900; i++ { // 30 s of sustained burn
			s.stepN(1)
			b, _ := s.world.GetBody(p.Ship)
			if d := b.Pos.DistTo(home); d < closest {
				closest = d
			}
		}

		label := "full thrust"
		if boost {
			label = "boosted thrust"
		}
		t.Logf("%s at the mothership: closest %.1f m from centre (collider %.1f m, drawn hull %.0f m)",
			label, closest, float32(hull), float32(MothershipRadius))

		if closest < minGap {
			t.Errorf("%s put the ship %.1f m from the mothership centre — it is "+
				"penetrating the collider", label, closest)
		}
	}
}

// The other half of the station's shape, and the one a previous fix was specifically
// about: there must be nothing solid above the saucer. A sphere collider big enough to make
// the rim solid also put ~17 m of invisible wall over the dome, which you hit while flying
// home and — once impacts did damage by speed — died to.
//
// The cylinder gets both. This flies straight over the top, right across the centre, and
// asserts the crossing happens at all and costs nothing.
func TestCanFlyOverTheMothership(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	home := s.homeOf(p)

	// Clear of the saucer's 12.6 m half-height plus the ship's own radius, but well inside
	// the 30 m the old sphere would have made solid.
	const alt = MothershipHalfHeight + ShipRadius + 4

	start := home.Add(physics.Vec3{X: 0, Y: -70, Z: alt})
	s.world.SetPosition(p.Ship, start)
	s.world.SetVelocity(p.Ship, physics.Vec3{})
	s.stepN(1)

	before := s.objects[p.Ship].Health

	// Nose along +Y, straight over the dome.
	s.SetInput(p.ID, Input{Seq: 1, ThrustFwd: 1})

	crossed := false
	for i := 0; i < 900; i++ {
		s.stepN(1)
		b, _ := s.world.GetBody(p.Ship)
		// Past the far side of the hull, still at altitude: it flew over.
		if b.Pos.Y > home.Y+MothershipRadius {
			crossed = true
			break
		}
	}

	b, _ := s.world.GetBody(p.Ship)
	t.Logf("ended %.1f m from centre at Z offset %.1f", b.Pos.DistTo(home), b.Pos.Z-home.Z)

	if !crossed {
		t.Errorf("could not fly over the station at %.1f m altitude — it stalled at "+
			"%+v. There is something solid above the saucer again", float32(alt), b.Pos)
	}
	if after := s.objects[p.Ship].Health; after < before {
		t.Errorf("flying over the station cost %v health; the hull above the saucer is "+
			"solid again", before-after)
	}
}

func TestReleasingInputDropsRock(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	ship, _ := s.world.GetBody(p.Ship)
	rock := s.addRock(ship.Pos.Add(ship.Rot.Forward().Scale(5)), 20, 2, 100)

	s.SetInput(p.ID, Input{Seq: 1, Grab: true})
	s.stepN(2)
	if p.Held != rock {
		t.Fatal("failed to grab")
	}

	s.SetInput(p.ID, Input{Seq: 2, Grab: false})
	s.stepN(1)

	if p.Held != 0 {
		t.Errorf("still holding after releasing grab")
	}
	if s.objects[rock].IsHeld() {
		t.Errorf("rock still marked as held: holders %v", s.objects[rock].Holders)
	}
}

// Yank hard enough and the object breaks free. Uses a very heavy rock and a weak
// tractor so the hold point runs away from it.
func TestGrabBreaksWhenRockCannotKeepUp(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	s.Tuning.SetByID(ParamGrabSpring, 5)    // very weak pull
	s.Tuning.SetByID(ParamGrabMaxForce, 20) // and a low ceiling
	s.Tuning.SetByID(ParamShipThrust, 5000) // but a very strong ship
	s.Tuning.SetByID(ParamGrabReactionScale, 0)

	// Park well clear of every mothership; the spawn ring is close enough to one that
	// reversing from there flies straight through it, which would stop the ship for
	// reasons that have nothing to do with the break condition under test.
	//
	// Off the belt plane rather than further along it: the motherships ring the origin
	// at z = 0, so any distance out along x eventually lands on one.
	s.world.SetPosition(p.Ship, physics.Vec3{X: 0, Y: 0, Z: 900})
	s.world.SetVelocity(p.Ship, physics.Vec3{})
	s.stepN(1)

	ship, _ := s.world.GetBody(p.Ship)
	rock := s.addRock(ship.Pos.Add(ship.Rot.Forward().Scale(5)), 4000, 2, 100)

	s.SetInput(p.ID, Input{Seq: 1, Grab: true})
	s.stepN(2)
	if p.Held != rock {
		t.Fatal("failed to grab")
	}

	// Retreat hard. Thrusting *forward* would just ram the ship into the rock it is
	// carrying; reversing drags the hold point away from it, which is the condition
	// under test.
	_ = s.DrainEvents()
	s.SetInput(p.ID, Input{Seq: 2, Grab: true, ThrustFwd: -1})
	s.stepN(60)

	// Assert the break *happened*, not that hands stay empty afterwards. With a 70 m
	// beam the player is still pointing at the rock they just lost, so once the
	// cooldown lapses the beam legitimately re-acquires it — which is what a tractor
	// beam should do.
	var dropped bool
	for _, e := range s.DrainEvents() {
		if e.Type == EventDropped && e.Entity == rock {
			dropped = true
		}
	}
	if !dropped {
		t.Errorf("rock never broke free despite the ship outrunning it")
	}
}

// The single most important behaviour in the game: carrying mass must degrade handling
// — noticeably, but not to a standstill. If this fails, hauling either feels weightless
// or feels broken, and no amount of tuning elsewhere will fix it.
//
// The rock here is deliberately the heaviest the spawner actually produces (radius 5.5
// at density 0.9 => ~150 kg against a 120 kg ship). Testing with an unrealistic 600 kg
// rock only proves that 5:1 mass ratios are miserable, which tells us nothing about the
// game as it is actually played.
func TestCarryingHeaviestRockSlowsButDoesNotStopTheShip(t *testing.T) {
	measure := func(withRock bool) float32 {
		cfg := DefaultConfig()
		cfg.AutoRestock = false
		cfg.Match.DisableMatchFlow = true
		s, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		s.clearField()

		p := s.AddPlayer("pilot", 0)
		ship, _ := s.world.GetBody(p.Ship)
		start := ship.Pos

		in := Input{Seq: 1, ThrustFwd: 1}
		if withRock {
			// Heaviest rock the spawner produces: radius 5.5, mass r^3 * 0.9.
			const r = 5.5
			s.addRock(ship.Pos.Add(ship.Rot.Forward().Scale(9)), r*r*r*0.9, r, 100)
			in.Grab = true
		}

		s.SetInput(p.ID, in)
		s.stepN(60)

		end, _ := s.world.GetBody(p.Ship)
		return end.Pos.DistTo(start)
	}

	free := measure(false)
	laden := measure(true)
	ratio := laden / free

	t.Logf("2s of full thrust: unladen %.2f m, hauling the heaviest rock %.2f m (%.0f%%)",
		free, laden, ratio*100)

	if laden >= free {
		t.Errorf("hauling did not slow the ship at all (unladen %.2f m, laden %.2f m) — "+
			"the grab reaction force is not reaching the ship", free, laden)
	}
	// Design guardrail, not physics: below ~15%% the ship is effectively anchored and
	// hauling stops being fun. If this trips, rebalance ShipMass or the rock density
	// rather than weakening the reaction force, which is what sells the weight.
	if ratio < 0.15 {
		t.Errorf("hauling the heaviest rock leaves only %.0f%% of unladen mobility — "+
			"that is an anchor, not cargo", ratio*100)
	}
}

// Hard impacts must cost value. This is the risk/reward at the centre of the game.
func TestImpactReducesIntegrity(t *testing.T) {
	s := newBareSim(t, nil)

	a := s.addRock(physics.Vec3{X: 300, Y: 0, Z: 0}, 50, 2, 500)
	b := s.addRock(physics.Vec3{X: 320, Y: 0, Z: 0}, 50, 2, 500)
	s.world.SetVelocity(a, physics.Vec3{X: 25})
	s.world.SetVelocity(b, physics.Vec3{X: -25})

	s.stepN(45)

	if s.objects[a].Integrity >= 1 {
		t.Errorf("rock A integrity = %v after a 50 m/s head-on, want < 1",
			s.objects[a].Integrity)
	}
	if s.objects[b].Integrity >= 1 {
		t.Errorf("rock B integrity = %v after a 50 m/s head-on, want < 1",
			s.objects[b].Integrity)
	}
}

// Gentle contact must NOT cost value, or careful play would be pointless.
func TestGentleContactDoesNotDamage(t *testing.T) {
	s := newBareSim(t, nil)

	a := s.addRock(physics.Vec3{X: 300, Y: 0, Z: 0}, 50, 2, 500)
	b := s.addRock(physics.Vec3{X: 305, Y: 0, Z: 0}, 50, 2, 500)
	s.world.SetVelocity(a, physics.Vec3{X: 0.5})

	s.stepN(45)

	if s.objects[a].Integrity < 1 {
		t.Errorf("a 0.5 m/s nudge damaged rock A (integrity %v)", s.objects[a].Integrity)
	}
	if s.objects[b].Integrity < 1 {
		t.Errorf("a 0.5 m/s nudge damaged rock B (integrity %v)", s.objects[b].Integrity)
	}
}

// Depositing must credit the hauler's own team, and pay out scaled by integrity.
func TestDepositCreditsHaulersTeam(t *testing.T) {
	s := newBareSim(t, nil)

	p := s.AddPlayer("hauler", 2)
	if p.Team != 2 {
		t.Fatalf("player got team %d, want the requested 2", p.Team)
	}

	// Park the ship just outside the mothership, pointing outward, and put a rock in
	// front of it — inside the deposit volume.
	// Outside the mothership's 30 m body (or collision resolution ejects the ship),
	// but close enough that a rock held 5 m ahead sits inside the 45 m deposit volume.
	s.world.SetPosition(p.Ship, s.homeOf(p).Add(physics.Vec3{X: 40, Y: 0, Z: 0}))
	s.world.SetVelocity(p.Ship, physics.Vec3{})
	s.stepN(1)

	ship, _ := s.world.GetBody(p.Ship)
	rock := s.addRock(ship.Pos.Add(ship.Rot.Forward().Scale(5)), 30, 2, 400)
	s.objects[rock].Integrity = 0.5 // battered on the way home

	s.SetInput(p.ID, Input{Seq: 1, Grab: true})
	s.stepN(5)

	if got := s.teams[2].Score; got != 200 {
		t.Errorf("team 2 score = %v, want 200 (400 value x 0.5 integrity)", got)
	}
	for _, other := range []uint8{0, 1, 3} {
		if s.teams[other].Score != 0 {
			t.Errorf("team %d was credited %v, want 0", other, s.teams[other].Score)
		}
	}
	if p.Held != 0 {
		t.Error("player still holding the rock after deposit")
	}
	if _, still := s.objects[rock]; still {
		t.Error("deposited rock was not removed from the world")
	}
}

func TestDepositEmitsEvent(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("hauler", 0)

	// Outside the mothership's 30 m body (or collision resolution ejects the ship),
	// but close enough that a rock held 5 m ahead sits inside the 45 m deposit volume.
	s.world.SetPosition(p.Ship, s.homeOf(p).Add(physics.Vec3{X: 40, Y: 0, Z: 0}))
	s.stepN(1)
	_ = s.DrainEvents()

	ship, _ := s.world.GetBody(p.Ship)
	s.addRock(ship.Pos.Add(ship.Rot.Forward().Scale(5)), 30, 2, 400)

	s.SetInput(p.ID, Input{Seq: 1, Grab: true})
	s.stepN(5)

	var sawGrab, sawDeposit bool
	for _, e := range s.DrainEvents() {
		switch e.Type {
		case EventGrabbed:
			sawGrab = true
		case EventDeposited:
			sawDeposit = true
			if e.Value != 400 {
				t.Errorf("deposit event value = %v, want 400", e.Value)
			}
		}
	}
	if !sawGrab {
		t.Error("no grabbed event")
	}
	if !sawDeposit {
		t.Error("no deposited event")
	}
}

// Tuning must survive a save/load round trip, or a good feel could not be kept.
func TestTuningRoundTrip(t *testing.T) {
	path := t.TempDir() + "/feel.toml"

	a := DefaultTuning()
	a.SetByID(ParamGrabSpring, 777)
	a.SetByID(ParamGrabReactionScale, 0.42)
	if err := a.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	b := DefaultTuning()
	if err := b.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if b.GrabSpring != 777 {
		t.Errorf("GrabSpring = %v, want 777", b.GrabSpring)
	}
	if b.GrabReactionScale != 0.42 {
		t.Errorf("GrabReactionScale = %v, want 0.42", b.GrabReactionScale)
	}
}

// DebugSet is fed straight from the network and must not trust its input.
func TestTuningClampsAndRejects(t *testing.T) {
	tn := DefaultTuning()

	tn.SetByID(ParamGrabSpring, 1e9)
	if tn.GrabSpring != 2000 {
		t.Errorf("GrabSpring = %v, want clamped to 2000", tn.GrabSpring)
	}

	tn.SetByID(ParamGrabSpring, -50)
	if tn.GrabSpring != 0 {
		t.Errorf("GrabSpring = %v, want clamped to 0", tn.GrabSpring)
	}

	if tn.SetByID(0xBEEF, 1) {
		t.Error("unknown parameter id was accepted")
	}
}

// A missing config file must not be an error — a fresh clone has no config/.
func TestLoadMissingFileIsOK(t *testing.T) {
	tn := DefaultTuning()
	if err := tn.Load(t.TempDir() + "/does-not-exist.toml"); err != nil {
		t.Errorf("Load of missing file returned %v, want nil", err)
	}
}
