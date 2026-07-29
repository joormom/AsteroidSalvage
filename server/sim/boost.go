package sim

import "asteroidsalvage/physics"

// Boost.
//
// Boost used to be free — hold the key, get double thrust, forever — which on a map this
// size made the throttle a formality: there was never a reason not to be boosting. Now
// it is a tank you spend and wait for, like the weapon charge, so crossing the belt is a
// series of decisions about when the extra speed is actually worth it.
//
// The tank alone was not enough. Refilling from empty at a steady trickle meant a pilot
// who tapped the key kept a sliver of charge arriving forever, so boost was still
// permanently available in stutters — the tank changed the rhythm without ever actually
// saying no. Two rules fix that, and they are the whole design:
//
//   - Running dry stalls the tank completely for a few seconds. Nothing refills. This is
//     the cost of burning it all, and it is the part a player feels.
//   - Starting a burn needs a real amount in the tank, not a drop. Below that you are
//     holding a dead key, so tapping buys nothing that waiting would not.
//
// Together they turn an empty tank into a genuine pause rather than a brief inconvenience.

const (
	// BoostMaxSeconds is how long the tank lasts at full burn.
	BoostMaxSeconds = 4.0

	// BoostRefillSecs is how long an empty tank takes to come back, once it starts.
	BoostRefillSecs = 7.0

	// BoostThrustScale is the multiplier while boosting.
	BoostThrustScale = 2.0

	// BoostEmptyCooldownSecs is the dead time after burning the tank all the way down.
	// The tank does not refill at all while this runs — it is the pause that stops boost
	// from being permanently available in stutters.
	BoostEmptyCooldownSecs = 3.0

	// BoostReengageSeconds is the minimum charge needed to *start* a burn, as opposed to
	// continuing one already running. Without a floor here the tank refills into
	// immediately-spendable scraps and tapping the key beats managing it.
	BoostReengageSeconds = 1.0

	// boostEpsilon is the point at which the tank counts as empty.
	//
	// Draining by exactly one tick's worth per tick does not land on zero: after the
	// full four seconds the float residue is a fraction of a microsecond of burn, which
	// is enough for "> 0" to re-engage the thruster for one more tick. A millisecond of
	// slack is invisible to a player and makes the bottom of the tank behave.
	boostEpsilon = 1e-3
)

// MaxBoost is the tank size. A method rather than the bare constant so an upgrade could
// raise it later without every caller changing.
func (p *Player) MaxBoost() float32 { return BoostMaxSeconds }

// Boosting reports whether this player's engines are actually running hot — which is not
// the same as holding the key, since the tank can be empty or stalled.
func (p *Player) Boosting() bool { return p.boosting }

// ShipBoosting reports whether the ship with this entity id has its engines running hot.
// Used by the snapshot encoder, which works in entities rather than players.
func (s *Sim) ShipBoosting(e physics.EntityID) bool {
	p := s.playerByShip(e)
	return p != nil && p.Boosting()
}

// BoostCooldown is the seconds of forced pause left after running the tank dry, or 0.
// The HUD shows this instead of a fill level, because during it the bar is not filling.
func (p *Player) BoostCooldown() float32 { return p.boostCooldown }

// BoostReady reports whether a burn could be started right now. Drives the HUD's
// distinction between "charging" and "good to go" — a bar that is 10% full and a bar that
// is 10% full *and usable* are different states and need to look different.
func (p *Player) BoostReady() bool {
	return p.boostCooldown <= 0 && p.Boost >= BoostReengageSeconds
}

// updateBoost spends or refills the tank for one tick.
//
// Called for every player including the dead, so a destroyed ship respawns with
// something in it rather than stranded on empty.
func (s *Sim) updateBoost(p *Player) {
	// Releasing the key always clears the lockout, whatever else is going on.
	if !p.input.Boost {
		p.boostLocked = false
	}

	// The post-empty stall. Nothing refills and nothing engages until it expires; that
	// dead bar is the feedback, so it deliberately runs even for a player who is dead or
	// has already let go of the key.
	if p.boostCooldown > 0 {
		p.boostCooldown -= TickDuration
		if p.boostCooldown < 0 {
			p.boostCooldown = 0
		}
		p.boosting = false
		return
	}

	// Continuing a burn only needs fuel; starting one needs a usable amount of it.
	threshold := float32(boostEpsilon)
	if !p.boosting {
		threshold = BoostReengageSeconds
	}
	engaged := p.input.Boost && !p.boostLocked && !p.Dead() && p.Boost >= threshold
	p.boosting = engaged

	if engaged {
		p.Boost -= TickDuration
		if p.Boost <= boostEpsilon {
			p.Boost = 0
			p.boosting = false
			p.boostCooldown = BoostEmptyCooldownSecs
			// Ran dry. Hold the key from here and nothing happens until you let go:
			// without the lockout, a held key re-engages the moment the stall ends and
			// the charge floor is met, which hides the fact that you overspent.
			p.boostLocked = true
		}
		return
	}

	if p.Boost < BoostMaxSeconds {
		p.Boost += TickDuration * (BoostMaxSeconds / BoostRefillSecs)
		if p.Boost > BoostMaxSeconds {
			p.Boost = BoostMaxSeconds
		}
	}
}
