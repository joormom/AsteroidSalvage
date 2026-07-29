package sim

import "testing"

// newMatchSim builds a sim with the tournament running, on a short clock so a whole
// best-of-N can be played inside a test.
func newMatchSim(t *testing.T, mutate func(*Config)) *Sim {
	t.Helper()
	cfg := DefaultConfig()
	cfg.AutoRestock = false
	cfg.PropCount = 0
	cfg.Match = MatchConfig{
		BestOf:           5,
		WarmupSeconds:    1,
		RoundSeconds:     2,
		IntermissionSecs: 1,
	}
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

// award banks a fixed amount for a team by hand, standing in for a delivery.
func (s *Sim) award(team uint8, amount float32) {
	if tm, ok := s.teams[team]; ok {
		tm.Score += amount
	}
}

func TestMatchStartsInWarmupThenRuns(t *testing.T) {
	s := newMatchSim(t, nil)

	if s.Match().Phase != PhaseWarmup {
		t.Fatalf("phase = %v, want warmup", s.Match().Phase)
	}

	s.stepN(TickHz + 2) // outlast the 1 s warmup
	if s.Match().Phase != PhaseRound {
		t.Fatalf("phase = %v after warmup, want round", s.Match().Phase)
	}
	if s.Match().Round != 1 {
		t.Errorf("round = %d, want 1", s.Match().Round)
	}
}

func TestRoundEndsAndOpensTheShop(t *testing.T) {
	s := newMatchSim(t, nil)
	s.stepN(TickHz + 2) // into round 1

	s.award(0, 500)
	s.stepN(2*TickHz + 2) // outlast the round

	m := s.Match()
	if m.Phase != PhaseIntermission {
		t.Fatalf("phase = %v after the round clock, want intermission", m.Phase)
	}
	if m.RoundWins[0] != 1 {
		t.Errorf("team 0 round wins = %d, want 1", m.RoundWins[0])
	}
}

// Team scores reset each round; the round wins are what carry.
func TestTeamScoresResetBetweenRounds(t *testing.T) {
	s := newMatchSim(t, nil)
	s.stepN(TickHz + 2)

	s.award(0, 500)
	s.stepN(2*TickHz + 2) // end round 1 -> intermission
	s.stepN(TickHz + 2)   // end intermission -> round 2

	if s.Match().Round != 2 {
		t.Fatalf("round = %d, want 2", s.Match().Round)
	}
	for id, tm := range s.teams {
		if tm.Score != 0 {
			t.Errorf("team %d carried %v into round 2, want a clean slate", id, tm.Score)
		}
	}
	if s.Match().RoundWins[0] != 1 {
		t.Errorf("round wins were reset; they should carry")
	}
}

// Best-of-5 ends the moment a team reaches three round wins.
func TestMatchEndsAtMajority(t *testing.T) {
	s := newMatchSim(t, nil)
	s.stepN(TickHz + 2)

	for i := 0; i < 3; i++ {
		s.award(1, 100)
		s.stepN(2*TickHz + 2) // finish the round
		if s.Match().Phase == PhaseMatchOver {
			break
		}
		s.stepN(TickHz + 2) // finish the intermission
	}

	m := s.Match()
	if m.Phase != PhaseMatchOver {
		t.Fatalf("phase = %v after three round wins, want match over", m.Phase)
	}
	if m.Winner != 1 {
		t.Errorf("winner = %d, want team 1", m.Winner)
	}
	if m.RoundWins[1] < 3 {
		t.Errorf("team 1 has %d round wins, want 3", m.RoundWins[1])
	}
}

// A scoreless round is a draw and must not hand anyone a win.
func TestScorelessRoundAwardsNobody(t *testing.T) {
	s := newMatchSim(t, nil)
	s.stepN(TickHz + 2)
	s.stepN(2*TickHz + 2)

	for id, wins := range s.Match().RoundWins {
		if wins != 0 {
			t.Errorf("team %d won a round nobody scored in (%d)", id, wins)
		}
	}
}

/*============================================================================
 * Shop
 *===========================================================================*/

func TestUpgradesOnlyPurchasableDuringIntermission(t *testing.T) {
	s := newMatchSim(t, nil)
	p := s.AddPlayer("pilot", 0)
	p.Credits = 10000

	s.stepN(TickHz + 2) // mid-round
	if _, ok := s.Buy(p.ID, UpgradeThrust); ok {
		t.Error("bought an upgrade mid-round; the shop should be closed")
	}

	s.award(0, 100)
	s.stepN(2*TickHz + 2) // into intermission
	if s.Match().Phase != PhaseIntermission {
		t.Fatalf("expected intermission, got %v", s.Match().Phase)
	}

	if _, ok := s.Buy(p.ID, UpgradeThrust); !ok {
		t.Error("could not buy during the intermission")
	}
}

func TestUpgradesCostCreditsAndCap(t *testing.T) {
	s := newMatchSim(t, nil)
	p := s.AddPlayer("pilot", 0)
	s.stepN(TickHz + 2)
	s.award(0, 100)
	s.stepN(2*TickHz + 2) // intermission

	// Too poor.
	p.Credits = 10
	if _, ok := s.Buy(p.ID, UpgradeTractor); ok {
		t.Error("bought the tractor amplifier with 10 credits")
	}

	// Affordable.
	cost, _ := CostFor(UpgradeTractor, 0)
	p.Credits = cost + 1
	if lvl, ok := s.Buy(p.ID, UpgradeTractor); !ok || lvl != 1 {
		t.Fatalf("purchase failed: level %d ok %v", lvl, ok)
	}
	if p.Credits > 1.001 {
		t.Errorf("credits = %v after paying %v, want ~1", p.Credits, cost)
	}
	if p.BeamStrength != 2 {
		t.Errorf("BeamStrength = %d after the amplifier, want 2", p.BeamStrength)
	}

	// Maxed out.
	p.Credits = 100000
	if _, ok := s.Buy(p.ID, UpgradeTractor); ok {
		t.Error("bought the tractor amplifier twice; MaxLevel is 1")
	}
}

// Upgrades must affect only the buyer, never the shared Tuning.
func TestUpgradesArePerPlayer(t *testing.T) {
	s := newMatchSim(t, nil)
	rich := s.AddPlayer("rich", 0)
	poor := s.AddPlayer("poor", 1)

	s.stepN(TickHz + 2)
	s.award(0, 100)
	s.stepN(2*TickHz + 2)

	rich.Credits = 100000
	for i := 0; i < 3; i++ {
		s.Buy(rich.ID, UpgradeThrust)
		s.Buy(rich.ID, UpgradeRange)
	}

	if rich.ThrustMultiplier() <= poor.ThrustMultiplier() {
		t.Errorf("thrust upgrade did not differentiate: rich %.2f poor %.2f",
			rich.ThrustMultiplier(), poor.ThrustMultiplier())
	}
	if rich.GrabRangeBonus() <= poor.GrabRangeBonus() {
		t.Errorf("range upgrade did not differentiate: rich %.1f poor %.1f",
			rich.GrabRangeBonus(), poor.GrabRangeBonus())
	}
	if poor.ThrustMultiplier() != 1.0 {
		t.Errorf("the other player's thrust changed (%.2f) — upgrades leaked into "+
			"shared tuning", poor.ThrustMultiplier())
	}
}

// Each intermission must present a fresh set of buyable choices.
func TestOffersRolledEachIntermission(t *testing.T) {
	s := newMatchSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	s.stepN(TickHz + 2) // round 1
	if len(p.Offers) != 0 {
		t.Errorf("offers exist mid-round: %v", p.Offers)
	}

	s.award(0, 100)
	s.stepN(2*TickHz + 2) // intermission

	if len(p.Offers) != OffersPerIntermission {
		t.Fatalf("got %d offers, want %d", len(p.Offers), OffersPerIntermission)
	}

	seen := map[UpgradeID]bool{}
	for _, o := range p.Offers {
		if seen[o.Upgrade] {
			t.Errorf("offer list repeats %v; a grid of duplicates looks broken", o.Upgrade)
		}
		seen[o.Upgrade] = true
		if o.Cost <= 0 {
			t.Errorf("offer %v has no cost", o.Upgrade)
		}
		if o.Level != 1 {
			t.Errorf("first-time offer for %v is level %d, want 1", o.Upgrade, o.Level)
		}
	}
}

// Buying must go through an offered slot, not an arbitrary upgrade id.
func TestBuyOfferOnlyAcceptsOfferedSlots(t *testing.T) {
	s := newMatchSim(t, nil)
	p := s.AddPlayer("pilot", 0)
	s.stepN(TickHz + 2)
	s.award(0, 100)
	s.stepN(2*TickHz + 2) // intermission

	p.Credits = 100000
	before := len(p.Offers)

	if ok, why := s.BuyOffer(p.ID, 99); ok {
		t.Error("bought a slot that was never offered")
	} else if why == "" {
		t.Error("rejection gave no reason")
	}

	offer := p.Offers[0]
	ok, why := s.BuyOffer(p.ID, 0)
	if !ok {
		t.Fatalf("could not buy slot 0: %s", why)
	}
	if p.Upgrades[offer.Upgrade] != 1 {
		t.Errorf("upgrade %v is level %d, want 1", offer.Upgrade, p.Upgrades[offer.Upgrade])
	}
	if len(p.Offers) != before-1 {
		t.Errorf("bought slot was not removed: %d offers remain, want %d",
			len(p.Offers), before-1)
	}
}

func TestBuyOfferRejectedOutsideIntermission(t *testing.T) {
	s := newMatchSim(t, nil)
	p := s.AddPlayer("pilot", 0)
	p.Credits = 100000
	p.Offers = []Offer{{Upgrade: UpgradeThrust, Level: 1, Cost: 1}}

	s.stepN(TickHz + 2) // mid-round
	if ok, _ := s.BuyOffer(p.ID, 0); ok {
		t.Error("bought during a live round; the shop should be closed")
	}
}

// Maxed upgrades must stop appearing, or slots get wasted on things you cannot buy.
func TestOffersExcludeMaxedUpgrades(t *testing.T) {
	s := newMatchSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	// Max everything except Engine Boost. Driven off the spec table rather than a hand
	// written list, so adding a shop line does not silently break this — and through
	// grantLevel rather than the player's own map, because station lines count up on the
	// team and writing them to the player would leave them looking unbought.
	for _, spec := range UpgradeSpecs {
		if spec.ID != UpgradeThrust {
			s.grantLevel(p, spec.ID, spec.MaxLevel)
		}
	}

	s.stepN(TickHz + 2)
	s.award(0, 100)
	s.stepN(2*TickHz + 2)

	if len(p.Offers) != 1 {
		t.Fatalf("got %d offers, want only the one upgrade still available", len(p.Offers))
	}
	if p.Offers[0].Upgrade != UpgradeThrust {
		t.Errorf("offered %v, want the only unmaxed upgrade", p.Offers[0].Upgrade)
	}
}

func TestTeamNaming(t *testing.T) {
	s := newMatchSim(t, nil)
	p := s.AddPlayer("pilot", 1)

	if got := s.teams[1].Name; got != DefaultTeamName(1) {
		t.Errorf("default team name is %q, want %q", got, DefaultTeamName(1))
	}

	if !s.SetTeamName(p.ID, "Rock Hounds") {
		t.Fatal("rename rejected")
	}
	if got := s.teams[1].Name; got != "Rock Hounds" {
		t.Errorf("team name is %q after rename", got)
	}

	if s.SetTeamName(p.ID, "   ") {
		t.Error("accepted a blank team name")
	}
	if s.SetTeamName(p.ID, "this name is far too long to be reasonable") {
		t.Error("accepted an overlong team name")
	}
	if got := s.teams[1].Name; got != "Rock Hounds" {
		t.Errorf("a rejected rename changed the name to %q", got)
	}
}

func TestCargoDampenersReduceDamage(t *testing.T) {
	s := newMatchSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	if p.CargoDamageScale() != 1.0 {
		t.Errorf("base cargo damage scale = %v, want 1", p.CargoDamageScale())
	}
	p.Upgrades[UpgradeHull] = 2
	if got := p.CargoDamageScale(); got >= 0.5 {
		t.Errorf("two dampener levels give scale %v, want well under 0.5", got)
	}
}

// Credits and upgrades must survive a round boundary, or there is no reason to care
// about a round you have already lost.
func TestCreditsAndUpgradesPersistAcrossRounds(t *testing.T) {
	s := newMatchSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	s.stepN(TickHz + 2)
	s.award(0, 100)
	s.stepN(2*TickHz + 2) // intermission

	p.Credits = 5000
	s.Buy(p.ID, UpgradeThrust)
	creditsAfter := p.Credits

	s.stepN(TickHz + 2) // into round 2

	if s.Match().Round != 2 {
		t.Fatalf("round = %d, want 2", s.Match().Round)
	}
	if p.Upgrades[UpgradeThrust] != 1 {
		t.Errorf("upgrade lost across the round boundary")
	}
	if p.Credits != creditsAfter {
		t.Errorf("credits changed across the boundary: %v -> %v", creditsAfter, p.Credits)
	}
}
