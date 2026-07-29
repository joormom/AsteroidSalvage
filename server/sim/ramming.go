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
