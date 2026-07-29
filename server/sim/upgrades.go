package sim

// Ship upgrades, bought with personal credits between rounds.
//
// Credits are personal and team score is not: the team wins the match, but you upgrade
// your own ship. That split is deliberate — it means helping a teammate haul a massive
// asteroid pays you directly (the payout splits between everyone with a beam on it),
// so co-operation is never charity.
//
// Upgrades persist for the whole match. They are the reason to care about the rounds
// you have already played.

type UpgradeID uint8

const (
	UpgradeTractor UpgradeID = 0 // beam strength: solo a massive asteroid
	UpgradeThrust  UpgradeID = 1 // engine power
	UpgradeRange   UpgradeID = 2 // tractor reach
	UpgradeHull    UpgradeID = 3 // cargo survives rougher handling

	// Combat lines. Kept separate from the hauling lines above so a player has a real
	// choice every intermission: spend on getting rocks home faster, or on stopping the
	// other crew getting theirs home at all.
	UpgradeLaser     UpgradeID = 4 // damage per shot
	UpgradeCapacitor UpgradeID = 5 // more shots per magazine
	UpgradeRecharger UpgradeID = 6 // magazine refills faster

	// Station lines. These bolt onto the team's mothership instead of the buyer's ship —
	// see Team.Station for why they are shared. They exist because a destructible station
	// needs an answer: without them a crew that decides to siege you simply does, and the
	// only counterplay is to abandon the belt and fly home.
	UpgradeShields UpgradeID = 7 // the station takes less damage
	UpgradeTurrets UpgradeID = 8 // the station shoots back
)

// IsStation reports whether an upgrade modifies the team's mothership rather than the
// buyer's own ship. Station upgrades are bought with personal credits but tracked on the
// team, so a level bought by one crewmate is a level for everybody.
func (u UpgradeID) IsStation() bool {
	return u == UpgradeShields || u == UpgradeTurrets
}

// UpgradeSpec describes one purchasable line.
type UpgradeSpec struct {
	ID       UpgradeID
	Name     string
	Desc     string
	MaxLevel int

	// BaseCost is level 1; each further level costs BaseCost * (level * CostGrowth).
	BaseCost   float32
	CostGrowth float32
}

// UpgradeSpecs is the shop, ordered by id.
var UpgradeSpecs = []UpgradeSpec{
	{
		ID: UpgradeTractor, Name: "Tractor Amplifier",
		Desc:     "Your beam counts as two. Solo a massive asteroid.",
		MaxLevel: 1, BaseCost: 1200, CostGrowth: 1,
	},
	{
		ID: UpgradeThrust, Name: "Engine Boost",
		Desc:     "+25% thrust per level. Shorter round trips.",
		MaxLevel: 3, BaseCost: 400, CostGrowth: 1.6,
	},
	{
		ID: UpgradeRange, Name: "Beam Extender",
		Desc:     "+8 m tractor reach per level. Grab without closing in.",
		MaxLevel: 3, BaseCost: 350, CostGrowth: 1.5,
	},
	{
		ID: UpgradeHull, Name: "Cargo Dampeners",
		Desc:     "Cargo you carry takes 35% less impact damage per level.",
		MaxLevel: 2, BaseCost: 500, CostGrowth: 1.8,
	},
	{
		ID: UpgradeLaser, Name: "Laser Focuser",
		Desc:     "+40% laser damage per level. Six hits to a kill, not eight.",
		MaxLevel: 3, BaseCost: 450, CostGrowth: 1.7,
	},
	{
		ID: UpgradeCapacitor, Name: "Capacitor Bank",
		Desc:     "+2 shots per charge. Win the longer exchange.",
		MaxLevel: 3, BaseCost: 400, CostGrowth: 1.6,
	},
	{
		ID: UpgradeRecharger, Name: "Fast Recharger",
		Desc:     "Charge refills 20% quicker per level.",
		MaxLevel: 3, BaseCost: 380, CostGrowth: 1.6,
	},

	// Priced well above the personal lines. A station upgrade is bought once for the
	// whole crew and lasts the match, so it competes with two rounds of your own gear —
	// which is the decision worth having, not a reflex purchase.
	{
		ID: UpgradeShields, Name: "Station Shields",
		Desc:     "TEAM: your mothership takes 20% less damage per level.",
		MaxLevel: 3, BaseCost: 900, CostGrowth: 1.7,
	},
	{
		ID: UpgradeTurrets, Name: "Station Turrets",
		Desc:     "TEAM: your mothership shoots back. One turret per level.",
		MaxLevel: 3, BaseCost: 1000, CostGrowth: 1.6,
	},
}

// Spec returns the shop entry for an upgrade.
func (u UpgradeID) Spec() (UpgradeSpec, bool) {
	if int(u) < len(UpgradeSpecs) {
		return UpgradeSpecs[u], true
	}
	return UpgradeSpec{}, false
}

// CostFor is the price of taking an upgrade from `have` to `have+1`.
// Returns false if it is already maxed.
func CostFor(u UpgradeID, have int) (float32, bool) {
	spec, ok := u.Spec()
	if !ok || have >= spec.MaxLevel {
		return 0, false
	}
	cost := spec.BaseCost
	for i := 0; i < have; i++ {
		cost *= spec.CostGrowth
	}
	return cost, true
}

/*============================================================================
 * Applying upgrades
 *
 * Kept as small accessors rather than mutating Tuning, because Tuning is global and
 * shared by every player — an upgrade must affect one ship, not the whole match.
 *===========================================================================*/

// ThrustMultiplier is the engine scaling from upgrades.
func (p *Player) ThrustMultiplier() float32 {
	m := 1.0 + 0.25*float32(p.Upgrades[UpgradeThrust])
	// The speed item multiplies on top of the engine upgrade rather than replacing it,
	// so it is worth the same proportion to everyone who picks it up.
	if p.Effects.Speed > 0 {
		m *= BoostScale
	}
	return m
}

// GrabRangeBonus is extra tractor reach in metres.
func (p *Player) GrabRangeBonus() float32 {
	return 8.0 * float32(p.Upgrades[UpgradeRange])
}

// CargoDamageScale is how much impact damage this player's cargo actually takes.
func (p *Player) CargoDamageScale() float32 {
	scale := float32(1)
	for i := 0; i < p.Upgrades[UpgradeHull]; i++ {
		scale *= 0.65
	}
	return scale
}

// EffectiveBeamStrength is how many beams this player counts as.
func (p *Player) EffectiveBeamStrength() int {
	if p.Upgrades[UpgradeTractor] > 0 {
		return 2
	}
	return 1
}

// OffersPerIntermission is how many upgrades a player is offered between rounds.
// Four fills a 2x2 grid cleanly and keeps the choice quick — the intermission is short
// and reading a full catalogue every round would eat it.
const OffersPerIntermission = 4

// Offer is one purchasable slot presented during an intermission.
type Offer struct {
	Upgrade UpgradeID
	Level   int // the level this would take the player to
	Cost    float32
}

/*============================================================================
 * Where a level lives
 *
 * Personal upgrades count up on the player; station upgrades count up on the team. Every
 * caller goes through these two so the distinction is made in exactly one place — an
 * eligibility check that disagreed with the purchase about which counter to read would
 * let a crew buy the same shield four times.
 *===========================================================================*/

// levelOf is a player's effective level in an upgrade: their own for personal lines, the
// team's for station lines.
func (s *Sim) levelOf(p *Player, u UpgradeID) int {
	if !u.IsStation() {
		return p.Upgrades[u]
	}
	t, ok := s.teams[p.Team]
	if !ok || t.Station == nil {
		return 0
	}
	return t.Station[u]
}

// grantLevel records a purchase against whichever counter owns it.
func (s *Sim) grantLevel(p *Player, u UpgradeID, level int) {
	if !u.IsStation() {
		p.Upgrades[u] = level
		p.BeamStrength = p.EffectiveBeamStrength()
		return
	}
	t, ok := s.teams[p.Team]
	if !ok {
		return
	}
	if t.Station == nil {
		t.Station = map[UpgradeID]int{}
	}
	t.Station[u] = level
}

// rollOffers picks the upgrades a player is shown this intermission.
//
// Randomised so no two intermissions look the same and players cannot beeline for the
// same build every match. Anything already maxed is excluded, and if fewer than four
// upgrades remain available the slots simply run out rather than repeating — a grid of
// duplicates would look broken.
func (s *Sim) rollOffers(p *Player) []Offer {
	var pool []UpgradeID
	for _, spec := range UpgradeSpecs {
		if _, ok := CostFor(spec.ID, s.levelOf(p, spec.ID)); ok {
			pool = append(pool, spec.ID)
		}
	}

	// Fisher-Yates over the eligible pool, using the sim's seeded RNG so a replayed
	// match with the same seed offers the same choices.
	for i := len(pool) - 1; i > 0; i-- {
		j := s.rng.Intn(i + 1)
		pool[i], pool[j] = pool[j], pool[i]
	}

	n := OffersPerIntermission
	if len(pool) < n {
		n = len(pool)
	}

	offers := make([]Offer, 0, n)
	for _, id := range pool[:n] {
		have := s.levelOf(p, id)
		cost, ok := CostFor(id, have)
		if !ok {
			continue
		}
		offers = append(offers, Offer{Upgrade: id, Level: have + 1, Cost: cost})
	}
	return offers
}

// Offers returns the current intermission's offers for a player.
func (s *Sim) Offers(id PlayerID) []Offer {
	if p, ok := s.players[id]; ok {
		return p.Offers
	}
	return nil
}

// BuyOffer purchases the upgrade in one of the player's offer slots.
//
// Buying by slot rather than by upgrade id is what makes the offers meaningful: a
// client cannot ask for something it was not shown.
func (s *Sim) BuyOffer(id PlayerID, slot int) (bool, string) {
	p, ok := s.players[id]
	if !ok {
		return false, "no such player"
	}
	if s.match.Phase != PhaseIntermission {
		return false, "the shop is closed"
	}
	if slot < 0 || slot >= len(p.Offers) {
		return false, "no such offer"
	}

	offer := p.Offers[slot]
	if p.Credits < offer.Cost {
		return false, "not enough credits"
	}

	// Re-read the level at purchase time rather than trusting the offer: a station line
	// can be bought by a teammate between the roll and the click, and charging the offer's
	// stale price would sell a level that no longer exists.
	have := s.levelOf(p, offer.Upgrade)
	cost, ok := CostFor(offer.Upgrade, have)
	if !ok {
		return false, "already maxed"
	}
	if p.Credits < cost {
		return false, "not enough credits"
	}

	p.Credits -= cost
	s.grantLevel(p, offer.Upgrade, have+1)

	// Spent slots are removed rather than left greyed out, so the grid always shows
	// what is still actually buyable.
	p.Offers = append(p.Offers[:slot], p.Offers[slot+1:]...)

	return true, ""
}

// Buy attempts a purchase. Returns the new level and whether it succeeded.
//
// Rejects anything outside the intermission: buying mid-round would let a player
// upgrade out of a bad situation, and the whole point of the between-round shop is
// that you commit to a loadout and live with it.
func (s *Sim) Buy(id PlayerID, u UpgradeID) (int, bool) {
	p, ok := s.players[id]
	if !ok {
		return 0, false
	}
	if s.match.Phase != PhaseIntermission {
		return p.Upgrades[u], false
	}

	have := s.levelOf(p, u)
	cost, ok := CostFor(u, have)
	if !ok || p.Credits < cost {
		return have, false
	}

	p.Credits -= cost
	s.grantLevel(p, u, have+1)

	return have + 1, true
}
