package sim

import (
	"testing"

	"asteroidsalvage/physics"
)

func newHoardSim(t *testing.T, mutate func(*Config)) *Sim {
	t.Helper()
	cfg := DefaultConfig()
	cfg.AutoRestock = false
	cfg.PropCount = 0
	cfg.Match.DisableMatchFlow = true
	cfg.Match.Mode = ModeHoard
	cfg.Match.Modes = DefaultModeConfig()
	if mutate != nil {
		mutate(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// putRock drops a collectable rock at a spot and returns it.
func putRock(s *Sim, pos physics.Vec3, value float32) *Object {
	e := s.world.SpawnSphere(MassFor(TierIron, 3), 3, pos)
	o := &Object{
		Entity: e, Kind: KindAsteroid, Tier: TierIron, Radius: 3,
		Team: NoTeam, Value: value, Integrity: 1, RequiredBeams: 1,
	}
	s.objects[e] = o
	return o
}

// The whole mode: fly into a rock and it is yours, with no trip home.
func TestHoardCollectsOnContact(t *testing.T) {
	s := newHoardSim(t, nil)
	p := s.AddPlayer("hippo", 0)

	s.world.SetPosition(p.Ship, physics.Vec3{X: 0, Y: 0, Z: 600})
	s.world.SetVelocity(p.Ship, physics.Vec3{})
	s.stepN(1)

	ship, _ := s.world.GetBody(p.Ship)
	rock := putRock(s, ship.Pos.Add(physics.Vec3{Y: 4}), 100)
	s.stepN(2)

	if _, still := s.objects[rock.Entity]; still {
		t.Fatal("the rock was not collected on contact")
	}
	want := 100 * s.matchCfg.Modes.HoardPickupBonus
	if p.Credits != want {
		t.Errorf("collected %v credits, want %v", p.Credits, want)
	}
	if got := s.teams[p.Team].Score; got != want {
		t.Errorf("team scored %v, want %v", got, want)
	}
}

// Collection must not happen in Salvage, or hauling would be pointless.
func TestSalvageDoesNotCollectOnContact(t *testing.T) {
	s := newHoardSim(t, func(c *Config) { c.Match.Mode = ModeSalvage })
	p := s.AddPlayer("hauler", 0)

	s.world.SetPosition(p.Ship, physics.Vec3{X: 0, Y: 0, Z: 600})
	s.world.SetVelocity(p.Ship, physics.Vec3{})
	s.stepN(1)

	ship, _ := s.world.GetBody(p.Ship)
	rock := putRock(s, ship.Pos.Add(physics.Vec3{Y: 4}), 100)
	s.stepN(2)

	if _, still := s.objects[rock.Entity]; !still {
		t.Error("a rock vanished on contact in salvage mode")
	}
	if p.Credits != 0 {
		t.Errorf("salvage paid %v for touching a rock", p.Credits)
	}
}

// Rocks somebody has been bouncing off the scenery are worth less, even in a scramble.
func TestHoardRespectsIntegrity(t *testing.T) {
	s := newHoardSim(t, nil)
	p := s.AddPlayer("hippo", 0)
	s.world.SetPosition(p.Ship, physics.Vec3{X: 0, Y: 0, Z: 600})
	s.world.SetVelocity(p.Ship, physics.Vec3{})
	s.stepN(1)

	ship, _ := s.world.GetBody(p.Ship)
	rock := putRock(s, ship.Pos.Add(physics.Vec3{Y: 4}), 100)
	rock.Integrity = 0.5
	s.stepN(2)

	want := 100 * 0.5 * s.matchCfg.Modes.HoardPickupBonus
	if p.Credits != want {
		t.Errorf("a half-wrecked rock paid %v, want %v", p.Credits, want)
	}
}

// A runaway leader should not have to sit through the rest of a decided round.
func TestHoardTargetEndsTheRound(t *testing.T) {
	s := newMatchSim(t, func(c *Config) {
		c.Match.Mode = ModeHoard
		c.Match.Modes = ModeConfig{HoardTarget: 200, HoardPickupBonus: 1}
		c.Match.RoundSeconds = 120
	})
	p := s.AddPlayer("hippo", 0)
	s.stepN(TickHz + 2) // into a live round

	s.world.SetPosition(p.Ship, physics.Vec3{X: 0, Y: 0, Z: 600})
	s.world.SetVelocity(p.Ship, physics.Vec3{})
	s.stepN(1)

	ship, _ := s.world.GetBody(p.Ship)
	putRock(s, ship.Pos.Add(physics.Vec3{Y: 4}), 250)
	s.stepN(2)

	if got := s.match.RoundWins[p.Team]; got != 1 {
		t.Errorf("hitting the target gave %d round wins, want 1", got)
	}
	if s.match.Phase != PhaseIntermission {
		t.Errorf("phase %v after reaching the target, want the shop", s.match.Phase)
	}
}

func newKothSim(t *testing.T, mutate func(*Config)) *Sim {
	t.Helper()
	cfg := DefaultConfig()
	cfg.AutoRestock = false
	cfg.PropCount = 0
	cfg.Match.DisableMatchFlow = true
	cfg.Match.Mode = ModeKingOfTheHill
	cfg.Match.Modes = DefaultModeConfig()
	if mutate != nil {
		mutate(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// standOnHill parks a player in the middle of the control point.
func standOnHill(s *Sim, p *Player) {
	s.world.SetPosition(p.Ship, s.hillPos)
	s.world.SetVelocity(p.Ship, physics.Vec3{})
	s.stepN(1)
}

// clearScores zeroes the board after setup.
//
// Placing a ship costs a step to settle the physics, and a step on the hill is a tick of
// control — so simply arranging a test scores a fraction of a point before the thing
// under test has even started. Tests that measure from zero call this once they are set
// up rather than allowing for a tick of slop in every assertion.
func clearScores(s *Sim) {
	for _, t := range s.teams {
		t.Score = 0
	}
}

// Holding the hill alone has to pay, at the configured rate.
func TestKothScoresWhileHeld(t *testing.T) {
	s := newKothSim(t, nil)
	p := s.AddPlayer("king", 0)
	standOnHill(s, p)
	clearScores(s)

	s.stepN(TickHz) // one second of control

	got := s.teams[p.Team].Score
	want := s.matchCfg.Modes.KothRate
	if absf(got-want) > want*0.25 {
		t.Errorf("a second of control scored %v, want about %v", got, want)
	}
	if h := s.Hill(); h.Team != p.Team || h.Contested {
		t.Errorf("hill reports team %d contested %v, want team %d uncontested",
			h.Team, h.Contested, p.Team)
	}
}

// An empty hill pays nobody.
func TestKothPaysNothingWhenEmpty(t *testing.T) {
	s := newKothSim(t, nil)
	p := s.AddPlayer("loiterer", 0)

	// Well outside the volume.
	s.world.SetPosition(p.Ship, s.hillPos.Add(physics.Vec3{X: HillRadius * 4}))
	s.world.SetVelocity(p.Ship, physics.Vec3{})
	s.stepN(TickHz)

	if got := s.teams[p.Team].Score; got != 0 {
		t.Errorf("scored %v from outside the hill", got)
	}
	if h := s.Hill(); h.Team != NoTeam || h.Contested {
		t.Error("an empty hill reports a holder")
	}
}

// Contested means nobody scores — otherwise the right play against a leader is to leave.
func TestKothContestedPaysNobody(t *testing.T) {
	s := newKothSim(t, nil)
	a := s.AddPlayer("red", 0)
	b := s.AddPlayer("blue", 1)
	if a.Team == b.Team {
		t.Fatal("test needs two teams")
	}

	standOnHill(s, a)
	standOnHill(s, b)
	clearScores(s)
	s.stepN(TickHz)

	for id, tm := range s.teams {
		if tm.Score != 0 {
			t.Errorf("team %d scored %v on a contested hill", id, tm.Score)
		}
	}
	if h := s.Hill(); !h.Contested || h.Team != NoTeam {
		t.Errorf("hill reports team %d contested %v, want contested and unowned",
			h.Team, h.Contested)
	}
}

// A dead ship does not hold ground.
func TestKothIgnoresTheDead(t *testing.T) {
	s := newKothSim(t, nil)
	p := s.AddPlayer("ghost", 0)
	standOnHill(s, p)

	clearScores(s)
	p.respawnIn = RespawnTicks
	s.stepN(TickHz)

	if got := s.teams[p.Team].Score; got != 0 {
		t.Errorf("a wreck held the hill for %v points", got)
	}
}

// The hill has to move, or it becomes one entrenched position for the whole round.
func TestKothHillRelocates(t *testing.T) {
	s := newKothSim(t, func(c *Config) { c.Match.Modes.KothShiftSecs = 2 })
	before := s.hillPos

	s.stepN(2*TickHz + 4)

	if s.hillPos.Sub(before).Len() < 1 {
		t.Error("the hill never moved")
	}
	// And wherever it went, it must still be legal ground.
	for team := range s.motherships {
		home := s.mothershipPos(team, len(s.teams))
		if s.hillPos.Sub(home).Len() < HillRadius+MothershipRadius {
			t.Errorf("the hill moved onto team %d's station", team)
		}
	}
}

// Reaching the target ends the round rather than running the clock out on a decided one.
func TestKothTargetEndsTheRound(t *testing.T) {
	s := newMatchSim(t, func(c *Config) {
		c.PropCount = 0
		c.Match.Mode = ModeKingOfTheHill
		c.Match.Modes = ModeConfig{KothTarget: 10, KothRate: 40, KothShiftSecs: 0}
		c.Match.RoundSeconds = 120
	})
	p := s.AddPlayer("king", 0)
	s.stepN(TickHz + 2) // into a live round
	standOnHill(s, p)

	s.stepN(TickHz)

	if got := s.match.RoundWins[p.Team]; got != 1 {
		t.Errorf("holding to the target gave %d round wins, want 1", got)
	}
	if s.match.Phase != PhaseIntermission {
		t.Errorf("phase %v after reaching the target, want the shop", s.match.Phase)
	}
}

// Holding the hill pays the crew's wallets as well as the scoreboard: round points win
// the round, credits buy upgrades that outlast it.
func TestKothPaysCreditsWhileHeld(t *testing.T) {
	s := newKothSim(t, func(c *Config) {
		c.Match.Modes.KothCredits = 20
		c.Match.Modes.KothShiftSecs = 0
	})
	holder := s.AddPlayer("king", 0)
	mate := s.AddPlayer("crew", 0)
	if mate.Team != holder.Team {
		t.Fatalf("test needs both on one crew, got %d and %d", holder.Team, mate.Team)
	}
	rival := s.AddPlayer("blue", 1)

	standOnHill(s, holder)
	holder.Credits, mate.Credits, rival.Credits = 0, 0, 0

	s.stepN(TickHz) // one second of control

	if absf(holder.Credits-20) > 1 {
		t.Errorf("the holder earned %v credits in a second, want about 20", holder.Credits)
	}
	// The teammate flying escort is doing the other half of the job.
	if absf(mate.Credits-20) > 1 {
		t.Errorf("a crewmate earned %v credits, want about 20", mate.Credits)
	}
	if rival.Credits != 0 {
		t.Errorf("a rival earned %v credits from someone else's hill", rival.Credits)
	}
}

// No hill, no wages.
func TestKothPaysNoCreditsWhenContested(t *testing.T) {
	s := newKothSim(t, func(c *Config) { c.Match.Modes.KothCredits = 20 })
	a := s.AddPlayer("red", 0)
	b := s.AddPlayer("blue", 1)
	if a.Team == b.Team {
		t.Fatal("test needs two teams")
	}

	standOnHill(s, a)
	standOnHill(s, b)
	a.Credits, b.Credits = 0, 0

	s.stepN(TickHz)

	if a.Credits != 0 || b.Credits != 0 {
		t.Errorf("a contested hill paid %v and %v", a.Credits, b.Credits)
	}
}
