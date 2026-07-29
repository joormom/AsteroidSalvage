package sim

import (
	"math"

	"asteroidsalvage/physics"
)

// The grab mechanic.
//
// The held object is never parented to the ship. Instead a spring-damper pulls it
// toward a hold point in front of the ship, and — critically — the equal-and-opposite
// force is applied back to the ship. That reaction is what makes a heavy asteroid feel
// heavy: your ship becomes sluggish, overshoots turns, and fights you on the way home.
// Parenting the object would make hauling a 5-tonne rock feel identical to hauling a
// pebble, which is the single most important thing to get right here.
//
// Every constant below is a live tunable (see tuning.go); none of these numbers should
// be trusted until they have been played with via the dev console.

// grabConeDot is how far off-axis a target may be: dot(toTarget, forward) must exceed
// this. 0.85 is roughly a 32 degree cone.
//
// Cone width has to track beam range. A tight 20 degree cone suited a 70 m beam, where
// a wide one would hoover up rocks the player never aimed at; at close quarters that
// same cone is fiddly, because a rock a few metres away subtends a large angle and
// drifts out of a narrow cone as you manoeuvre.
const grabConeDot = 0.85

// breakDistanceFactor drops the object once it lags more than this multiple of the hold
// distance behind the hold point. Expressing the break condition as distance rather
// than force makes it legible to the player: the rock visibly falls behind, then goes.
const breakDistanceFactor = 2.5

// underpoweredBeamScale is how much pull a beam manages against a rock too heavy for it.
//
// Zero, and it has to be. An earlier version applied a token 12% so the cargo would
// visibly strain — but space has no friction, so *any* non-zero force accelerates the
// rock indefinitely. A patient solo pilot still hauled a massive asteroid home, just
// slowly, which defeats the entire point of the tier. "Too heavy to move alone" only
// means anything if the force is actually zero.
//
// Feedback comes from the client instead: the beam visibly connects, and the HUD says
// the rock needs another beam.
const underpoweredBeamScale = 0.0

// beamStrengthOn totals the tractor strength currently applied to an object. Two stock
// pilots and one upgraded pilot both come to 2.
func (s *Sim) beamStrengthOn(o *Object) int {
	total := 0
	for _, id := range o.Holders {
		if p, ok := s.players[id]; ok {
			strength := p.BeamStrength
			if strength < 1 {
				strength = 1
			}
			total += strength
		}
	}
	return total
}

// breakCooldownTicks is how long cargo stays un-grabbable after it breaks free.
// Half a second: long enough that overreaching hurts, short enough not to feel broken.
const breakCooldownTicks = TickHz / 2

// updateGrab runs one tick of grab logic for a player: acquire, hold, or release.
// Hold-to-carry — the object is held only while the grab input is down.
func (s *Sim) updateGrab(p *Player, tune *Tuning) {
	if p.grabCooldown > 0 {
		p.grabCooldown--
	}

	ship, ok := s.states[p.Ship]
	if !ok {
		return
	}

	if !p.input.Grab {
		if p.Held != 0 {
			s.release(p, EventDropped)
		}
		return
	}

	// Acquire whenever the button is down and our hands are empty.
	//
	// This deliberately does NOT require a fresh button press. An earlier version did,
	// which meant a player flying toward a rock with grab already held could never pick
	// it up — they had to release and re-press at exactly the right moment. Holding the
	// button and scooping up whatever you fly into is what players actually expect.
	if p.Held == 0 && p.grabCooldown == 0 {
		if target := s.findGrabTarget(p, ship, tune); target != 0 {
			s.attach(p, target)
		}
	}

	if p.Held == 0 {
		return
	}

	held, ok := s.states[p.Held]
	if !ok {
		// The object was despawned underneath us (deposited, or removed).
		p.Held = 0
		return
	}

	s.applyHoldForce(p, ship, held, tune)
}

// findGrabTarget picks whatever the player is pointing at, within beam range.
//
// Selection is by aim, not proximity: among everything inside the cone, the one closest
// to the crosshair wins. Picking the nearest instead means a rock drifting past your
// shoulder steals the lock from the one you lined up on.
func (s *Sim) findGrabTarget(p *Player, ship physics.BodyState, tune *Tuning) physics.EntityID {
	fwd := ship.Rot.Forward()

	var best physics.EntityID
	bestDot := float32(grabConeDot)
	reach := tune.GrabRange + p.GrabRangeBonus()

	for e, o := range s.objects {
		if o.Kind != KindAsteroid && o.Kind != KindSalvage {
			continue
		}
		// The biggest rocks are not towable at any beam strength — the only way to get
		// value out of one is to shoot it apart and haul the fragments. Refusing the
		// lock is clearer than letting the beam attach and then do nothing.
		if !o.Tier.Spec().Towable {
			continue
		}
		// Deliberately NOT skipping objects someone else is holding: a second beam on
		// the same rock is how a crew moves a massive one.
		if o.HeldByPlayer(p.ID) {
			continue
		}

		st, ok := s.states[e]
		if !ok {
			continue
		}

		toObj := st.Pos.Sub(ship.Pos)
		dist := toObj.Len()
		// Bigger rocks present a bigger target, so range extends by their radius rather
		// than punishing you for lining up on one.
		if dist > reach+o.Radius || dist < 1e-4 {
			continue
		}

		dot := toObj.Scale(1 / dist).Dot(fwd)
		if dot > bestDot {
			best, bestDot = e, dot
		}
	}

	return best
}

func (s *Sim) attach(p *Player, e physics.EntityID) {
	o, ok := s.objects[e]
	if !ok {
		return
	}
	o.addHolder(p.ID)
	p.Held = e

	// Start the slack at the lock-on range so reeling in from a distance is allowed.
	if st, ok := s.states[e]; ok {
		if ship, ok := s.states[p.Ship]; ok {
			p.holdSlack = st.Pos.DistTo(ship.Pos)
		}
	}

	// A held object must never sleep, or it would freeze mid-haul.
	s.world.SetSleepAllowed(e, false)

	s.emit(Event{Type: EventGrabbed, Entity: e, Player: p.ID})
}

// release drops whatever the player is holding.
func (s *Sim) release(p *Player, why EventType) {
	e := p.Held
	if e == 0 {
		return
	}
	if o, ok := s.objects[e]; ok {
		o.removeHolder(p.ID)
		// Only allow sleep once the last beam is off it.
		if !o.IsHeld() {
			s.world.SetSleepAllowed(e, true)
		}
	}
	p.Held = 0

	s.emit(Event{Type: why, Entity: e, Player: p.ID})
}

// holdDistanceFor keeps the cargo clear of the hull.
//
// A fixed hold distance means a big rock is held *inside* the ship: at 6 m with a 5.5 m
// radius asteroid, its surface sits half a metre off the hull and every burst of thrust
// rams the ship into its own cargo. Offsetting by both radii keeps a constant gap
// regardless of what is being carried.
func (s *Sim) holdDistanceFor(e physics.EntityID, tune *Tuning) float32 {
	d := tune.GrabHoldDistance + ShipRadius
	if o, ok := s.objects[e]; ok {
		d += o.Radius
	}
	return d
}

// applyHoldForce is the spring-damper constraint, and the reaction that sells the mass.
func (s *Sim) applyHoldForce(p *Player, ship, held physics.BodyState, tune *Tuning) {
	holdDist := s.holdDistanceFor(p.Held, tune)
	holdPoint := ship.Pos.Add(ship.Rot.Forward().Scale(holdDist))
	delta := holdPoint.Sub(held.Pos)
	gap := delta.Len()

	// Ratchet the allowance down as the cargo closes, so a long-range lock is tolerated
	// on the way in but tightens to the normal rules once it is stowed.
	if gap < p.holdSlack {
		p.holdSlack = gap
	}

	limit := holdDist * breakDistanceFactor
	if slack := p.holdSlack * 1.35; slack > limit {
		limit = slack
	}

	// Yank it too hard and you lose it. Checked before applying force so the drop
	// happens on the tick the player overreached, not one later.
	if gap > limit {
		s.release(p, EventDropped)
		// Unlike letting go deliberately, losing cargo locks you out briefly — you have
		// to come back around for it.
		p.grabCooldown = breakCooldownTicks
		return
	}

	// Damp against velocity *relative to the ship*, not world velocity — otherwise the
	// constraint fights the ship's own motion and hauling feels like dragging an anchor
	// through treacle even when moving in a straight line.
	relVel := held.Vel.Sub(ship.Vel)

	// Damping scales with sqrt(mass), which makes the damping *ratio* independent of
	// cargo mass: for m·a = k·x - c·sqrt(m)·v the ratio is c/(2·sqrt(k)) with the mass
	// cancelling out. So one tuning value critically damps a 3 kg pebble and a 150 kg
	// boulder alike, and 2·sqrt(grab.spring) is the critical value to tune around.
	//
	// A flat coefficient left the system badly underdamped: reeling a rock in from
	// across the beam's range accelerated it into the hull hard enough to destroy its
	// value before it got home — deliveries were banking zero credits.
	damping := tune.GrabDamping * float32(math.Sqrt(float64(held.Mass)))

	force := delta.Scale(tune.GrabSpring).Sub(relVel.Scale(damping))
	force, _ = force.ClampLen(tune.GrabMaxForce)

	// Massive rocks need more combined beam strength than one pilot has. Under-strength
	// beams still attach and still strain — the rock shudders and refuses to follow —
	// which is far more legible than silently failing to grab. Bring a teammate or buy
	// the tractor upgrade.
	if o, ok := s.objects[p.Held]; ok && o.RequiredBeams > 1 {
		if s.beamStrengthOn(o) < o.RequiredBeams {
			force = force.Scale(underpoweredBeamScale)
		}
	}

	s.addForce(p.Held, force, physics.Vec3{})

	// Newton's third law, with a tuning knob. Physically this should be 1.0, but the
	// value that *feels* right may not be the physical one — that is precisely what the
	// console slider is for.
	s.addForce(p.Ship, force.Scale(-tune.GrabReactionScale), physics.Vec3{})
}
