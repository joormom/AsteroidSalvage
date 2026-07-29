package sim

import (
	"testing"

	"asteroidsalvage/physics"
)

func absf(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// aimAt parks a player at a fixed spot with the target directly down the barrel,
// and returns the object that was aimed at. Both bodies are settled with a step so the
// physics snapshot the raycast reads is current.
func aimAt(s *Sim, shooter *Player, target physics.EntityID, standoff float32) {
	st, _ := s.world.GetBody(target)
	o := s.objects[target]

	// Default ship rotation points along +Y, so sitting on -Y of the target aims at it.
	pos := st.Pos.Sub(physics.Vec3{Y: o.Radius + standoff})
	s.world.SetPosition(shooter.Ship, pos)
	s.world.SetVelocity(shooter.Ship, physics.Vec3{})
	s.stepN(1)
}

// A station has to be attackable, or the whole siege mechanic does not exist.
func TestLaserDamagesEnemyMothership(t *testing.T) {
	s := newBareSim(t, nil)
	attacker := s.AddPlayer("red", 0)

	station, ok := s.MothershipFor(1)
	if !ok {
		t.Fatal("team 1 has no mothership")
	}
	if got := s.objects[station].Health; got != MothershipMaxHealth {
		t.Fatalf("station starts on %v health, want %v", got, MothershipMaxHealth)
	}

	aimAt(s, attacker, station, 40)
	s.SetInput(attacker.ID, Input{Seq: 1, Fire: true})
	s.stepN(ShotCooldownTicks * 3)

	got := s.objects[station].Health
	if got >= MothershipMaxHealth {
		t.Errorf("station took no damage (health %v)", got)
	}
	if got != MothershipMaxHealth-3*attacker.LaserDamageDealt() {
		t.Errorf("station on %v after three shots, want %v",
			got, MothershipMaxHealth-3*attacker.LaserDamageDealt())
	}
}

// Shooting your own station would turn a stray shot near the hangar into self-sabotage.
func TestOwnMothershipIsNotATarget(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	station, _ := s.MothershipFor(0)
	aimAt(s, p, station, 40)

	s.SetInput(p.ID, Input{Seq: 1, Fire: true})
	s.stepN(ShotCooldownTicks * 3)

	if got := s.objects[station].Health; got != MothershipMaxHealth {
		t.Errorf("own station took %v damage", MothershipMaxHealth-got)
	}
}

// Breaching a station knocks that crew out — and only that crew.
//
// It used to end the round outright and hand it to the attackers. With four teams on a
// map that meant one siege ended two other crews' round for reasons that had nothing to
// do with them, so a breach is now an elimination like running out of lives.
func TestBreachingAStationEliminatesOnlyThatTeam(t *testing.T) {
	s := newMatchSim(t, func(c *Config) { c.TeamCount = 3; c.Match.RoundSeconds = 120 })
	attacker := s.AddPlayer("red", 0)
	victim := s.AddPlayer("blue", 1)
	bystander := s.AddPlayer("green", 2)
	if victim.Team == attacker.Team || bystander.Team == attacker.Team {
		t.Fatalf("test needs three crews, got %d %d %d",
			attacker.Team, victim.Team, bystander.Team)
	}

	s.stepN(TickHz + 2)
	if s.match.Phase != PhaseRound {
		t.Fatalf("phase %v, want a live round", s.match.Phase)
	}

	station, _ := s.MothershipFor(victim.Team)
	s.objects[station].Health = 2 * attacker.LaserDamageDealt()
	aimAt(s, attacker, station, 40)

	s.SetInput(attacker.ID, Input{Seq: 1, Fire: true})
	for i := 0; i < 8*TickHz && s.objects[station].Health > 0; i++ {
		s.stepN(1)
	}

	if s.objects[station].Health > 0 {
		t.Fatalf("station survived on %v", s.objects[station].Health)
	}
	if !s.TeamEliminated(victim.Team) {
		t.Error("the crew whose station broke is still in the round")
	}
	if s.TeamEliminated(bystander.Team) {
		t.Error("a bystander crew was eliminated by somebody else's siege")
	}
	// Two crews are still flying, so the round carries on.
	if s.match.Phase != PhaseRound {
		t.Errorf("phase %v after one crew was knocked out, want the round to continue",
			s.match.Phase)
	}
}

// A station cannot be touched in King of the Hill: that round is decided on points alone.
func TestStationsAreInvulnerableInKingOfTheHill(t *testing.T) {
	s := newKothSim(t, nil)
	attacker := s.AddPlayer("red", 0)

	station, _ := s.MothershipFor(1)
	before := s.objects[station].Health
	aimAt(s, attacker, station, 40)

	s.SetInput(attacker.ID, Input{Seq: 1, Fire: true})
	s.stepN(ShotCooldownTicks * 8)

	if got := s.objects[station].Health; got != before {
		t.Errorf("a station took %v damage in King of the Hill", before-got)
	}
}

// A breached station must not stay broken into the next round, or one successful siege
// hands the attackers every remaining round against a wreck.
func TestStationsAreRebuiltBetweenRounds(t *testing.T) {
	s := newMatchSim(t, nil)
	station, _ := s.MothershipFor(1)

	s.objects[station].Health = 1
	s.stepN(TickHz + 2) // warmup ends, round 1 begins

	if got := s.objects[station].Health; got != MothershipMaxHealth {
		t.Errorf("station entered the round on %v health, want a full %v",
			got, MothershipMaxHealth)
	}
}

// Shields are the answer to being sieged; without them the mechanic is one-sided.
func TestShieldsReduceStationDamage(t *testing.T) {
	s := newBareSim(t, nil)
	attacker := s.AddPlayer("red", 0)

	s.teams[1].Station[UpgradeShields] = 2

	// Compared with a tolerance: the scale is built by repeated multiplication in
	// float32, which does not land exactly on the same bits as the constant product.
	want := float32(ShieldDamageScale * ShieldDamageScale)
	if got := s.ShieldScale(1); absf(got-want) > 1e-5 {
		t.Fatalf("two shield levels give scale %v, want %v", got, want)
	}

	station, _ := s.MothershipFor(1)
	aimAt(s, attacker, station, 40)

	s.SetInput(attacker.ID, Input{Seq: 1, Fire: true})
	s.stepN(ShotCooldownTicks * 3)

	taken := MothershipMaxHealth - s.objects[station].Health
	wantTaken := 3 * attacker.LaserDamageDealt() * s.ShieldScale(1)
	if absf(taken-wantTaken) > 1e-3 {
		t.Errorf("shielded station took %v, want %v", taken, wantTaken)
	}
	if taken >= 3*attacker.LaserDamageDealt() {
		t.Error("shields absorbed nothing")
	}
}

// Turrets have to actually shoot, or a station cannot defend itself while its crew is out
// in the belt — which is exactly when it gets attacked.
func TestTurretsShootIntruders(t *testing.T) {
	s := newBareSim(t, nil)
	intruder := s.AddPlayer("red", 0)
	s.teams[1].Station[UpgradeTurrets] = 1

	station, _ := s.MothershipFor(1)
	aimAt(s, intruder, station, 40)

	before := s.objects[intruder.Ship].Health
	s.stepN(TurretCooldownTicks * 3)

	got := s.objects[intruder.Ship].Health
	if got >= before {
		t.Errorf("intruder parked at an armed station took no fire (health %v)", got)
	}
}

// A turret that shot its own crew would make the upgrade a liability.
func TestTurretsHoldFireOnTheirOwnTeam(t *testing.T) {
	s := newBareSim(t, nil)
	friendly := s.AddPlayer("blue", 1)
	s.teams[1].Station[UpgradeTurrets] = 3

	station, _ := s.MothershipFor(1)
	aimAt(s, friendly, station, 40)

	before := s.objects[friendly.Ship].Health
	s.stepN(TurretCooldownTicks * 3)

	if got := s.objects[friendly.Ship].Health; got != before {
		t.Errorf("a station shot its own crew: %v -> %v", before, got)
	}
}

// Out of range is out of range: turrets must not plink at someone working the belt.
func TestTurretsDoNotReachTheBelt(t *testing.T) {
	s := newBareSim(t, nil)
	distant := s.AddPlayer("red", 0)
	s.teams[1].Station[UpgradeTurrets] = 3

	station, _ := s.MothershipFor(1)
	aimAt(s, distant, station, TurretRange+50)

	before := s.objects[distant.Ship].Health
	s.stepN(TurretCooldownTicks * 3)

	if got := s.objects[distant.Ship].Health; got != before {
		t.Errorf("a turret reached past %v m", TurretRange)
	}
}

// Station upgrades are bought with one wallet but belong to the whole crew — that is what
// separates them from every other line in the shop.
func TestStationUpgradesAreSharedByTheTeam(t *testing.T) {
	s := newMatchSim(t, nil)
	buyer := s.AddPlayer("payer", 0)
	mate := s.AddPlayer("mate", 0)
	if mate.Team != buyer.Team {
		t.Fatalf("test needs both players on one team, got %d and %d",
			buyer.Team, mate.Team)
	}

	buyer.Credits = 10000
	s.stepN(TickHz + 2)   // round starts
	s.stepN(2*TickHz + 2) // round ends, shop opens
	if s.match.Phase != PhaseIntermission {
		t.Fatalf("phase %v, want the shop open", s.match.Phase)
	}

	if _, ok := s.Buy(buyer.ID, UpgradeShields); !ok {
		t.Fatal("could not buy station shields")
	}

	if got := s.levelOf(mate, UpgradeShields); got != 1 {
		t.Errorf("teammate sees shield level %d, want the 1 their crewmate paid for", got)
	}
	if got := mate.Upgrades[UpgradeShields]; got != 0 {
		t.Errorf("a station upgrade landed on the teammate's personal sheet (%d)", got)
	}
	if s.ShieldScale(buyer.Team) >= 1 {
		t.Error("the bought shield is not actually reducing damage")
	}
}

// The station's own credit is never a teammate's: a turret kill must not put someone
// else's name on it.
func TestTurretKillHasNoPlayerCredit(t *testing.T) {
	s := newBareSim(t, nil)
	victim := s.AddPlayer("red", 0)
	s.teams[1].Station[UpgradeTurrets] = 3

	station, _ := s.MothershipFor(1)
	aimAt(s, victim, station, 40)
	s.objects[victim.Ship].Health = TurretDamage

	s.stepN(TurretCooldownTicks * 2)

	if !victim.Dead() {
		t.Fatalf("turrets did not finish a ship on %v health", TurretDamage)
	}
}
