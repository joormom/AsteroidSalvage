package sim

import "asteroidsalvage/physics"

// Laser bolts.
//
// These used to be hitscan: the shot resolved on the tick it was fired and the client drew
// a line. That made the weapon a pointing contest — if your crosshair was on someone when
// you clicked, you hit them, at any range.
//
// Bolts travel, and they fly straight. Travel time is the whole mechanic: you have to
// lead a moving target, and a ship crossing at 40 m/s is most of its own length away by
// the time a bolt covers 300 m — so range costs accuracy in a way a damage falloff never
// conveys.
//
// They used to arc downward as well, a deliberate 22 m/s² along world -Z. That is gone:
// a shot now goes exactly where it is pointed, and the only correction a player makes is
// for the target's motion. There is no gravity out here, and a reticle that is wrong
// about elevation turns every long shot into a guess at how much to hold over rather than
// a read of where somebody is going.
//
// What replaces the arc as a limit on range is a hard one: a round is spent after
// BoltMaxRange and simply stops existing.
//
// The server owns the flight. Bolts are not physics bodies: they are swept segments tested
// against the same objects the old raycast used, which keeps them cheap and keeps them from
// bouncing off the things they are supposed to hit.

const (
	// BoltSpeed is muzzle velocity in m/s. Roughly ten times a ship's top speed, so
	// leading a target is a real correction at range without the bolt being effectively
	// instant across the belt.
	BoltSpeed = 420.0

	// BoltMaxRange is how far a round travels before it is spent, in metres. Measured as
	// distance actually flown rather than as a lifetime in ticks, so a round fired from a
	// ship already moving fast — muzzle velocity is added to the hull's — does not quietly
	// reach further than one fired from a standstill.
	//
	// 2000 m is a little under a fifth of the map's diameter and comfortably past any
	// range a fight happens at, so in practice it bounds shots fired into open space.
	BoltMaxRange = 2000.0

	// BoltRadius is the bolt's own size for collision, in metres. Small but not zero:
	// a purely infinitesimal point makes glancing hits on a fast crosser feel stolen.
	BoltRadius = 0.8
)

// Bolt is one laser round in flight.
type Bolt struct {
	ID  uint32
	Pos physics.Vec3
	Vel physics.Vec3

	Team    uint8
	Shooter PlayerID         // 0 for a station turret, which has no pilot
	Ship    physics.EntityID // the hull it came from, so it cannot hit its own nose
	Damage  float32

	// Missile marks the one-shot item round rather than a laser bolt. It flies through
	// the same code with different numbers; the flag changes how it is drawn, how big it
	// is, and that it steers. See items.go.
	Missile bool

	// Target is the ship a missile is chasing, locked at launch. 0 means it never
	// acquired one, or the one it had is gone — either way it carries straight on.
	Target physics.EntityID

	// travelled is metres flown so far, against BoltMaxRange.
	travelled float32
}

// Radius is the round's collision size. A missile is fatter than a bolt: it is a rare
// shot, and losing one to a hitbox technicality reads as the game cheating.
func (b *Bolt) Radius() float32 {
	if b.Missile {
		return MissileRadius
	}
	return BoltRadius
}

// Bolts returns every round currently in flight, for the snapshot encoder.
func (s *Sim) Bolts() []Bolt { return s.bolts }

// fireBolt launches a round from a player's nose.
func (s *Sim) fireBolt(p *Player) {
	ship, ok := s.states[p.Ship]
	if !ok {
		return
	}

	dir := ship.Rot.Forward()
	s.nextBoltID++

	s.bolts = append(s.bolts, Bolt{
		ID: s.nextBoltID,
		// Started at the nose rather than the centre, or the bolt spawns inside the hull
		// and the muzzle flash is buried in the model.
		Pos:     ship.Pos.Add(dir.Scale(ShipRadius + 1.0)),
		Vel:     dir.Scale(BoltSpeed).Add(ship.Vel),
		Team:    p.Team,
		Shooter: p.ID,
		Ship:    p.Ship,
		Damage:  p.LaserDamageDealt(),
	})
}

// updateBolts advances every round one tick and resolves what it hits.
func (s *Sim) updateBolts() {
	if len(s.bolts) == 0 {
		return
	}

	live := s.bolts[:0]
	for i := range s.bolts {
		b := &s.bolts[i]

		// Steer before stepping, so the tick that turns is also the tick that travels
		// along the new heading — a missile that turned only after moving would trail
		// its own course by one tick, which at 240 m/s is 8 m of lag.
		if b.Missile {
			s.steerMissile(b)
		}

		from := b.Pos
		step := b.Vel.Scale(TickDuration)
		to := from.Add(step)

		// Range is checked against the step that is about to happen, so a round is spent
		// at the limit rather than one tick past it — at 420 m/s a tick is 14 m, which is
		// wider than most things in the game.
		if b.travelled+step.Len() >= BoltMaxRange {
			continue
		}

		if hit, point := s.boltSweep(b, from, to); hit != 0 {
			s.resolveBoltHit(b, hit, point)
			continue // spent
		}

		b.travelled += step.Len()
		b.Pos = to
		live = append(live, *b)
	}
	s.bolts = live
}

// boltSweep tests a bolt's movement over one tick against everything it can hit.
//
// A swept segment rather than a point test at the new position: at 420 m/s a bolt covers
// 14 m in a tick, which is wider than most things in the game — a point test would fly
// straight through a ship on most frames.
func (s *Sim) boltSweep(b *Bolt, from, to physics.Vec3) (physics.EntityID, physics.Vec3) {
	delta := to.Sub(from)
	dist := delta.Len()
	if dist < 1e-6 {
		return 0, to
	}
	dir := delta.Scale(1 / dist)

	var best physics.EntityID
	bestT := dist

	for e, o := range s.objects {
		switch o.Kind {
		case KindAsteroid, KindSalvage, KindProp:
			// shootable, or solid cover
		case KindShip:
			// Your own hull and your crew's are not targets; nor is a wreck.
			if e == b.Ship || o.Team == b.Team {
				continue
			}
			if pl := s.playerByShip(e); pl == nil || pl.Dead() {
				continue
			}
		case KindMothership:
			if o.Team == b.Team || o.Health <= 0 {
				continue
			}
		default:
			continue
		}

		st, ok := s.states[e]
		if !ok {
			continue
		}
		// Against the object's real collider, not a sphere of Radius. For everything
		// spherical those are the same test; for the scenery that is not, this is what stops
		// a shot passing through solid hull or stopping against thin air. See collider.go.
		if t, hit := o.Collider.Ray(from, dir, st.Pos, st.Rot, b.Radius()); hit && t <= bestT {
			best, bestT = e, t
		}
	}

	return best, from.Add(dir.Scale(bestT))
}

// resolveBoltHit applies a bolt's damage to whatever it struck.
func (s *Sim) resolveBoltHit(b *Bolt, target physics.EntityID, point physics.Vec3) {
	o, ok := s.objects[target]
	if !ok {
		return
	}

	// Props absorb the round and take nothing: scenery is cover, not a target.
	if o.Kind == KindProp {
		return
	}

	// Damage is carried on the bolt rather than re-read from the shooter, so a round
	// already in flight is not retroactively buffed by an upgrade bought mid-flight.
	shooter, ok := s.players[b.Shooter]
	if !ok {
		// A station's round, or one whose pilot has since left. Stations still have to
		// be able to kill, so this is not simply dropped.
		s.applyStationDamage(b, o)
		return
	}
	s.applyDamageAt(shooter, o, b.Damage)
}

// applyStationDamage resolves a round with no pilot behind it.
func (s *Sim) applyStationDamage(b *Bolt, o *Object) {
	if o.Kind != KindShip {
		return // stations shoot at ships; a round into a rock simply stops
	}
	victim := s.playerByShip(o.Entity)
	if victim == nil {
		return
	}
	o.Health -= victim.absorbWithShield(b.Damage)
	s.emit(Event{Type: EventShipHit, Entity: o.Entity, Value: b.Damage})
	if o.Health <= 0 {
		s.destroyShipByStation(victim, b.Team)
	}
}

// AimBolt returns the direction to fire so a round arrives where a moving target will be.
//
// Lead only. It used to solve elevation as well, back when rounds arced; with a flat
// trajectory the whole problem is "where will they be when it gets there".
//
// Solved by iteration rather than algebraically: flight time depends on the lead point and
// the lead point depends on the flight time. Three passes is plenty at these speeds, and it
// keeps a closed-form quartic out of the codebase for a gun that is allowed to miss.
func AimBolt(from, targetPos, targetVel physics.Vec3) physics.Vec3 {
	aim := targetPos
	for i := 0; i < 3; i++ {
		t := aim.Sub(from).Len() / BoltSpeed
		aim = targetPos.Add(targetVel.Scale(t))
	}
	d := aim.Sub(from)
	if l := d.Len(); l > 1e-6 {
		return d.Scale(1 / l)
	}
	return physics.Vec3{Y: 1}
}

// fireStationBolt launches a turret round along a direction.
func (s *Sim) fireStationBolt(team uint8, station physics.EntityID, from physics.Vec3,
	dir physics.Vec3, damage float32) {

	s.nextBoltID++
	s.bolts = append(s.bolts, Bolt{
		ID:     s.nextBoltID,
		Pos:    from,
		Vel:    dir.Scale(BoltSpeed),
		Team:   team,
		Ship:   station, // so a round cannot clip the hull it left
		Damage: damage,
	})
}

// applyDamageAt is applyLaserDamage with the damage supplied rather than computed.
func (s *Sim) applyDamageAt(shooter *Player, o *Object, damage float32) {
	if o.Kind == KindMothership {
		s.damageMothership(shooter, o, damage)
		return
	}

	if o.Kind == KindShip {
		victim := s.playerByShip(o.Entity)
		if victim == nil {
			return
		}
		// The item shield eats what it can first. A hit it absorbs entirely still emits
		// the event, so the shooter sees they connected and the victim can be shown the
		// shield taking it rather than nothing happening at all.
		through := victim.absorbWithShield(damage)
		o.Health -= through
		s.emit(Event{Type: EventShipHit, Entity: o.Entity, Player: shooter.ID, Value: damage})
		if o.Health <= 0 {
			s.destroyShip(victim, shooter)
		}
		return
	}

	o.Health -= damage
	if o.Health <= 0 {
		s.shatterAsteroid(o, shooter)
	} else {
		s.emit(Event{Type: EventAsteroidHit, Entity: o.Entity, Player: shooter.ID, Value: damage})
	}
}
