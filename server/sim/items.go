package sim

import (
	"math"

	"asteroidsalvage/physics"
)

// Cargo boxes and what is in them.
//
// Pickups are the one thing on the map you get by *flying*, rather than by shooting or by
// hauling. A box drifts inside a bubble, you steer through it, and what was in it goes
// into one of three slots. Nothing is aimed and nothing is bought — which makes them the
// only reward in the game that a fight cannot be won without leaving.
//
// Four items, chosen so that each answers a different question rather than being a bigger
// number than the last:
//
//   - Missile: one shot, five times a laser's damage. An opening, not a weapon.
//   - Damage:  30 seconds of a laser that hurts. Rewards already being in the fight.
//   - Speed:   30 seconds of getting somewhere. Rewards not being in the fight yet.
//   - Shield:  30 damage absorbed. The only one that is worth holding rather than using.
//
// Effects are on the *player*, not the ship, so being destroyed spends them — an item is a
// window, and a window you can bank across a respawn is just a permanent upgrade with
// extra steps.

// ItemID values are wire values — they ride in a pickup's tier byte and in PlayerState.
type ItemID uint8

const (
	ItemNone    ItemID = 0
	ItemMissile ItemID = 1
	ItemDamage  ItemID = 2
	ItemSpeed   ItemID = 3
	ItemShield  ItemID = 4
)

// itemKinds is every item a box can contain, for rolling one at random.
var itemKinds = []ItemID{ItemMissile, ItemDamage, ItemSpeed, ItemShield}

const (
	// ItemSlots is how many items a pilot can carry. Three, and picking up a fourth is
	// refused rather than silently overwriting one: a box you flew through and did not
	// get is annoying, but a missile that vanished because you clipped a speed boost on
	// the way to the fight is worse.
	ItemSlots = 3

	// MissileDamage is a single round's damage — five laser hits in one, so a missile
	// takes a full-health ship most of the way down without killing outright.
	MissileDamage = 25.0

	// MissileSpeed is deliberately slower than a bolt. It has to be dodgeable at range,
	// or a one-shot 25 damage hit is just a sniper rifle.
	MissileSpeed = 240.0

	// MissileTurnRate is how fast a missile can bend its own course, in radians per
	// second. This single number is the whole difference between "guided" and
	// "unavoidable".
	//
	// A missile is nearly three times a boosting ship's top speed, so it can always catch
	// up — dodging one is never about outrunning it. It is about making it turn. At 1.6
	// rad/s a missile needs about two seconds to reverse, which a ship burning across its
	// path can beat at close range; a missile that overshoots then has to come back
	// around, and its range cap eventually spends it.
	MissileTurnRate = 1.6

	// MissileSeekRange bounds acquisition, in metres. A missile locks at launch and never
	// re-targets, so this is how far you can pick somebody out — not how far it flies.
	MissileSeekRange = 900.0

	// missileSeekConeDot is the widest off-axis angle that will lock, as a dot product
	// against the nose. ~40 degrees: wide enough that lining up is not a pixel-hunt,
	// narrow enough that "which one did I lock" is never a surprise.
	missileSeekConeDot = 0.766

	// MissileRadius is generous. The missile is a rare shot, and losing one to a hitbox
	// technicality reads as the game cheating.
	MissileRadius = 2.5

	// BoostSeconds is how long the damage and speed items last.
	BoostSeconds = 30.0

	// BoostScale is what they multiply. +50%, applied to laser damage and to thrust.
	BoostScale = 1.5

	// ShieldPool is how much damage the shield absorbs before it is gone. Slightly more
	// than a ship's own hull, so popping it genuinely doubles a duel.
	ShieldPool = 30.0

	// PickupRadius is the bubble's size in metres — what you have to fly through. Still
	// generous against a 2 m ship, so collecting one is a small course correction rather
	// than threading a needle at 40 m/s, but no longer the size of a small asteroid.
	PickupRadius = 8.0

	// PickupCount is how many boxes the map holds at once.
	PickupCount = 14

	// PickupRespawnSeconds is how long after one is taken before another appears
	// somewhere else. Long enough that clearing the map is worth something, short enough
	// that a round never runs dry.
	PickupRespawnSeconds = 20

	// PickupSpread is how far from the origin boxes are scattered, in metres. Wider than
	// the mothership ring so they are not all clustered around home, inside MapRadius so
	// they are not somewhere nobody flies.
	PickupSpread = 1000.0

	// pickupEntityBase keeps synthetic pickup ids clear of real entities, of the hill,
	// and of bolts.
	pickupEntityBase = 0xFF000000
)

// Effects are a player's active item timers. Seconds, counted down each tick.
type Effects struct {
	Damage float32 // laser damage boost remaining
	Speed  float32 // thrust boost remaining
	Shield float32 // damage the shield can still absorb; not a timer
}

// HasItem reports whether this slot holds anything.
func (p *Player) HasItem(slot int) bool {
	return slot >= 0 && slot < ItemSlots && p.Items[slot] != ItemNone
}

// GiveItem puts an item in the first free slot, reporting false when full.
func (p *Player) GiveItem(item ItemID) bool {
	for i := range p.Items {
		if p.Items[i] == ItemNone {
			p.Items[i] = item
			return true
		}
	}
	return false
}

// ClearItems empties the inventory and every active effect. Called on death and between
// rounds — see the note at the top of this file.
func (p *Player) ClearItems() {
	for i := range p.Items {
		p.Items[i] = ItemNone
	}
	p.Effects = Effects{}
}

// updateEffects counts down the timed boosts. Called once per tick from Step.
func (s *Sim) updateEffects() {
	for _, p := range s.players {
		if p.impactCooldown > 0 {
			p.impactCooldown--
		}
		if p.Effects.Damage > 0 {
			p.Effects.Damage -= TickDuration
			if p.Effects.Damage < 0 {
				p.Effects.Damage = 0
			}
		}
		if p.Effects.Speed > 0 {
			p.Effects.Speed -= TickDuration
			if p.Effects.Speed < 0 {
				p.Effects.Speed = 0
			}
		}
	}
}

// UseItem spends the item in a slot. Reports whether anything happened, so the caller can
// tell a real use from a press on an empty slot.
func (s *Sim) UseItem(id PlayerID, slot int) bool {
	p, ok := s.players[id]
	if !ok || !p.HasItem(slot) || p.Dead() {
		return false
	}

	item := p.Items[slot]
	switch item {
	case ItemMissile:
		if !s.fireMissile(p) {
			return false // no ship to fire from; keep the missile
		}
	case ItemDamage:
		p.Effects.Damage = BoostSeconds
	case ItemSpeed:
		p.Effects.Speed = BoostSeconds
	case ItemShield:
		p.Effects.Shield = ShieldPool
	default:
		return false
	}

	p.Items[slot] = ItemNone
	s.emit(Event{Type: EventItemUsed, Entity: p.Ship, Player: p.ID, Value: float32(item)})
	return true
}

// fireMissile launches the one-shot round. Reuses the bolt system — same swept-segment
// flight, same range cap — because a missile is a bolt with different numbers, and giving
// it its own list would mean maintaining two copies of the collision code.
func (s *Sim) fireMissile(p *Player) bool {
	ship, ok := s.states[p.Ship]
	if !ok {
		return false
	}

	dir := ship.Rot.Forward()
	s.nextBoltID++
	s.bolts = append(s.bolts, Bolt{
		ID:      s.nextBoltID,
		Pos:     ship.Pos.Add(dir.Scale(ShipRadius + 1.5)),
		Vel:     dir.Scale(MissileSpeed).Add(ship.Vel),
		Team:    p.Team,
		Shooter: p.ID,
		Ship:    p.Ship,
		// Fixed damage: a missile is not scaled by the laser upgrades or by the damage
		// boost, so its value does not quietly depend on what else you are carrying.
		Damage:  MissileDamage,
		Missile: true,
		// Locked at launch and never re-acquired. A missile that kept shopping for a
		// better target would be impossible to bait, and baiting one is the interesting
		// half of being shot at.
		Target: s.acquireMissileTarget(p, ship, dir),
	})
	return true
}

// acquireMissileTarget picks the enemy ship closest to where the launcher is pointing.
//
// By aim angle rather than by distance, the same rule the tractor beam uses: a nearer ship
// off to the side must not steal the lock from the one you lined up on. Returns 0 when
// there is nothing in the cone, and an unlocked missile simply flies straight.
func (s *Sim) acquireMissileTarget(p *Player, ship physics.BodyState,
	dir physics.Vec3) physics.EntityID {

	var best physics.EntityID
	bestDot := float32(missileSeekConeDot)

	for e, o := range s.objects {
		if o.Kind != KindShip || o.Team == p.Team || e == p.Ship {
			continue
		}
		// A wreck waiting to respawn is parked far below the field. Locking one would
		// send the missile straight down out of the world.
		if pl := s.playerByShip(e); pl == nil || pl.Dead() {
			continue
		}

		st, ok := s.states[e]
		if !ok {
			continue
		}
		to := st.Pos.Sub(ship.Pos)
		dist := to.Len()
		if dist < 1e-3 || dist > MissileSeekRange {
			continue
		}
		if dot := to.Scale(1 / dist).Dot(dir); dot > bestDot {
			best, bestDot = e, dot
		}
	}
	return best
}

// steerMissile bends a missile toward its target by at most MissileTurnRate this tick.
//
// The turn is capped rather than the heading being set outright, which is the entire
// reason a missile can be dodged: it has to come around, and a ship that breaks late can
// make it overshoot.
func (s *Sim) steerMissile(b *Bolt) {
	if b.Target == 0 {
		return
	}
	st, ok := s.states[b.Target]
	if !ok {
		b.Target = 0 // it died or left; carry straight on
		return
	}

	speed := b.Vel.Len()
	if speed < 1e-3 {
		return
	}
	cur := b.Vel.Scale(1 / speed)

	// Aim where the target will be, not where it is. Without the lead a missile trails
	// a crossing ship forever and never closes the last few metres.
	lead := AimBolt(b.Pos, st.Pos, st.Vel)

	dot := cur.Dot(lead)
	if dot > 0.9999 {
		return // already on course
	}
	if dot < -1 {
		dot = -1
	}
	angle := float32(math.Acos(float64(dot)))

	// Rotate about the axis between the two headings, rather than blending toward the
	// desired one. A linear blend collapses at exactly 180 degrees — cur and lead are
	// then colinear, so every interpolation between them lands back on the same line and
	// the missile flies serenely on with a target directly behind it.
	axis := cur.Cross(lead)
	if axis.Len() < 1e-6 {
		// Antiparallel: every axis is equally correct, so pick one that is not colinear.
		axis = cur.Cross(physics.Vec3{Z: 1})
		if axis.Len() < 1e-6 {
			axis = cur.Cross(physics.Vec3{X: 1})
		}
	}
	axis = axis.Scale(1 / axis.Len())

	step := float32(MissileTurnRate) * TickDuration
	if angle < step {
		step = angle
	}

	// Rodrigues, simplified: the axis is perpendicular to cur, so the term in
	// axis*(axis . cur) drops out.
	sin, cos := float32(math.Sin(float64(step))), float32(math.Cos(float64(step)))
	next := cur.Scale(cos).Add(axis.Cross(cur).Scale(sin))
	if l := next.Len(); l > 1e-6 {
		b.Vel = next.Scale(speed / l)
	}
}

// absorbWithShield takes what it can from an active shield and returns the damage that
// gets through to the hull.
func (p *Player) absorbWithShield(damage float32) float32 {
	if p.Effects.Shield <= 0 || damage <= 0 {
		return damage
	}
	if damage <= p.Effects.Shield {
		p.Effects.Shield -= damage
		return 0
	}
	// Overkill spills through rather than being wasted, so a shield with 2 points left
	// does not shrug off a missile.
	through := damage - p.Effects.Shield
	p.Effects.Shield = 0
	return through
}

/*============================================================================
 * Pickups
 *===========================================================================*/

// Pickups are Objects like anything else, so they ride the snapshot and the client draws
// them through machinery it already has. The Tier byte carries which item is inside,
// which is what lets a box be recognisable from a distance without a new message.

// spawnPickups fills the map to PickupCount.
func (s *Sim) spawnPickups() {
	have := 0
	for _, o := range s.objects {
		if o.Kind == KindPickup {
			have++
		}
	}
	for ; have < PickupCount; have++ {
		s.spawnPickup()
	}
}

// spawnPickup places one box somewhere in the play volume.
func (s *Sim) spawnPickup() {
	pos := s.randomPickupPoint()
	item := itemKinds[s.rng.Intn(len(itemKinds))]

	// No physics body: a box you can collide with is a box that shoves you off course on
	// the way to collecting it, and one drifting into a station would be worse. It exists
	// only as a snapshot entry and a distance check.
	s.nextPickupID++
	e := physics.EntityID(pickupEntityBase + s.nextPickupID)
	s.objects[e] = &Object{
		Entity: e, Kind: KindPickup, Tier: Tier(item), Radius: PickupRadius,
		Team: NoTeam, Integrity: 1, Health: 1, MaxHealth: 1,
	}
	s.pickupPos[e] = pos
}

// randomPickupPoint picks somewhere clear of the stations to put a box.
func (s *Sim) randomPickupPoint() physics.Vec3 {
	for tries := 0; tries < 24; tries++ {
		p := physics.Vec3{
			X: (s.rng.Float32()*2 - 1) * PickupSpread,
			Y: (s.rng.Float32()*2 - 1) * PickupSpread,
			Z: (s.rng.Float32()*2 - 1) * PickupSpread * 0.35,
		}
		clear := true
		for _, o := range s.objects {
			if o.Kind != KindMothership {
				continue
			}
			if st, ok := s.states[o.Entity]; ok {
				if st.Pos.DistTo(p) < o.Radius+80 {
					clear = false
					break
				}
			}
		}
		if clear {
			return p
		}
	}
	return physics.Vec3{X: PickupSpread * 0.5}
}

// updatePickups collects any box a ship has flown through and tops the map back up.
func (s *Sim) updatePickups() {
	for _, p := range s.players {
		if p.Dead() {
			continue
		}
		ship, ok := s.states[p.Ship]
		if !ok {
			continue
		}

		for e, o := range s.objects {
			if o.Kind != KindPickup {
				continue
			}
			pos, ok := s.pickupPos[e]
			if !ok {
				continue
			}
			if pos.DistTo(ship.Pos) > PickupRadius+ShipRadius {
				continue
			}

			// A full inventory leaves the box where it is rather than eating it, so the
			// pilot can come back once they have spent something.
			if !p.GiveItem(ItemID(o.Tier)) {
				continue
			}

			s.emit(Event{
				Type: EventItemPickedUp, Entity: e, Player: p.ID,
				Value: float32(o.Tier),
			})
			delete(s.objects, e)
			delete(s.pickupPos, e)
			s.pickupRespawn = append(s.pickupRespawn, PickupRespawnSeconds*TickHz)
		}
	}

	// Respawn timers. A box taken comes back somewhere else rather than in the same
	// place, so clearing a corner of the map is worth doing.
	live := s.pickupRespawn[:0]
	for _, ticks := range s.pickupRespawn {
		if ticks--; ticks <= 0 {
			s.spawnPickup()
			continue
		}
		live = append(live, ticks)
	}
	s.pickupRespawn = live
}

// PickupPos exposes a box's position for the snapshot encoder. Pickups have no physics
// body, so they are not in the state index everything else is read from.
func (s *Sim) PickupPos(e physics.EntityID) (physics.Vec3, bool) {
	p, ok := s.pickupPos[e]
	return p, ok
}

// clearPickups removes every box. Called when a round ends, alongside clearSalvage.
func (s *Sim) clearPickups() {
	for e, o := range s.objects {
		if o.Kind == KindPickup {
			delete(s.objects, e)
			delete(s.pickupPos, e)
		}
	}
	s.pickupRespawn = s.pickupRespawn[:0]
}
