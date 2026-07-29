package sim

import (
	"testing"

	"asteroidsalvage/physics"
)

// Match stats feed the end-of-match table. They are counted at the same places the
// corresponding events are emitted, so a row can never disagree with what the player
// watched happen.

func TestDeliveryCountsTowardTheHaulersStats(t *testing.T) {
	s := newBareSim(t, nil)
	p := s.AddPlayer("hauler", 0)

	s.world.SetPosition(p.Ship, s.homeOf(p).Add(physics.Vec3{X: 40, Y: 0, Z: 0}))
	s.stepN(1)
	_ = s.DrainEvents()

	ship, _ := s.world.GetBody(p.Ship)
	s.addRock(ship.Pos.Add(ship.Rot.Forward().Scale(5)), 30, 2, 400)

	s.SetInput(p.ID, Input{Seq: 1, Grab: true})
	s.stepN(5)

	if p.Stats.Delivered != 1 {
		t.Errorf("Delivered = %d, want 1", p.Stats.Delivered)
	}
	if p.Stats.Banked != 400 {
		t.Errorf("Banked = %v, want 400", p.Stats.Banked)
	}
}

// A rock two pilots dragged home is a delivery for both, worth what each was actually
// paid — the same split the credits take. Crediting only whoever tripped the deposit
// radius would make the second beam on a massive look like it did nothing.
func TestASharedHaulCountsForEveryHolder(t *testing.T) {
	s := newBareSim(t, nil)
	a := s.AddPlayer("a", 0)
	b := s.AddPlayer("b", 0)

	// Lock on out in the belt first. Staging this inside the deposit volume banks the
	// rock on the tick the first beam catches it, before the second one has locked —
	// which is a single-hauler delivery wearing a two-hauler test's clothes.
	shipA, _ := s.world.GetBody(a.Ship)
	s.world.SetPosition(b.Ship, shipA.Pos.Add(shipA.Rot.Right().Scale(5)))
	s.stepN(1)

	rock := s.addRock(shipA.Pos.Add(shipA.Rot.Forward().Scale(10)), 30, 2, 400)
	s.SetInput(a.ID, Input{Seq: 1, Grab: true})
	s.SetInput(b.ID, Input{Seq: 1, Grab: true})
	s.stepN(3)

	if o := s.objects[rock]; len(o.Holders) != 2 {
		t.Fatalf("staging failed: holders = %v, want both players", o.Holders)
	}
	_ = s.DrainEvents()

	// Now haul it home: move both ships and the cargo into the deposit volume together.
	home := s.homeOf(a)
	dest := home.Add(physics.Vec3{X: 40, Y: 0, Z: 0})
	s.world.SetPosition(a.Ship, dest)
	s.world.SetPosition(b.Ship, dest.Add(shipA.Rot.Right().Scale(5)))
	s.world.SetPosition(rock, dest.Add(shipA.Rot.Forward().Scale(10)))
	s.stepN(2)

	for _, p := range []*Player{a, b} {
		if p.Stats.Delivered != 1 {
			t.Errorf("%s Delivered = %d, want 1", p.Name, p.Stats.Delivered)
		}
		if p.Stats.Banked != 200 {
			t.Errorf("%s Banked = %v, want half of 400", p.Name, p.Stats.Banked)
		}
	}
}

func TestAKillIsCountedBothWays(t *testing.T) {
	s := newBareSim(t, nil)
	killer := s.AddPlayer("killer", 0)
	victim := s.AddPlayer("victim", 1)

	s.destroyShip(victim, killer)

	if killer.Stats.Kills != 1 {
		t.Errorf("killer Kills = %d, want 1", killer.Stats.Kills)
	}
	if killer.Stats.Deaths != 0 {
		t.Errorf("killer Deaths = %d, want 0", killer.Stats.Deaths)
	}
	if victim.Stats.Deaths != 1 {
		t.Errorf("victim Deaths = %d, want 1", victim.Stats.Deaths)
	}
	if victim.Stats.Kills != 0 {
		t.Errorf("victim Kills = %d, want 0", victim.Stats.Kills)
	}
}

// A ram destroys both ships and calls destroyShip once each way. Both pilots should come
// out of it with a kill and a death — that is the trade ramming actually is.
func TestARamCostsBothPilotsAShip(t *testing.T) {
	s := newBareSim(t, nil)
	a := s.AddPlayer("a", 0)
	b := s.AddPlayer("b", 1)

	s.destroyShip(a, b)
	s.destroyShip(b, a)

	for _, p := range []*Player{a, b} {
		if p.Stats.Kills != 1 || p.Stats.Deaths != 1 {
			t.Errorf("%s came out of a ram with %d kills and %d deaths, want 1 and 1",
				p.Name, p.Stats.Kills, p.Stats.Deaths)
		}
	}
}

// A station turret has no player behind it, so the death is counted with no kill to
// credit. Inventing one would put a teammate's name on a kill they did not make.
func TestAStationKillCountsADeathAndNoKill(t *testing.T) {
	s := newBareSim(t, nil)
	victim := s.AddPlayer("victim", 0)

	s.destroyShipByStation(victim, 1)

	if victim.Stats.Deaths != 1 {
		t.Errorf("Deaths = %d, want 1", victim.Stats.Deaths)
	}
	total := 0
	for _, p := range s.players {
		total += p.Stats.Kills
	}
	if total != 0 {
		t.Errorf("a station kill credited %d player kills, want 0", total)
	}
}

// Results are sorted on the server because Go map iteration is random: without it the
// same match would draw a differently ordered table for every player watching it.
func TestResultsAreSortedByCrewThenBanked(t *testing.T) {
	s := newBareSim(t, func(c *Config) { c.TeamCount, c.TeamSize = 2, 2 })

	a0 := s.AddPlayer("a0", 0)
	a1 := s.AddPlayer("a1", 0)
	b0 := s.AddPlayer("b0", 1)
	b1 := s.AddPlayer("b1", 1)

	a0.Stats.Banked, a1.Stats.Banked = 100, 900
	b0.Stats.Banked, b1.Stats.Banked = 700, 50

	// Sorted the same way however many times it is asked for.
	for i := 0; i < 20; i++ {
		rows := s.Results()
		want := []string{"a1", "a0", "b0", "b1"}
		for j, name := range want {
			if rows[j].Name != name {
				t.Fatalf("row %d is %q, want %q (pass %d)", j, rows[j].Name, name, i)
			}
		}
	}

	_ = b1
}
