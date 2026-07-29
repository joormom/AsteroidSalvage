package sim

import (
	"math"

	"asteroidsalvage/physics"
)

// Laser combat: the trigger, the magazine and what a kill costs.
//
// The flight of a round lives in projectiles.go. This file used to resolve shots by
// raycast on the tick they were fired; bolts travel and fall now, so all that survives
// here is the decision to fire, the energy it costs, and what happens when something dies.
//
// Energy is the whole balance lever. A magazine of shots that refills over a few
// seconds means a fight is a burst and a retreat, not a continuous beam, and it stops
// anyone from parking on a rival's mothership and holding the trigger.

const (
	// ShipMaxHealth and LaserDamage together set how many hits a kill takes — eight at
	// base damage, so a duel is winnable by the pilot who lands more, not the one who
	// happened to fire first.
	//
	// Eight is more than one stock magazine holds, which is deliberate now that these
	// numbers also have to describe a 2500-point mothership: a kill costs a reload, so
	// disengaging is always an option and nobody is deleted from full health in one
	// burst. Everything else that has health is expressed against this scale — see
	// toughnessScale in tiers.go, which keeps rocks at the time-to-break they were
	// tuned for when this number moved.
	ShipMaxHealth = 30.0
	LaserDamage   = 4.0

	// Energy: a burst of shots, then a wait.
	BaseMaxEnergy    = 6.0
	EnergyRefillSecs = 5.0

	// Firing faster than this is pointless — it just empties the magazine in a frame.
	ShotCooldownTicks = 6

	// How long a destroyed ship stays out of the game before respawning at home.
	RespawnTicks = TickHz * 4

	// A destroyed ship's wreck shoves nearby rocks around; without it a kill is
	// strangely silent up close.
	DeathBlastRadius = 26.0
	DeathBlastForce  = 900.0
)

// Shot is one muzzle discharge: a flash and a bang at the barrel, nothing more.
//
// It used to describe a whole hitscan beam — how far it reached, whether it connected and
// what it hit. None of that can be known at the trigger any more, because the round has
// not gone anywhere yet: bolts travel and fall (projectiles.go) and reach clients as their
// own bodies. Those fields were left carrying zeros for a while and immediately misled a
// test into asserting that nothing ever hit anything, so they are gone.
type Shot struct {
	Shooter physics.EntityID
	Team    uint8
}

// MaxEnergy is this player's magazine size, including upgrades.
func (p *Player) MaxEnergy() float32 {
	return BaseMaxEnergy + 2.0*float32(p.Upgrades[UpgradeCapacitor])
}

// EnergyRegenPerSecond is how fast the magazine refills.
func (p *Player) EnergyRegenPerSecond() float32 {
	// Each recharger level cuts the refill time by 20%.
	secs := EnergyRefillSecs
	for i := 0; i < p.Upgrades[UpgradeRecharger]; i++ {
		secs *= 0.8
	}
	return p.MaxEnergy() / float32(secs)
}

// LaserDamageDealt is this player's per-shot damage, including upgrades.
func (p *Player) LaserDamageDealt() float32 {
	return LaserDamage * (1.0 + 0.4*float32(p.Upgrades[UpgradeLaser]))
}

// Shots returns the laser discharges from the most recent tick.
func (s *Sim) Shots() []Shot { return s.shots }

// updateWeapons regenerates energy and fires for every player who is holding the
// trigger. Called once per tick from Step.
func (s *Sim) updateWeapons() {
	s.shots = s.shots[:0]

	for _, p := range s.players {
		if p.cooldown > 0 {
			p.cooldown--
		}

		// Recharge. Dead players refill too, so a respawn is not also a reload wait.
		maxE := p.MaxEnergy()
		if p.Energy < maxE {
			p.Energy += p.EnergyRegenPerSecond() * TickDuration
			if p.Energy > maxE {
				p.Energy = maxE
			}
		}

		if p.Dead() {
			// Grounded players have no countdown to run. Without this the timer would
			// tick past zero and hand them the respawn their team could not pay for.
			if p.grounded {
				continue
			}
			p.respawnIn--
			if p.respawnIn <= 0 {
				s.respawn(p)
			}
			continue
		}

		if !p.input.Fire || p.cooldown > 0 || p.Energy < 1.0 {
			continue
		}
		if !s.scoringActive() {
			continue // no shooting during warmup, intermissions or after the match
		}

		p.Energy -= 1.0
		p.cooldown = ShotCooldownTicks
		s.fireLaser(p)
		s.fireBolt(p)
	}
}

// fireLaser records the muzzle discharge for the clients.
//
// It no longer resolves the shot. Rounds travel and fall now (see projectiles.go), so
// nothing about where this shot ends up is known on the tick it is fired — this exists
// only so clients get a flash and a bang at the barrel on the frame the trigger went
// down, which they cannot infer from a bolt that appears in the next snapshot.
func (s *Sim) fireLaser(p *Player) {
	s.shots = append(s.shots, Shot{Shooter: p.Ship, Team: p.Team})
}

// raySphere returns the distance to the near intersection, if any.
func raySphere(origin, dir, centre physics.Vec3, radius float32) (float32, bool) {
	m := origin.Sub(centre)
	b := m.Dot(dir)
	c := m.Dot(m) - radius*radius

	// Pointing away and already outside: no hit.
	if c > 0 && b > 0 {
		return 0, false
	}
	disc := b*b - c
	if disc < 0 {
		return 0, false
	}

	t := -b - float32(math.Sqrt(float64(disc)))
	if t < 0 {
		t = 0 // origin is inside the sphere
	}
	return t, true
}

func (s *Sim) playerByShip(e physics.EntityID) *Player {
	for _, p := range s.players {
		if p.Ship == e {
			return p
		}
	}
	return nil
}

// destroyShip kills a player, drops their cargo and starts the respawn clock.
func (s *Sim) destroyShip(victim, killer *Player) {
	if victim.Held != 0 {
		s.release(victim, EventDropped)
	}

	s.startRespawn(victim)
	if o, ok := s.objects[victim.Ship]; ok {
		o.Health = 0
	}

	// Shove nearby rocks so a kill has some physical consequence.
	if st, ok := s.states[victim.Ship]; ok {
		s.blast(st.Pos, DeathBlastRadius, DeathBlastForce)
	}

	// Park the wreck far away rather than despawning it: the entity id stays stable, so
	// clients keep their node and the respawn is a move rather than a pop-in.
	s.world.SetVelocity(victim.Ship, physics.Vec3{})
	s.world.SetPosition(victim.Ship, physics.Vec3{X: 0, Y: 0, Z: -100000})

	s.emit(Event{
		Type: EventShipDestroyed, Entity: victim.Ship,
		Player: killer.ID, Value: float32(victim.Team),
	})

	s.checkElimination()
}

// startRespawn charges the team a life and sets the clock — or grounds the player if that
// was the last one.
//
// Every death funnels through here rather than assigning respawnIn directly, so a new way
// to die cannot quietly become a free one.
func (s *Sim) startRespawn(victim *Player) {
	if s.spendLife(victim.Team) {
		victim.respawnIn = RespawnTicks
		return
	}
	victim.respawnIn = 0
	victim.grounded = true
}

func (s *Sim) respawn(p *Player) {
	p.respawnIn = 0
	if o, ok := s.objects[p.Ship]; ok {
		o.Health = o.MaxHealth
	}
	s.resetShip(p)
	p.Energy = p.MaxEnergy()
	p.Boost = p.MaxBoost()
	p.boostLocked = false
	p.boostCooldown = 0
	s.emit(Event{Type: EventShipRespawn, Entity: p.Ship, Value: float32(p.Team)})
}

// shatterAsteroid breaks a rock apart, spawning fragments if its tier splits.
//
// Fragments are thrown outward so the break reads as an explosion rather than a swap,
// and they inherit the parent's position spread over a shell so they do not all spawn
// inside one another and shove each other violently apart.
//
// A big rock yields two things: ordinary fragments of the next tier down, and — for the
// tiers that carry one — a seam of core material worth far more than the rest of the
// rock put together. That is the whole reason to spend a magazine on something you
// could have flown around.
func (s *Sim) shatterAsteroid(o *Object, shooter *Player) {
	st, ok := s.states[o.Entity]
	if !ok {
		st = physics.BodyState{}
	}
	spec := o.Tier.Spec()

	s.emit(Event{
		Type: EventAsteroidDestroyed, Entity: o.Entity,
		Player: shooter.ID, Value: float32(o.Tier),
	})

	s.world.Despawn(o.Entity)
	delete(s.objects, o.Entity)

	// Ordinary debris rides the parent's outer shell; the core comes from deep inside,
	// so it emerges slowly and close to the middle. That difference is doing real work:
	// the valuable piece stays where the rock was instead of being flung across the
	// belt, so whoever broke it open gets first claim on it.
	s.spawnFragments(st, o.Radius, spec.SplitsInto, spec.SplitCount, 0.75, 6, 6)
	s.spawnFragments(st, o.Radius, spec.CoreTier, spec.CoreCount, 0.18, 2, 2)
}

// spawnFragments scatters `count` rocks of `tier` around a shattered parent.
//
// spread is the fraction of the parent's radius the fragments start out at, and the two
// speeds are the fixed and random parts of how hard they are thrown.
func (s *Sim) spawnFragments(st physics.BodyState, parentRadius float32, tier Tier,
	count int, spread, baseSpeed, randSpeed float32) {

	if count <= 0 {
		return
	}
	spec := tier.Spec()

	for i := 0; i < count; i++ {
		// Spread the fragments evenly around the parent's equator.
		angle := 2 * math.Pi * float64(i) / float64(count)
		offset := physics.Vec3{
			X: float32(math.Cos(angle)) * parentRadius * spread,
			Y: float32(math.Sin(angle)) * parentRadius * spread,
			Z: float32(s.rng.NormFloat64()) * parentRadius * spread * 0.27,
		}

		radius := spec.MinRadius + float32(s.rng.Float64())*(spec.MaxRadius-spec.MinRadius)

		e := s.world.SpawnSphere(MassFor(tier, radius), radius, st.Pos.Add(offset))
		if e == 0 {
			return // storage full
		}
		s.world.SetMaterial(e, ShipRestitution, 0.25)

		// Fling outward, plus whatever the parent was already doing.
		speed := baseSpeed + float32(s.rng.Float64())*randSpeed
		s.world.SetVelocity(e, st.Vel.Add(offset.Norm().Scale(speed)))

		s.objects[e] = &Object{
			Entity: e, Kind: KindAsteroid, Tier: tier, Radius: radius,
			Team: NoTeam, Value: ValueFor(tier, radius), Integrity: 1,
			RequiredBeams: requiredBeamsFor(tier),
			Health:        HealthFor(tier, radius),
			MaxHealth:     HealthFor(tier, radius),
		}
	}
}

// blast pushes everything nearby away from a point, falling off with distance.
func (s *Sim) blast(centre physics.Vec3, radius, force float32) {
	for e, o := range s.objects {
		if o.Kind != KindAsteroid && o.Kind != KindSalvage {
			continue
		}
		st, ok := s.states[e]
		if !ok {
			continue
		}
		away := st.Pos.Sub(centre)
		d := away.Len()
		if d > radius || d < 1e-3 {
			continue
		}
		falloff := 1.0 - d/radius
		s.addForce(e, away.Scale(1/d).Scale(force*falloff), physics.Vec3{})
	}
}
