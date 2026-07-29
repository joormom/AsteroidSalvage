package sim

import "asteroidsalvage/physics"

// Laser bolts.
//
// These used to be hitscan: the shot resolved on the tick it was fired and the client drew
// a line. That made the weapon a pointing contest — if your crosshair was on someone when
// you clicked, you hit them, at any range.
//
// Bolts travel now, and they fall. Two consequences, and both are the point:
//
//   - You have to lead a moving target. A ship crossing at 40 m/s is most of its own
//     length away by the time a bolt crosses 300 m, so range costs accuracy in a way a
//     damage falloff never conveys.
//   - You have to aim high at distance. The drop is not physics — there is no gravity out
//     here — it is a deliberate arc that turns a long shot into a judgement rather than a
//     straight line, and it gives the reticle something to be wrong about.
//
// The server owns the flight. Bolts are not physics bodies: they are swept segments tested
// against the same objects the old raycast used, which keeps them cheap and keeps them from
// bouncing off the things they are supposed to hit.

const (
	// BoltSpeed is muzzle velocity in m/s. Roughly ten times a ship's top speed, so
	// leading a target is a real correction at range without the bolt being effectively
	// instant across the belt.
	BoltSpeed = 420.0

	// BoltDrop is the downward acceleration applied to a bolt, in m/s². Along world -Z,
	// which is the plane the stations and the belt sit in, so "down" means the same thing
	// to everyone regardless of which way their ship is rolled.
	//
	// At 420 m/s a 300 m shot is in the air 0.71 s and falls about 5.5 m — enough that
	// you notice and compensate at range, little enough that ordinary mid-range fighting
	// is still pointing rather than lobbing.
	BoltDrop = 22.0

	// BoltLifetimeTicks caps a bolt's flight. At BoltSpeed this is well past the far side
	// of the map, so it only ever matters for shots fired into open space.
	BoltLifetimeTicks = TickHz * 5

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

	ticks int
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
		ticks:   BoltLifetimeTicks,
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

		b.ticks--
		if b.ticks <= 0 {
			continue
		}

		// Drop, then step. Applying the acceleration first makes the arc consistent with
		// the client's, which integrates the same way.
		b.Vel.Z -= BoltDrop * TickDuration
		from := b.Pos
		to := from.Add(b.Vel.Scale(TickDuration))

		if hit, point := s.boltSweep(b, from, to); hit != 0 {
			s.resolveBoltHit(b, hit, point)
			continue // spent
		}

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
		if t, hit := raySphere(from, dir, st.Pos, o.Radius+BoltRadius); hit && t <= bestT {
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
	o.Health -= b.Damage
	s.emit(Event{Type: EventShipHit, Entity: o.Entity, Value: b.Damage})
	if o.Health <= 0 {
		s.destroyShipByStation(victim, b.Team)
	}
}

// AimBolt returns the direction to fire so a round arrives where a moving target will be,
// compensating for both the target's motion and the bolt's own drop.
//
// Solved by iteration rather than algebraically: flight time depends on the lead point and
// the lead point depends on the flight time. Two passes is plenty at these speeds, and it
// keeps a closed-form quartic out of the codebase for a gun that is allowed to miss.
func AimBolt(from, targetPos, targetVel physics.Vec3) physics.Vec3 {
	aim := targetPos
	for i := 0; i < 3; i++ {
		t := aim.Sub(from).Len() / BoltSpeed
		// Where it will be, raised by however far the round will fall getting there.
		aim = targetPos.Add(targetVel.Scale(t))
		aim.Z += 0.5 * BoltDrop * t * t
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
		ticks:  BoltLifetimeTicks,
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
		o.Health -= damage
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
