package sim

import (
	"math"
	"testing"

	"asteroidsalvage/physics"
)

// placePickupAt drops a box right where the test wants it, rather than waiting for the
// scatter to happen to put one somewhere useful.
func placePickupAt(s *Sim, item ItemID, pos physics.Vec3) physics.EntityID {
	s.nextPickupID++
	e := physics.EntityID(pickupEntityBase + s.nextPickupID)
	s.objects[e] = &Object{
		Entity: e, Kind: KindPickup, Tier: Tier(item), Radius: PickupRadius,
		Team: NoTeam, Integrity: 1, Health: 1, MaxHealth: 1,
	}
	s.pickupPos[e] = pos
	return e
}

func TestFlyingThroughABoxCollectsIt(t *testing.T) {
	s := newBareSim(t, nil)
	s.clearPickups()
	p := s.AddPlayer("pilot", 0)

	s.world.SetPosition(p.Ship, physics.Vec3{X: 800, Y: 0, Z: 0})
	s.world.SetVelocity(p.Ship, physics.Vec3{})
	s.stepN(1)

	ship, _ := s.world.GetBody(p.Ship)
	box := placePickupAt(s, ItemMissile, ship.Pos)
	_ = s.DrainEvents()

	s.stepN(1)

	if p.Items[0] != ItemMissile {
		t.Errorf("slot 0 holds %d, want a missile", p.Items[0])
	}
	if _, still := s.objects[box]; still {
		t.Error("the box is still on the map after being collected")
	}

	var sawEvent bool
	for _, e := range s.DrainEvents() {
		if e.Type == EventItemPickedUp && e.Player == p.ID {
			sawEvent = true
			if ItemID(e.Value) != ItemMissile {
				t.Errorf("pickup event carried item %v, want a missile", e.Value)
			}
		}
	}
	if !sawEvent {
		t.Error("collecting a box emitted no event")
	}
}

// A box you fly through with no room left stays put, so you can come back for it once
// something has been spent.
func TestAFullInventoryLeavesTheBoxAlone(t *testing.T) {
	s := newBareSim(t, nil)
	s.clearPickups()
	p := s.AddPlayer("pilot", 0)

	for i := range p.Items {
		p.Items[i] = ItemShield
	}

	s.world.SetPosition(p.Ship, physics.Vec3{X: 800, Y: 0, Z: 0})
	s.world.SetVelocity(p.Ship, physics.Vec3{})
	s.stepN(1)

	ship, _ := s.world.GetBody(p.Ship)
	box := placePickupAt(s, ItemMissile, ship.Pos)
	s.stepN(2)

	if _, still := s.objects[box]; !still {
		t.Error("a box was consumed by a pilot with no room for it")
	}
}

func TestBoostsExpireAfterTheirWindow(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)
	p.Items[0] = ItemSpeed

	base := p.ThrustMultiplier()
	if !s.UseItem(p.ID, 0) {
		t.Fatal("using a speed boost did nothing")
	}
	if p.Items[0] != ItemNone {
		t.Error("the slot still holds the item after using it")
	}

	if boosted := p.ThrustMultiplier(); boosted <= base {
		t.Errorf("thrust multiplier %v is not above the base %v", boosted, base)
	}

	// Runs out on its own.
	s.stepN(int(BoostSeconds*TickHz) + 2)
	if p.Effects.Speed != 0 {
		t.Errorf("%v seconds of boost left after the full window", p.Effects.Speed)
	}
	if got := p.ThrustMultiplier(); got != base {
		t.Errorf("thrust multiplier stayed at %v after the boost expired, want %v", got, base)
	}
}

func TestDamageBoostRaisesLaserDamage(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)
	p.Items[1] = ItemDamage

	base := p.LaserDamageDealt()
	if !s.UseItem(p.ID, 1) {
		t.Fatal("using a damage boost did nothing")
	}
	if got := p.LaserDamageDealt(); got != base*BoostScale {
		t.Errorf("laser damage %v, want %v", got, base*BoostScale)
	}
}

// The shield is a pool, not a timer: it absorbs up to ShieldPool and then stops, and
// overkill spills through rather than being wasted.
func TestShieldAbsorbsThenBreaks(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)
	p.Items[2] = ItemShield

	if !s.UseItem(p.ID, 2) {
		t.Fatal("using a shield did nothing")
	}
	if p.Effects.Shield != ShieldPool {
		t.Fatalf("shield pool is %v, want %v", p.Effects.Shield, ShieldPool)
	}

	if through := p.absorbWithShield(10); through != 0 {
		t.Errorf("%v damage got through a fresh shield", through)
	}
	if p.Effects.Shield != ShieldPool-10 {
		t.Errorf("shield has %v left, want %v", p.Effects.Shield, ShieldPool-10)
	}

	// More than it has left: the excess reaches the hull.
	if through := p.absorbWithShield(30); through != 30-(ShieldPool-10) {
		t.Errorf("overkill let %v through, want %v", through, 30-(ShieldPool-10))
	}
	if p.Effects.Shield != 0 {
		t.Errorf("shield is on %v after being overwhelmed, want 0", p.Effects.Shield)
	}
	if through := p.absorbWithShield(5); through != 5 {
		t.Errorf("a spent shield still absorbed %v", 5-through)
	}
}

func TestMissileFliesAndHitsForItsOwnDamage(t *testing.T) {
	s := newBareSim(t, nil)
	shooter := s.AddPlayer("gunner", 0)
	victim := s.AddPlayer("target", 1)
	shooter.Items[0] = ItemMissile

	s.world.SetPosition(shooter.Ship, physics.Vec3{X: 0, Y: 0, Z: 700})
	s.world.SetVelocity(shooter.Ship, physics.Vec3{})
	s.stepN(1)
	s.world.SetPosition(victim.Ship, placeInFrontOf(s, shooter, 150))
	s.world.SetVelocity(victim.Ship, physics.Vec3{})
	s.stepN(1)

	before := s.objects[victim.Ship].Health
	if !s.UseItem(shooter.ID, 0) {
		t.Fatal("firing a missile did nothing")
	}

	// In the air, and marked as a missile rather than a bolt.
	bolts := s.Bolts()
	if len(bolts) != 1 {
		t.Fatalf("%d rounds in flight after firing a missile, want 1", len(bolts))
	}
	if !bolts[0].Missile {
		t.Error("the round in flight is not marked as a missile")
	}
	if bolts[0].Damage != MissileDamage {
		t.Errorf("missile carries %v damage, want %v", bolts[0].Damage, MissileDamage)
	}

	s.stepN(TickHz)
	if got := s.objects[victim.Ship].Health; got != before-MissileDamage {
		t.Errorf("victim went from %v to %v, want a %v-damage hit", before, got, MissileDamage)
	}
}

// A missile is fixed damage: it must not scale with the laser upgrades or with a damage
// boost, or its worth would quietly depend on what else the pilot is carrying.
func TestMissileDamageIsFixed(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("gunner", 0)
	p.Upgrades[UpgradeLaser] = 3
	p.Effects.Damage = BoostSeconds
	p.Items[0] = ItemMissile

	s.world.SetPosition(p.Ship, physics.Vec3{X: 0, Y: 0, Z: 700})
	s.stepN(1)
	if !s.UseItem(p.ID, 0) {
		t.Fatal("firing a missile did nothing")
	}

	if got := s.Bolts()[0].Damage; got != MissileDamage {
		t.Errorf("missile damage is %v with upgrades and a boost active, want %v",
			got, MissileDamage)
	}
}

// A missile locks on at launch and chases. Fired at a target that then moves off the
// launch heading, it must still connect — a straight round would sail past.
func TestMissileChasesAMovingTarget(t *testing.T) {
	s := newBareSim(t, nil)
	shooter := s.AddPlayer("gunner", 0)
	victim := s.AddPlayer("target", 1)
	shooter.Items[0] = ItemMissile

	s.world.SetPosition(shooter.Ship, physics.Vec3{X: 0, Y: 0, Z: 700})
	s.world.SetVelocity(shooter.Ship, physics.Vec3{})
	s.stepN(1)
	s.world.SetPosition(victim.Ship, placeInFrontOf(s, shooter, 400))
	s.world.SetVelocity(victim.Ship, physics.Vec3{})
	s.stepN(1)

	before := s.objects[victim.Ship].Health
	if !s.UseItem(shooter.ID, 0) {
		t.Fatal("firing a missile did nothing")
	}
	if s.Bolts()[0].Target != victim.Ship {
		t.Fatalf("missile locked entity %d, want the enemy ship %d",
			s.Bolts()[0].Target, victim.Ship)
	}

	// Slide the target sideways, off the heading the missile launched on. A modest
	// drift, so this tests guidance rather than the turn rate's limits.
	for i := 0; i < TickHz*3 && s.objects[victim.Ship].Health >= before; i++ {
		s.world.SetVelocity(victim.Ship, physics.Vec3{X: 25})
		s.stepN(1)
	}

	if got := s.objects[victim.Ship].Health; got >= before {
		t.Errorf("victim on %v health after a missile chase; it never connected", got)
	}
}

// The turn rate is the whole reason a missile is a weapon rather than a death sentence:
// it must be capable of overshooting.
func TestMissileTurnRateIsLimited(t *testing.T) {
	s := newBareSim(t, nil)
	shooter := s.AddPlayer("gunner", 0)
	victim := s.AddPlayer("target", 1)
	shooter.Items[0] = ItemMissile

	s.world.SetPosition(shooter.Ship, physics.Vec3{X: 0, Y: 0, Z: 700})
	s.world.SetVelocity(shooter.Ship, physics.Vec3{})
	s.stepN(1)
	s.world.SetPosition(victim.Ship, placeInFrontOf(s, shooter, 120))
	s.world.SetVelocity(victim.Ship, physics.Vec3{})
	s.stepN(1)

	if !s.UseItem(shooter.ID, 0) {
		t.Fatal("firing a missile did nothing")
	}

	launch := s.Bolts()[0].Vel
	// Teleport the target hard about-face behind the launcher, so the missile is asked
	// for an impossible turn.
	s.world.SetPosition(victim.Ship, placeInFrontOf(s, shooter, -300))
	s.stepN(1)

	if len(s.Bolts()) == 0 {
		t.Fatal("the missile vanished")
	}
	now := s.Bolts()[0].Vel

	// One tick may bend the heading by at most MissileTurnRate*dt. Compare directions.
	a := launch.Scale(1 / launch.Len())
	b := now.Scale(1 / now.Len())
	turned := math.Acos(math.Min(1, math.Max(-1, float64(a.Dot(b)))))
	limit := MissileTurnRate*TickDuration + 1e-3

	if turned > float64(limit) {
		t.Errorf("missile turned %.3f rad in one tick, cap is %.3f", turned, limit)
	}
	if turned <= 0 {
		t.Error("missile did not turn toward its target at all")
	}
}

// Nothing in the cone means a missile that flies straight, rather than one that refuses
// to launch or picks something behind the shooter.
func TestMissileWithNoTargetFliesStraight(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("gunner", 0)
	p.Items[0] = ItemMissile

	s.world.SetPosition(p.Ship, physics.Vec3{X: 0, Y: 0, Z: 5000})
	s.world.SetVelocity(p.Ship, physics.Vec3{})
	s.stepN(1)

	if !s.UseItem(p.ID, 0) {
		t.Fatal("a missile with nothing to lock refused to launch")
	}
	if got := s.Bolts()[0].Target; got != 0 {
		t.Errorf("missile locked entity %d with no enemy in the cone", got)
	}

	launch := s.Bolts()[0].Vel
	s.stepN(TickHz)
	if len(s.Bolts()) == 0 {
		t.Fatal("the missile vanished")
	}
	if now := s.Bolts()[0].Vel; now.Sub(launch).Len() > 1 {
		t.Errorf("an unlocked missile changed course by %v", now.Sub(launch).Len())
	}
}

// A teammate must not be lockable, and neither must the shooter's own hull.
func TestMissileWillNotLockFriendlies(t *testing.T) {
	s := newBareSim(t, nil)
	shooter := s.AddPlayer("gunner", 0)
	mate := s.AddPlayer("mate", 0)
	shooter.Items[0] = ItemMissile

	if mate.Team != shooter.Team {
		t.Fatalf("test setup: teams %d and %d", shooter.Team, mate.Team)
	}

	s.world.SetPosition(shooter.Ship, physics.Vec3{X: 0, Y: 0, Z: 5000})
	s.world.SetVelocity(shooter.Ship, physics.Vec3{})
	s.stepN(1)
	s.world.SetPosition(mate.Ship, placeInFrontOf(s, shooter, 200))
	s.world.SetVelocity(mate.Ship, physics.Vec3{})
	s.stepN(1)

	if !s.UseItem(shooter.ID, 0) {
		t.Fatal("firing a missile did nothing")
	}
	if got := s.Bolts()[0].Target; got != 0 {
		t.Errorf("missile locked entity %d, which is a teammate", got)
	}
}

func TestUsingAnEmptySlotDoesNothing(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("pilot", 0)

	if s.UseItem(p.ID, 0) {
		t.Error("using an empty slot reported success")
	}
	if s.UseItem(p.ID, 99) {
		t.Error("using an out-of-range slot reported success")
	}
}

// Dying spends what you were carrying. An item that survives a respawn is a permanent
// upgrade with extra steps.
func TestDeathClearsItemsAndEffects(t *testing.T) {
	s := newBareSim(t, nil)
	victim := s.AddPlayer("victim", 0)
	killer := s.AddPlayer("killer", 1)

	victim.Items[0] = ItemMissile
	victim.Effects = Effects{Damage: 10, Speed: 10, Shield: 20}

	s.destroyShip(victim, killer)

	if victim.Items[0] != ItemNone {
		t.Error("items survived a death")
	}
	if victim.Effects != (Effects{}) {
		t.Errorf("effects survived a death: %+v", victim.Effects)
	}
}

// The map keeps itself stocked, and a box taken comes back somewhere else.
func TestPickupsRespawnAfterBeingTaken(t *testing.T) {
	s := newBareSim(t, nil)
	s.clearPickups()
	s.spawnPickups()

	count := func() int {
		n := 0
		for _, o := range s.objects {
			if o.Kind == KindPickup {
				n++
			}
		}
		return n
	}

	if got := count(); got != PickupCount {
		t.Fatalf("map has %d boxes, want %d", got, PickupCount)
	}

	p := s.AddPlayer("pilot", 0)
	s.world.SetPosition(p.Ship, physics.Vec3{X: 800, Y: 0, Z: 0})
	s.world.SetVelocity(p.Ship, physics.Vec3{})
	s.stepN(1)
	ship, _ := s.world.GetBody(p.Ship)
	placePickupAt(s, ItemSpeed, ship.Pos)

	s.stepN(1)
	if got := count(); got != PickupCount {
		t.Fatalf("after collecting one of %d+1 boxes the map holds %d", PickupCount, got)
	}

	// The respawn timer puts the taken one back.
	s.stepN(PickupRespawnSeconds*TickHz + 2)
	if got := count(); got != PickupCount+1 {
		t.Errorf("map holds %d boxes after the respawn, want %d", got, PickupCount+1)
	}
}
