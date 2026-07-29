package sim

import "asteroidsalvage/physics"

// The mothership as a target.
//
// Until now a station was pure scenery: a place to bank cargo and the thing the green
// arrow pointed at. Making it destructible turns the map into something you can attack
// rather than merely race across, and it gives a losing team a second way to win — you
// cannot out-haul a crew that is ahead by a thousand, but you can go and break their
// drop-off while they are out in the belt.
//
// A siege is fast and decisive:
//
//	500 health at 4 damage a shot is 125 hits. One stock pilot firing continuously
//	(1.2 shots a second, ~4.8 dps) breaks a station in about a hundred seconds, so a
//	single determined player really can end a round alone. A four-player crew with
//	maxed lasers puts out roughly 165 dps and does it in a handful of seconds.
//
// That makes attacking a station the strongest play in the game rather than a last
// resort, and it means a crew that flies out together and leaves home empty is taking a
// real risk. The two station upgrades are the counterplay: shields roughly double the
// time to breach, and turrets punish anyone who comes alone.

const (
	// MothershipMaxHealth is a station's hull pool. Deliberately small relative to the
	// nine-minute pool it started at — a siege you cannot finish is a siege nobody tries.
	MothershipMaxHealth = 500.0

	// TurretRange is how far a station's guns reach. Comfortably outside the deposit
	// radius (60 m) so an attacker cannot sit at the mouth of the hangar unmolested, and
	// well short of the belt so turrets never plink at someone minding their own
	// business — you have to actually come for the station to be shot at.
	TurretRange = 220.0

	// TurretDamage is per turret shot. Under a ship's 30-point hull by enough that a
	// station alone kills slowly: unsupported turrets are a deterrent and a warning, not
	// a wall. Ten hits to kill an intruder gives a competent pilot time to leave.
	TurretDamage = 3.0

	// TurretCooldownTicks paces one turret, in ticks. At 30 Hz this is a shot every 1.2
	// seconds; with three turrets a station puts out 7.5 damage a second, which meets an
	// attacking crew's incoming fire without ever matching it.
	TurretCooldownTicks = 36

	// turretStaggerTicks offsets each turret on a station so they do not all discharge on
	// the same tick. Three simultaneous beams read as one thick flash; three spaced out
	// read as a station working.
	turretStaggerTicks = TurretCooldownTicks / 3

	// ShieldDamageScale is the multiplier per shield level, applied per level. Three
	// levels leave about half of incoming damage, which doubles the time to breach
	// without ever making a station immune — an upgrade that ends the attack outright
	// would remove the mechanic rather than balance it.
	ShieldDamageScale = 0.8
)

// MothershipEntity is a team's station, or 0 if it has none.
func (s *Sim) MothershipEntity(team uint8) physics.EntityID { return s.motherships[team] }

// StationLevel is a team's level in one of the shared station upgrades.
func (s *Sim) StationLevel(team uint8, u UpgradeID) int {
	t, ok := s.teams[team]
	if !ok || t.Station == nil {
		return 0
	}
	return t.Station[u]
}

// ShieldScale is the fraction of incoming damage a team's station actually takes.
func (s *Sim) ShieldScale(team uint8) float32 {
	scale := float32(1)
	for i := 0; i < s.StationLevel(team, UpgradeShields); i++ {
		scale *= ShieldDamageScale
	}
	return scale
}

// MothershipHealthFraction is what the HUD draws, 0-1. Missing stations read as gone
// rather than as pristine.
func (s *Sim) MothershipHealthFraction(team uint8) float32 {
	e, ok := s.motherships[team]
	if !ok {
		return 0
	}
	o, ok := s.objects[e]
	if !ok || o.MaxHealth <= 0 {
		return 0
	}
	return o.Health / o.MaxHealth
}

// MothershipDestroyed reports whether a team's station has been breached.
func (s *Sim) MothershipDestroyed(team uint8) bool {
	e, ok := s.motherships[team]
	if !ok {
		return true
	}
	o, ok := s.objects[e]
	return ok && o.Health <= 0
}

// damageMothership applies laser damage to a station, and ends the round if it breaks.
//
// Shields are applied here rather than at the shooter so that every source of damage —
// a player's laser today, anything added later — pays for them automatically.
func (s *Sim) damageMothership(shooter *Player, o *Object, damage float32) {
	if o.Health <= 0 {
		return // already breached; nothing left to shoot
	}
	if !s.stationsVulnerable() {
		return
	}

	scale := s.ShieldScale(o.Team)
	applied := damage * scale
	o.Health -= applied
	if o.Health < 0 {
		o.Health = 0
	}

	// A shielded hit reads differently to an unshielded one, so the client can flare the
	// bubble instead of sparking the hull. Sent as a separate event rather than a flag on
	// the hit so a client that does not know about shields still draws something sane.
	if scale < 1 {
		s.emit(Event{
			Type: EventShieldAbsorbed, Entity: o.Entity,
			Player: shooter.ID, Value: damage - applied,
		})
	}

	s.emit(Event{
		Type: EventMothershipHit, Entity: o.Entity,
		Player: shooter.ID, Value: applied,
	})

	if o.Health <= 0 {
		s.breachMothership(o, shooter)
	}
}

// stationsVulnerable reports whether motherships can be damaged at all in this mode.
//
// They cannot in King of the Hill. That mode is decided by holding ground and nothing
// else, and a station that could be broken would hand a crew a second, unrelated way to
// end a round — which is exactly the thing the mode is not about.
func (s *Sim) stationsVulnerable() bool {
	return s.matchCfg.Mode != ModeKingOfTheHill
}

// breachMothership knocks a crew out of the round when their station comes apart.
//
// It used to end the round outright and award it to the attackers. It does not any more:
// losing your base eliminates *you*, and everyone else carries on. With four crews on a
// map, one siege ending everybody's round — including two teams who had nothing to do
// with it — made the other two rounds of the fight irrelevant.
//
// The round then ends the same way a wipe-out does, once only one crew is left.
func (s *Sim) breachMothership(o *Object, shooter *Player) {
	s.emit(Event{
		Type: EventMothershipDestroyed, Entity: o.Entity,
		Player: shooter.ID, Value: float32(o.Team),
	})

	// Everything nearby gets thrown clear, including whoever was pressing the attack.
	if st, ok := s.states[o.Entity]; ok {
		s.blast(st.Pos, o.Radius*4, DeathBlastForce*3)
	}

	// Out of the round: no drop-off, no respawns, and every ship still flying goes with
	// the station. Nothing to bank and nothing to come back in.
	s.eliminateTeam(o.Team)
	s.checkElimination()
}

/*============================================================================
 * Turrets
 *===========================================================================*/

// updateTurrets fires every station's guns for one tick. Called from Step alongside the
// player weapons, so a turret shot lands in the same shot list and reaches clients
// through the machinery that already exists.
func (s *Sim) updateTurrets() {
	if !s.scoringActive() {
		return // stations hold fire outside a live round, same as players
	}

	for team, e := range s.motherships {
		count := s.StationLevel(team, UpgradeTurrets)
		if count <= 0 {
			continue
		}
		o, ok := s.objects[e]
		if !ok || o.Health <= 0 {
			continue // a breached station's guns are gone with it
		}
		st, ok := s.states[e]
		if !ok {
			continue
		}

		for i := 0; i < count; i++ {
			// Each turret runs its own clock, offset so they alternate rather than
			// volley. Keyed off the global tick because turrets have no state of their
			// own worth storing — they are a property of the station, not entities.
			if (int(s.tick)+i*turretStaggerTicks)%TurretCooldownTicks != 0 {
				continue
			}
			s.fireTurret(team, e, st.Pos, o.Radius)
		}
	}
}

// fireTurret picks the nearest enemy ship in range and shoots it.
func (s *Sim) fireTurret(team uint8, station physics.EntityID, pos physics.Vec3, radius float32) {
	target, dist := s.nearestEnemyShip(team, pos)
	if target == 0 {
		return
	}

	if s.playerByShip(target) == nil {
		return
	}
	st, ok := s.states[target]
	if !ok {
		return
	}
	_ = dist

	// Turrets fire the same travelling rounds a ship does, so they have to lead a moving
	// target and raise their aim for the drop — which also means a fast crosser can now
	// beat a station's guns, as it should be able to.
	centre, ok := s.states[station]
	if !ok {
		return
	}
	dir := AimBolt(centre.Pos, st.Pos, st.Vel)

	// Launched from the hull rather than the middle of the station, so a round looks like
	// it came from the surface instead of erupting out of the core.
	muzzle := centre.Pos.Add(dir.Scale(radius + 2))
	s.fireStationBolt(team, station, muzzle, dir, TurretDamage)
	s.shots = append(s.shots, Shot{Shooter: station, Team: team})
}

// nearestEnemyShip finds the closest living ship not on `team` within TurretRange.
func (s *Sim) nearestEnemyShip(team uint8, from physics.Vec3) (physics.EntityID, float32) {
	var best physics.EntityID
	bestDist := float32(TurretRange)

	for e, o := range s.objects {
		if o.Kind != KindShip || o.Team == team {
			continue
		}
		if pl := s.playerByShip(e); pl == nil || pl.Dead() {
			continue
		}
		st, ok := s.states[e]
		if !ok {
			continue
		}
		if d := st.Pos.Sub(from).Len(); d < bestDist {
			best, bestDist = e, d
		}
	}
	return best, bestDist
}

// destroyShipByStation is destroyShip for a kill with no player behind it.
//
// The kill credit goes to the victim's own team id, which the client reads as "a station
// got you" — there is no killer to name, and inventing one would put a teammate's name on
// a kill they did not make.
func (s *Sim) destroyShipByStation(victim *Player, byTeam uint8) {
	if victim.Held != 0 {
		s.release(victim, EventDropped)
	}

	s.startRespawn(victim)
	if o, ok := s.objects[victim.Ship]; ok {
		o.Health = 0
	}
	if st, ok := s.states[victim.Ship]; ok {
		s.blast(st.Pos, DeathBlastRadius, DeathBlastForce)
	}

	s.world.SetVelocity(victim.Ship, physics.Vec3{})
	s.world.SetPosition(victim.Ship, physics.Vec3{X: 0, Y: 0, Z: -100000})

	s.emit(Event{
		Type: EventShipDestroyed, Entity: victim.Ship,
		Value: float32(victim.Team),
	})

	s.checkElimination()
}

// restoreMotherships puts every station back to full between rounds.
//
// Without this, breaching a station in round one would win that round and then hand the
// attackers a free second one against a wreck. Each round is its own contest — the same
// reason team scores reset.
func (s *Sim) restoreMotherships() {
	for _, e := range s.motherships {
		if o, ok := s.objects[e]; ok {
			o.Health = MothershipMaxHealth
			o.MaxHealth = MothershipMaxHealth
		}
	}
}
