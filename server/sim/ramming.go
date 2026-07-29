package sim

import "asteroidsalvage/physics"

// Ramming.
//
// Flying your hull into something at speed is now a weapon. It costs you the ship every
// time, which is what keeps it from replacing the laser: a ram is a trade, and with a
// finite pool of team lives it is a trade you can run out of.
//
// Two cases, and they are deliberately asymmetric:
//
//   - Ship into an enemy ship: both are destroyed. Neither pilot gets to win a collision
//     they both chose to have, and a rammer who survived would make closing to tractor
//     range suicidal against anyone with a full boost tank.
//   - Ship into an enemy station: 15 damage to the station, and the ship is gone. A
//     station has 500 health, so that is 34 ships — nobody rams a base down, but a crew
//     that is already shooting it can spend a life to skip a few seconds of fire.
//
// Friendly hulls are exempt. Friendly *fire* already is, and a mechanic where a teammate
// arriving to help on a massive rock can delete you both by misjudging a closing speed is
// not a mechanic, it is a hazard.

const (
	// RamSpeedThreshold is the closing speed a collision needs before it counts as a ram,
	// in m/s. Comfortably above the speed where cargo starts taking impact damage, so
	// nudging a rival while you both jockey for the same rock is still just a nudge —
	// ramming has to be something you did on purpose, at speed, usually on boost.
	//
	// Scaled with the engines. When ships topped out near 21 m/s a 10 m/s threshold was
	// half of full throttle; after thrust doubled, ordinary cruising cleared it and every
	// incidental bump between rivals became a double kill. The number that matters is the
	// fraction of top speed, not the absolute.
	RamSpeedThreshold = 20.0

	// RamMothershipDamage is what one ship is worth when flown into an enemy station.
	RamMothershipDamage = 15.0

	// HullImpactThreshold is the closing speed above which flying into scenery starts
	// hurting, in m/s. The same number cargo starts taking impact damage at, so a haul
	// rough enough to damage the rock is rough enough to dent the ship carrying it —
	// which is the intuition, and one threshold is easier to learn than two.
	HullImpactThreshold = 12.0

	// HullImpactScale is hull points per m/s over the threshold. One, deliberately: the
	// rule is "every metre per second over twelve costs a hull point", which a player can
	// actually hold in their head.
	//
	// It also sets where the ceiling is. A ship tops out near 43 m/s unladen, so a
	// flat-out collision is 31 points against a 30-point hull — full speed into anything
	// solid is death, and everything below it is a survivable mistake that scales.
	HullImpactScale = 1.0

	// HullImpactCooldownTicks is how long a ship is immune to further collision damage
	// after taking some.
	//
	// A collision is an event, but *contact* is a state: the physics reports a touching
	// pair every tick, so without this, holding thrust against a rock dealt damage thirty
	// times a second — a third of a hull per tick, which killed a pilot who bumped
	// something and did not immediately reverse. Half a second is long enough that a
	// scrape along a surface is one hit, short enough that two genuine collisions in
	// quick succession still both land.
	HullImpactCooldownTicks = TickHz / 2
)

// applyRamming turns fast collisions into kills. Called from Step immediately after
// applyImpactDamage, which is what refreshes lastCollisions.
func (s *Sim) applyRamming() {
	if !s.scoringActive() {
		return // no kills during warmup, the shop, or after the match
	}

	for _, c := range s.lastCollisions {
		if c.RelSpeed < RamSpeedThreshold {
			continue
		}
		s.resolveRam(c.A, c.B)
	}
}

// resolveRam handles one fast contact, whichever way round its two entities came.
//
// Both pilots are resolved *before* either is destroyed. Handling the contact one side at
// a time looked tidier and was wrong: killing the first ship made the second pass see a
// dead opponent and bail, so the pilot who was rammed walked away from a head-on collision
// that killed the other one.
func (s *Sim) resolveRam(a, b physics.EntityID) {
	pilotA, pilotB := s.livePilotOf(a), s.livePilotOf(b)

	if pilotA != nil && pilotB != nil {
		if pilotA.Team == pilotB.Team {
			return // teammates bounce
		}
		s.destroyShip(pilotA, pilotB)
		s.destroyShip(pilotB, pilotA)
		return
	}

	// Otherwise at most one end is a ship, and the other may be a station.
	if pilotA != nil {
		s.ramStation(pilotA, b)
	}
	if pilotB != nil {
		s.ramStation(pilotB, a)
	}
}

// applyHullImpacts hurts a ship that flew into something solid, in proportion to how hard.
//
// Runs after applyRamming, which owns the two contacts that are *decided* by speed rather
// than scaled by it: ship-into-ship and ship-into-enemy-station both destroy outright
// above RamSpeedThreshold. Anything the ram pass already killed is skipped here, since
// livePilotOf refuses a dead one — otherwise a rammed pilot would be charged twice.
//
// Rocks, cargo and scenery are all the same to a hull at speed, so they are all included.
// Your own station is too: it is the thing you fly at fastest and most often, and being
// the one solid object in the game you could belly-flop into for free made docking at a
// hundred metres a second the correct way to deliver.
func (s *Sim) applyHullImpacts() {
	if !s.scoringActive() {
		return // no damage during warmup, the shop, or after the match
	}

	for _, c := range s.lastCollisions {
		if c.RelSpeed <= HullImpactThreshold {
			continue
		}
		damage := (c.RelSpeed - HullImpactThreshold) * HullImpactScale

		for i, e := range [2]physics.EntityID{c.A, c.B} {
			pilot := s.livePilotOf(e)
			if pilot == nil {
				continue
			}
			// What it hit. Ship-on-ship is the ram rule's business, not this one.
			other := c.B
			if i == 1 {
				other = c.A
			}
			o, ok := s.objects[other]
			if !ok || o.Kind == KindShip {
				continue
			}
			if pilot.impactCooldown > 0 {
				continue
			}

			pilot.impactCooldown = HullImpactCooldownTicks
			s.damageHull(pilot, damage)
		}
	}
}

// damageHull applies impact damage to a pilot's own ship, shield first.
func (s *Sim) damageHull(pilot *Player, damage float32) {
	o, ok := s.objects[pilot.Ship]
	if !ok {
		return
	}

	o.Health -= pilot.absorbWithShield(damage)
	// Emitted with no shooter: the Player field is who *dealt* it, and nobody did. The
	// client draws sparks either way, which is the point — a collision that took a third
	// of your hull should look like something happened.
	s.emit(Event{Type: EventShipHit, Entity: pilot.Ship, Value: damage})

	if o.Health <= 0 {
		s.destroyShipByStation(pilot, pilot.Team)
	}
}

// livePilotOf returns the player flying this entity, or nil if it is not a living ship.
func (s *Sim) livePilotOf(e physics.EntityID) *Player {
	o, ok := s.objects[e]
	if !ok || o.Kind != KindShip {
		return nil
	}
	p := s.playerByShip(e)
	if p == nil || p.Dead() {
		return nil
	}
	return p
}

// ramStation trades a ship for a bite out of an enemy station's hull.
func (s *Sim) ramStation(pilot *Player, target physics.EntityID) {
	station, ok := s.objects[target]
	if !ok || station.Kind != KindMothership {
		return // an asteroid: ramming a rock is just a collision
	}
	if station.Team == pilot.Team || station.Health <= 0 {
		return // your own hangar, or a wreck
	}

	s.damageMothership(pilot, station, RamMothershipDamage)
	s.destroyShipByStation(pilot, station.Team)
}
