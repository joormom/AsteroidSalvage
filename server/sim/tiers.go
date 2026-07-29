package sim

// Asteroid tiers.
//
// Each tier is a distinct read at a glance: a colour, a size band, and a payout. The
// point is that a player scanning the field can tell "that blue one is worth the detour"
// and "that dark red one is a two-person job" without reading any UI.
//
// Tier is a wire value carried in every snapshot body record (see shared/protocol.md),
// so the client can colour rocks without duplicating the balance table. Values here are
// the single source of truth for spawning and scoring.

type Tier uint8

const (
	TierRubble   Tier = 0 // grey, tiny, near worthless — clutter that fills the field
	TierIron     Tier = 1 // rust brown, the bread-and-butter haul
	TierCrystal  Tier = 2 // cyan, small and light but pays well
	TierGold     Tier = 3 // yellow, heavy for its size, pays very well
	TierMassive  Tier = 4 // dark red, enormous — needs two beams or an upgrade
	TierColossal Tier = 5 // pale grey-violet, far too big to tow: shoot it apart

	// TierCore never spawns on its own. It is what is *inside* the big rocks: a small,
	// light, blindingly valuable seam that only exists once something enormous has been
	// broken open. Finding one is the payoff for spending a magazine on a titan.
	TierCore Tier = 6

	// TierTitan is the biggest thing in the belt, and appears only in later rounds. A
	// stock laser will not get through one in any sensible time — it is the reason to
	// buy weapon upgrades, and the reason to bring the whole crew.
	TierTitan Tier = 7
)

// TierSpec describes one class of asteroid.
type TierSpec struct {
	Tier Tier
	Name string

	MinRadius float32
	MaxRadius float32

	// Density multiplier applied to radius^3. Gold is dense for its size; crystal is
	// light, which is what makes it worth chasing despite the small payout per rock.
	Density float32

	// Value per square metre of cross-section, so bigger rocks in a tier pay more.
	ValuePerArea float32

	// SpawnWeight is the relative frequency of this tier in a wave. Zero means it never
	// spawns naturally and can only appear by breaking something larger open.
	SpawnWeight float32

	// Toughness scales how much laser damage the rock absorbs, per square metre of
	// cross-section. Zero means it cannot be shot apart.
	Toughness float32

	// SplitsInto is the tier the fragments become when it is shot apart, and how many
	// there are. A tier that does not split leaves SplitCount at zero.
	SplitsInto Tier
	SplitCount int

	// CoreTier and CoreCount are the valuable seam buried inside. Breaking a big rock
	// open yields these on top of the ordinary fragments, which is what makes shooting
	// one worth the energy rather than just clearing an obstacle.
	CoreTier  Tier
	CoreCount int

	// Towable is false for rocks no tractor beam can move at all.
	Towable bool

	// MinEra gates this tier behind match progress: the round number in a real match,
	// or the wave number in sandbox. The biggest rocks are meant to show up once crews
	// have had an intermission or two to buy the guns for them.
	MinEra int
}

// TierSpecs is the balance table. Ordered by tier id.
//
// Toughness rises with tier so shooting a big rock apart is a real investment of
// energy, and the split chain (titan -> colossal -> massive -> gold) means the biggest
// rocks are a source of good cargo rather than just an obstacle.
//
// Sustained damage sets the scale here. A stock laser lands 25 damage at 1.2 shots per
// second (~30 dps); a fully upgraded one lands 55 at ~4.7 (~258 dps). So a rock's
// health, divided by 30, is roughly how many seconds one unequipped pilot needs — and
// an eighth of that is what a maxed-out one needs.
var TierSpecs = []TierSpec{
	{TierRubble, "rubble", 1.0, 1.8, 0.7, 8, 30, 3, 0, 0, 0, 0, true, 0},
	{TierIron, "iron", 2.2, 4.0, 0.9, 22, 38, 5, 0, 0, 0, 0, true, 0},
	{TierCrystal, "crystal", 1.4, 2.4, 0.35, 90, 15, 3, 0, 0, 0, 0, true, 0},
	{TierGold, "gold", 2.0, 3.2, 2.6, 130, 9, 7, 0, 0, 0, 0, true, 0},

	// Grown from 6-8.5 m, with the density dropped from 1.6 to compensate. A massive is
	// meant to look like a two-person job, and it now does — but it is still a two-beam
	// haul rather than an immovable one, because scaling mass with the new volume would
	// have quadrupled it and turned the crew haul into a chore. Bigger, not heavier.
	{TierMassive, "massive", 7.0, 11.0, 1.0, 40, 5, 10, TierGold, 3, 0, 0, true, 0},

	// Too big to tow at any beam strength: the only way to get value out of one is to
	// break it up and haul the pieces. Roughly half a minute of stock fire.
	{TierColossal, "colossal", 18.0, 26.0, 1.0, 30, 3, 2.5, TierMassive, 3, TierCore, 1, false, 2},

	// Never spawns; only ever cut out of something bigger. Small, light and worth more
	// per square metre than anything else in the game by a wide margin.
	//
	// Tough for its size on purpose: a rival can shoot a core out from under you and
	// deny the payout, but it costs them most of a magazine to do it, so it is a
	// decision rather than a reflex.
	{TierCore, "CORE", 2.0, 3.2, 0.3, 1200, 0, 30, 0, 0, 0, 0, true, 0},

	// The biggest thing in the game, and deliberately larger than a mothership: 40-60 m
	// against the station's 30 m hull, so a titan on the horizon reads as terrain rather
	// than as another rock. Density is low for its size — it is a rubble pile, not a
	// cannonball — but at 40 m it still outweighs a ship by a factor of ten thousand.
	//
	// Health runs 4800-10800, so several minutes of stock fire and 19-42 seconds with
	// the best gun in the shop. Meant to be a crew job in a late round.
	{TierTitan, "TITAN", 40.0, 60.0, 0.5, 20, 2, 3, TierColossal, 4, TierCore, 3, false, 3},
}

// Spec returns the balance entry for a tier.
func (t Tier) Spec() TierSpec {
	if int(t) < len(TierSpecs) {
		return TierSpecs[t]
	}
	return TierSpecs[TierIron]
}

// MassFor computes an asteroid's mass from its tier and radius.
func MassFor(t Tier, radius float32) float32 {
	return radius * radius * radius * t.Spec().Density
}

// ValueFor computes an asteroid's undamaged payout.
func ValueFor(t Tier, radius float32) float32 {
	return radius * radius * t.Spec().ValuePerArea
}

// toughnessBaseline is the per-shot laser damage the Toughness column above was written
// against. Ship health and laser damage were later rescaled together (100/25 down to
// 30/4) so that a mothership could have a legible 2500-point pool; rocks had no reason to
// change, and rewriting every Toughness value would have silently invalidated the
// "seconds of stock fire" figures the table is documented in.
const toughnessBaseline = 25.0

// toughnessScale converts the table's units into whatever the current damage scale is,
// so shots-to-break is exactly what it was before. Tuning a rock means editing its
// Toughness; tuning the guns cannot accidentally retune every rock in the game.
const toughnessScale = LaserDamage / toughnessBaseline

// HealthFor is how much laser damage a rock absorbs before breaking.
func HealthFor(t Tier, radius float32) float32 {
	return radius * radius * t.Spec().Toughness * toughnessScale
}

// pickTier chooses a tier by spawn weight, from those unlocked at the given era.
//
// Zero-weight tiers are skipped outright rather than left to the accumulator: a core
// seam must never appear loose in the belt, and "weight 0 but the roll landed exactly on
// the boundary" is precisely the kind of one-in-a-million bug that only shows up in
// someone's match.
func pickTier(roll float32, era int) Tier {
	var total float32
	for _, s := range TierSpecs {
		if s.SpawnWeight <= 0 || era < s.MinEra {
			continue
		}
		total += s.SpawnWeight
	}
	if total <= 0 {
		return TierIron
	}

	target := roll * total
	var acc float32
	for _, s := range TierSpecs {
		if s.SpawnWeight <= 0 || era < s.MinEra {
			continue
		}
		acc += s.SpawnWeight
		if target <= acc {
			return s.Tier
		}
	}
	return TierIron
}
