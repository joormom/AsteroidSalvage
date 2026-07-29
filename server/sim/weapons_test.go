package sim

import (
	"testing"

	"asteroidsalvage/physics"
)

// aimAt points a ship's body straight at a target position by placing the target on the
// ship's forward axis, which with identity rotation is +Y. Rotating the body instead
// would mean reimplementing look-at maths in a test.
func placeInFrontOf(s *Sim, p *Player, dist float32) physics.Vec3 {
	b, _ := s.world.GetBody(p.Ship)
	return b.Pos.Add(b.Rot.Forward().Scale(dist))
}

// The core of the feature: a laser must damage an enemy ship, and four hits must kill.
func TestLaserDestroysEnemyShip(t *testing.T) {
	s := newBareSim(t, nil)

	shooter := s.AddPlayer("red", 0)
	victim := s.AddPlayer("blue", 1)

	// Park them well clear of any mothership so collision response is not involved,
	// with the victim directly down the shooter's barrel.
	s.world.SetPosition(shooter.Ship, physics.Vec3{X: 800, Y: 0, Z: 0})
	s.world.SetVelocity(shooter.Ship, physics.Vec3{})
	s.stepN(1)
	s.world.SetPosition(victim.Ship, placeInFrontOf(s, shooter, 60))
	s.world.SetVelocity(victim.Ship, physics.Vec3{})
	s.stepN(1)

	target := s.objects[victim.Ship]
	if target.Health != ShipMaxHealth {
		t.Fatalf("victim starts on %v health, want %v", target.Health, ShipMaxHealth)
	}

	// Hold the trigger. A kill takes more hits than one magazine holds, so this has to
	// run long enough to cover the reload as well as the shots — derived from the
	// constants so a rebalance of either does not quietly turn this into a test that
	// stops firing halfway.
	perShot := float32(LaserDamage) // via a variable: the constant ratio is not an integer
	shotsNeeded := int(float32(ShipMaxHealth)/perShot) + 1
	reloadTicks := int(float32(shotsNeeded) / shooter.EnergyRegenPerSecond() * TickHz)

	// Stop the moment it dies rather than running the whole window out. A wreck respawns
	// on full health after four seconds, and a burst long enough to cover the reload is
	// long enough to sail past that — which showed up here as a victim on full health and
	// looked exactly like a laser that does nothing.
	s.SetInput(shooter.ID, Input{Seq: 1, Fire: true})
	for i := 0; i < ShotCooldownTicks*shotsNeeded+reloadTicks && !victim.Dead(); i++ {
		s.stepN(1)
	}

	if !victim.Dead() {
		t.Errorf("victim survived a sustained burst (health %v)", target.Health)
	}
	if victim.RespawnSeconds() <= 0 {
		t.Error("destroyed ship has no respawn timer running")
	}
}

// Friendly fire would make any crowded team launch a bloodbath.
func TestLaserIgnoresTeammates(t *testing.T) {
	s := newBareSim(t, nil)

	shooter := s.AddPlayer("a", 0)
	mate := s.AddPlayer("b", 0)
	if mate.Team != shooter.Team {
		t.Fatalf("test setup: players landed on teams %d and %d", shooter.Team, mate.Team)
	}

	s.world.SetPosition(shooter.Ship, physics.Vec3{X: 800, Y: 0, Z: 0})
	s.stepN(1)
	s.world.SetPosition(mate.Ship, placeInFrontOf(s, shooter, 60))
	s.world.SetVelocity(mate.Ship, physics.Vec3{})
	s.stepN(1)

	s.SetInput(shooter.ID, Input{Seq: 1, Fire: true})
	s.stepN(ShotCooldownTicks * 8)

	if got := s.objects[mate.Ship].Health; got != ShipMaxHealth {
		t.Errorf("teammate took %v damage from friendly fire", ShipMaxHealth-got)
	}
}

// The energy bar is the whole balance lever: a burst, then a wait.
func TestEnergyLimitsBurstThenRefills(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)
	s.world.SetPosition(p.Ship, physics.Vec3{X: 800, Y: 0, Z: 0})
	s.stepN(1)

	fired := 0
	s.SetInput(p.ID, Input{Seq: 1, Fire: true})
	// Long enough to empty the magazine, short enough that regen cannot refill a whole
	// extra shot: BaseMaxEnergy shots at ShotCooldownTicks apart.
	for i := 0; i < ShotCooldownTicks*BaseMaxEnergy; i++ {
		s.stepN(1)
		fired += len(s.Shots())
	}

	if fired > BaseMaxEnergy+1 {
		t.Errorf("fired %d shots on a %v-shot charge; the energy gate is not holding",
			fired, float32(BaseMaxEnergy))
	}
	if fired < 3 {
		t.Errorf("only %d shots came out; holding the trigger should empty the bar", fired)
	}

	// Drained, then refilled over the advertised window.
	s.SetInput(p.ID, Input{Seq: 2})
	s.stepN(int(EnergyRefillSecs*TickHz) + 4)
	if p.Energy < p.MaxEnergy()-0.01 {
		t.Errorf("charge is %v of %v after a full refill window", p.Energy, p.MaxEnergy())
	}
}

// Upgrades have to actually reach the weapon, or the shop lines are decoration.
func TestWeaponUpgradesApply(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	base := p.MaxEnergy()
	p.Upgrades[UpgradeCapacitor] = 2
	if got := p.MaxEnergy(); got <= base {
		t.Errorf("capacitor did not raise the magazine (%v -> %v)", base, got)
	}

	baseDmg := p.LaserDamageDealt()
	p.Upgrades[UpgradeLaser] = 2
	if got := p.LaserDamageDealt(); got <= baseDmg {
		t.Errorf("focuser did not raise damage (%v -> %v)", baseDmg, got)
	}

	baseRegen := p.EnergyRegenPerSecond()
	p.Upgrades[UpgradeRecharger] = 2
	if got := p.EnergyRegenPerSecond(); got <= baseRegen {
		t.Errorf("recharger did not speed up regen (%v -> %v)", baseRegen, got)
	}
}

// Shooting a colossal rock must produce haulable fragments — that is the only way its
// value ever gets banked, since no beam can tow it.
func TestColossalAsteroidShattersIntoFragments(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("gunner", 0)

	s.world.SetPosition(p.Ship, physics.Vec3{X: 900, Y: 0, Z: 0})
	s.world.SetVelocity(p.Ship, physics.Vec3{})
	s.stepN(1)

	const radius = 14.0
	pos := placeInFrontOf(s, p, 90)
	e := s.world.SpawnSphere(MassFor(TierColossal, radius), radius, pos)
	s.objects[e] = &Object{
		Entity: e, Kind: KindAsteroid, Tier: TierColossal, Radius: radius,
		Team: NoTeam, Value: ValueFor(TierColossal, radius), Integrity: 1,
		RequiredBeams: 4,
		Health:        HealthFor(TierColossal, radius),
		MaxHealth:     HealthFor(TierColossal, radius),
	}
	s.world.SetSleepAllowed(e, false)
	s.world.SetVelocity(e, physics.Vec3{})

	// A colossal rock is a real investment of charge, so give the shooter the ammunition
	// to finish the job rather than testing the energy gate here.
	p.Upgrades[UpgradeLaser] = 3
	s.SetInput(p.ID, Input{Seq: 1, Fire: true})

	// Entity ids are recycled, so "is e still in the map?" is not a safe test — a
	// fragment can be handed the parent's id the moment it despawns. Watch the tier.
	broke := false
	for i := 0; i < 60*TickHz && !broke; i++ {
		s.stepN(1)
		o, still := s.objects[e]
		broke = !still || o.Tier != TierColossal
	}

	if !broke {
		t.Fatalf("colossal rock survived a minute of sustained fire (health %v of %v)",
			s.objects[e].Health, s.objects[e].MaxHealth)
	}

	fragments := 0
	for _, o := range s.objects {
		if o.Kind == KindAsteroid && o.Tier == TierMassive {
			fragments++
		}
	}
	if want := TierColossal.Spec().SplitCount; fragments != want {
		t.Errorf("shattering left %d massive fragments, want %d", fragments, want)
	}
}

// A rock nobody can tow must refuse the beam outright rather than attaching and then
// silently doing nothing.
func TestColossalAsteroidCannotBeGrabbed(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("hauler", 0)
	p.Upgrades[UpgradeTractor] = 1 // even fully upgraded

	s.world.SetPosition(p.Ship, physics.Vec3{X: 900, Y: 0, Z: 0})
	s.world.SetVelocity(p.Ship, physics.Vec3{})
	s.stepN(1)

	const radius = 13.0
	pos := placeInFrontOf(s, p, 25)
	e := s.world.SpawnSphere(MassFor(TierColossal, radius), radius, pos)
	s.objects[e] = &Object{
		Entity: e, Kind: KindAsteroid, Tier: TierColossal, Radius: radius,
		Team: NoTeam, Integrity: 1, RequiredBeams: 4,
	}
	s.world.SetSleepAllowed(e, false)

	s.SetInput(p.ID, Input{Seq: 1, Grab: true})
	s.stepN(10)

	if p.Held != 0 {
		t.Error("a colossal rock was grabbed; it is meant to be shot apart, not towed")
	}
}

// Bolts travel, so a target has to be led — and a flat shot at range falls short.
//
// This replaces a hitscan range test. "Can the beam reach 1300 m" stopped being the
// interesting question the moment rounds had a flight time and an arc; what matters now
// is that a shot connects at fighting range and that distance genuinely costs accuracy.
func TestBoltsTravelStraight(t *testing.T) {
	// Close enough that the arc is negligible: a flat shot must simply connect.
	near := newBareSim(t, nil)
	shooter := near.AddPlayer("gunner", 0)
	victim := near.AddPlayer("target", 1)

	near.world.SetPosition(shooter.Ship, physics.Vec3{X: 0, Y: 0, Z: 700})
	near.world.SetVelocity(shooter.Ship, physics.Vec3{})
	near.stepN(1)
	near.world.SetPosition(victim.Ship, placeInFrontOf(near, shooter, 120))
	near.world.SetVelocity(victim.Ship, physics.Vec3{})
	near.stepN(1)

	before := near.objects[victim.Ship].Health
	near.SetInput(shooter.ID, Input{Seq: 1, Fire: true})
	near.stepN(ShotCooldownTicks * 4)

	if got := near.objects[victim.Ship].Health; got >= before {
		t.Errorf("a 120 m shot did nothing (health %v); bolts are not connecting", got)
	}

	// Not instant: the round has to still be in the air a tick after firing.
	air := newBareSim(t, nil)
	p := air.AddPlayer("gunner", 0)
	air.world.SetPosition(p.Ship, physics.Vec3{X: 0, Y: 0, Z: 700})
	air.world.SetVelocity(p.Ship, physics.Vec3{})
	air.stepN(1)
	air.SetInput(p.ID, Input{Seq: 1, Fire: true})
	air.stepN(1)

	if len(air.Bolts()) == 0 {
		t.Fatal("no bolt in flight the tick after firing; the shot resolved instantly")
	}
	launched := air.Bolts()[0].Pos
	air.stepN(1)
	if len(air.Bolts()) > 0 && air.Bolts()[0].Pos.Sub(launched).Len() < 1 {
		t.Error("the bolt is not moving")
	}

	// And it flies flat. Rounds used to arc down world -Z at 22 m/s²; fired level, one
	// must now hold its height. A metre of slack covers the shooter's own motion being
	// added to muzzle velocity, not an arc — a second of the old drop was 11 m.
	air.stepN(TickHz)
	if bolts := air.Bolts(); len(bolts) > 0 {
		if dz := bolts[0].Pos.Z - launched.Z; dz < -1 || dz > 1 {
			t.Errorf("a bolt moved %.2f m vertically in a second, want a flat trajectory", dz)
		}
	} else {
		t.Error("the bolt vanished before its trajectory could be measured")
	}
}

// A round is spent after BoltMaxRange rather than flying until it leaves the map.
func TestBoltsExpireAtMaxRange(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("gunner", 0)

	// Well above everything, so the only thing that can end this round's flight is the
	// range cap — not a mothership it happened to run into.
	s.world.SetPosition(p.Ship, physics.Vec3{X: 0, Y: 0, Z: 5000})
	s.world.SetVelocity(p.Ship, physics.Vec3{})
	s.stepN(1)

	s.SetInput(p.ID, Input{Seq: 1, Fire: true})
	s.stepN(1)
	s.SetInput(p.ID, Input{Seq: 2}) // stop firing, so only this round is in the air

	bolts := s.Bolts()
	if len(bolts) == 0 {
		t.Fatal("no bolt in flight the tick after firing")
	}
	launched := bolts[0].Pos

	// At 420 m/s, 2000 m is about 4.8 seconds. Halfway there it must still be alive.
	s.stepN(TickHz * 2)
	if len(s.Bolts()) == 0 {
		t.Fatalf("the round was spent inside %.0f m", BoltMaxRange/2)
	}
	if flown := s.Bolts()[0].Pos.Sub(launched).Len(); flown > BoltMaxRange {
		t.Errorf("bolt flew %.0f m, past the %.0f m cap, and is still alive",
			flown, BoltMaxRange)
	}

	// Past the cap it is gone.
	s.stepN(TickHz * 4)
	if n := len(s.Bolts()); n != 0 {
		t.Errorf("%d round(s) still in flight beyond %.0f m", n, BoltMaxRange)
	}
}

// Breaking a titan open must yield the valuable seam inside — that is the entire reason
// to spend a magazine on one instead of flying around it.
func TestTitanYieldsValuableCore(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("gunner", 0)

	s.world.SetPosition(p.Ship, physics.Vec3{X: 0, Y: 0, Z: 900})
	s.world.SetVelocity(p.Ship, physics.Vec3{})
	s.stepN(1)

	const radius = 42.0
	pos := placeInFrontOf(s, p, 200)
	e := s.world.SpawnSphere(MassFor(TierTitan, radius), radius, pos)
	s.objects[e] = &Object{
		Entity: e, Kind: KindAsteroid, Tier: TierTitan, Radius: radius,
		Team: NoTeam, Value: ValueFor(TierTitan, radius), Integrity: 1,
		RequiredBeams: requiredBeamsFor(TierTitan),
		Health:        HealthFor(TierTitan, radius),
		MaxHealth:     HealthFor(TierTitan, radius),
	}
	s.world.SetSleepAllowed(e, false)
	s.world.SetVelocity(e, physics.Vec3{})

	// The gun a crew would actually bring to a titan in a late round.
	p.Upgrades[UpgradeLaser] = 3
	p.Upgrades[UpgradeCapacitor] = 3
	p.Upgrades[UpgradeRecharger] = 3
	p.Energy = p.MaxEnergy()
	s.SetInput(p.ID, Input{Seq: 1, Fire: true})

	// Entity ids are recycled, so watch the tier rather than the map key.
	broke := false
	for i := 0; i < 120*TickHz && !broke; i++ {
		s.stepN(1)
		o, still := s.objects[e]
		broke = !still || o.Tier != TierTitan
	}
	if !broke {
		t.Fatalf("titan survived two minutes of maxed-out fire (health %v of %v)",
			s.objects[e].Health, s.objects[e].MaxHealth)
	}

	cores, colossals := 0, 0
	var coreValue float32
	for _, o := range s.objects {
		switch o.Tier {
		case TierCore:
			cores++
			coreValue += o.Value
		case TierColossal:
			colossals++
		}
	}

	spec := TierTitan.Spec()
	if cores != spec.CoreCount {
		t.Errorf("breaking a titan left %d cores, want %d", cores, spec.CoreCount)
	}
	if colossals != spec.SplitCount {
		t.Errorf("breaking a titan left %d colossal fragments, want %d",
			colossals, spec.SplitCount)
	}

	// One core has to be worth more than a whole ordinary rock, or "very valuable" is
	// just a label.
	best := ValueFor(TierGold, TierGold.Spec().MaxRadius)
	if cores > 0 && coreValue/float32(cores) <= best {
		t.Errorf("a core pays %.0f, no better than the best gold rock at %.0f",
			coreValue/float32(cores), best)
	}
}

// A stock laser must not be able to chew through a titan; that is what the shop is for.
//
// Checked across the tier's whole radius band rather than at one size, so growing the
// rock cannot quietly slide the smallest one into "a stock gun handles it".
func TestTitanNeedsUpgradedGuns(t *testing.T) {
	spec := TierTitan.Spec()

	stock := &Player{Upgrades: map[UpgradeID]int{}}
	maxed := &Player{Upgrades: map[UpgradeID]int{
		UpgradeLaser: 3, UpgradeCapacitor: 3, UpgradeRecharger: 3,
	}}

	// Sustained damage is the shot rate the energy bar allows times damage per shot.
	dps := func(p *Player) float32 {
		rate := p.EnergyRegenPerSecond()
		if cap := float32(TickHz) / ShotCooldownTicks; rate > cap {
			rate = cap
		}
		return rate * p.LaserDamageDealt()
	}

	// The smallest titan is the easiest, so it sets the "impractical alone" bar; the
	// largest is the hardest, so it sets the "worth upgrading for" one.
	smallest := HealthFor(TierTitan, spec.MinRadius)
	largest := HealthFor(TierTitan, spec.MaxRadius)

	t.Logf("titan %.0f-%.0f m: %.0f-%.0f health -> %.0f-%.0fs stock, %.0f-%.0fs maxed",
		spec.MinRadius, spec.MaxRadius, smallest, largest,
		smallest/dps(stock), largest/dps(stock),
		smallest/dps(maxed), largest/dps(maxed))

	if secs := smallest / dps(stock); secs < 120 {
		t.Errorf("a stock gun breaks the smallest titan in %.0fs; it should be "+
			"impractical alone", secs)
	}
	if secs := largest / dps(maxed); secs > 60 {
		t.Errorf("even a maxed gun needs %.0fs on the biggest titan; the upgrades have "+
			"to make it worthwhile", secs)
	}

	// The whole point of this batch: a titan has to dwarf the station it is hauled past.
	if spec.MinRadius <= MothershipRadius {
		t.Errorf("the smallest titan is %.0f m against a %.0f m mothership — it should "+
			"be bigger than the station, not comparable to it",
			spec.MinRadius, float32(MothershipRadius))
	}
}

// The biggest rocks are meant to appear once crews have had an intermission to buy the
// guns for them, not in round one.
func TestBiggestTiersAreGatedByProgress(t *testing.T) {
	seen := func(era int) map[Tier]bool {
		out := map[Tier]bool{}
		for i := 0; i < 20000; i++ {
			out[pickTier(float32(i)/20000, era)] = true
		}
		return out
	}

	early := seen(1)
	if early[TierTitan] {
		t.Error("titans are spawning in the first round")
	}
	if early[TierColossal] {
		t.Error("colossal rocks are spawning in the first round")
	}

	late := seen(4)
	if !late[TierTitan] {
		t.Error("titans never appear, even in a late round")
	}
	if !late[TierColossal] {
		t.Error("colossal rocks never appear, even in a late round")
	}

	// Core seams are cut out of big rocks; finding one loose in the belt would make
	// breaking a titan open pointless.
	for _, era := range []int{0, 1, 4, 20} {
		if seen(era)[TierCore] {
			t.Errorf("a core spawned loose in the belt at era %d", era)
		}
	}
}

// Deposits must land at your own team's mothership and nowhere else.
func TestDepositOnlyCountsAtOwnMothership(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("hauler", 0)

	// Park at a *rival's* mothership with a rock in hand.
	var rival uint8 = 1
	if p.Team == rival {
		rival = 2
	}
	enemyHome, ok := s.motherships[rival]
	if !ok {
		t.Skip("no second team in this configuration")
	}
	eb, _ := s.world.GetBody(enemyHome)

	s.world.SetPosition(p.Ship, eb.Pos.Add(physics.Vec3{X: 40, Y: 0, Z: 0}))
	s.world.SetVelocity(p.Ship, physics.Vec3{})
	s.stepN(1)

	ship, _ := s.world.GetBody(p.Ship)
	rock := s.addRock(ship.Pos.Add(ship.Rot.Forward().Scale(5)), 30, 2, 400)

	s.SetInput(p.ID, Input{Seq: 1, Grab: true})
	s.stepN(8)

	if _, still := s.objects[rock]; !still {
		t.Error("the rock was banked at a rival's mothership")
	}
	for _, team := range s.teams {
		if team.Score != 0 {
			t.Errorf("team %d scored %v from a delivery to the wrong hangar",
				team.ID, team.Score)
		}
	}
}
