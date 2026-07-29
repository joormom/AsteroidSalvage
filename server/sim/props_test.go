package sim

import (
	"testing"

	"asteroidsalvage/physics"
)

func newMapSim(t *testing.T, mutate func(*Config)) *Sim {
	t.Helper()
	cfg := DefaultConfig()
	cfg.AutoRestock = false
	cfg.Match.DisableMatchFlow = true
	if mutate != nil {
		mutate(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// A map should actually have scenery on it.
func TestMapGeneratesProps(t *testing.T) {
	s := newMapSim(t, nil)

	props := s.Props()
	if len(props) < PropCount-2 {
		t.Errorf("generated %d props, want about %d", len(props), PropCount)
	}

	kinds := map[Tier]int{}
	for _, o := range props {
		kinds[o.Tier]++
		if o.Radius <= 0 {
			t.Errorf("prop with radius %v", o.Radius)
		}
		if o.MaxHealth != 0 {
			t.Errorf("prop has a health pool (%v); scenery is indestructible",
				o.MaxHealth)
		}
		if o.Team != NoTeam {
			t.Errorf("prop belongs to team %d", o.Team)
		}
	}
	if len(kinds) < 2 {
		t.Errorf("every prop is the same kind (%v); the map should vary", kinds)
	}
}

// Nothing the size of a battlestation may be generated on top of a hangar door.
func TestPropsKeepClearOfStationsAndEachOther(t *testing.T) {
	// Several seeds, because a placement bug that only shows on some layouts is exactly
	// the kind that reaches a player and not a test.
	for seed := int64(1); seed <= 8; seed++ {
		s := newMapSim(t, func(c *Config) { c.Seed = seed })

		props := s.Props()
		positions := make([]physics.Vec3, 0, len(props))
		radii := make([]float32, 0, len(props))

		for _, o := range props {
			st, ok := s.states[o.Entity]
			if !ok {
				// States are built on the first tick; step once so this reads real
				// positions rather than skipping the check entirely.
				s.stepN(1)
				st = s.states[o.Entity]
			}

			for team, e := range s.motherships {
				ms, ok := s.states[e]
				if !ok {
					continue
				}
				gap := st.Pos.Sub(ms.Pos).Len() - o.Radius - MothershipRadius
				if gap < 0 {
					t.Errorf("seed %d: a %.0f m prop overlaps team %d's station by %.0f m",
						seed, o.Radius, team, -gap)
				}
			}

			for i, p := range positions {
				gap := st.Pos.Sub(p).Len() - o.Radius - radii[i]
				if gap < 0 {
					t.Errorf("seed %d: two props overlap by %.0f m", seed, -gap)
				}
			}
			positions = append(positions, st.Pos)
			radii = append(radii, o.Radius)
		}
	}
}

// The same seed has to be the same map, or a host cannot share one and a generation bug
// cannot be reproduced.
func TestMapLayoutIsSeeded(t *testing.T) {
	layout := func(seed int64) []physics.Vec3 {
		s := newMapSim(t, func(c *Config) { c.Seed = seed })
		s.stepN(1)
		var out []physics.Vec3
		for _, o := range s.Props() {
			out = append(out, s.states[o.Entity].Pos)
		}
		return out
	}

	a, b := layout(7), layout(7)
	if len(a) != len(b) {
		t.Fatalf("same seed produced %d and %d props", len(a), len(b))
	}
	// Maps are keyed by entity id, so iteration order varies between runs; compare the
	// sets by summing, which is order-independent and still catches a moved prop.
	var sumA, sumB physics.Vec3
	for i := range a {
		sumA = sumA.Add(a[i])
		sumB = sumB.Add(b[i])
	}
	if sumA.Sub(sumB).Len() > 0.01 {
		t.Error("the same seed generated a different map")
	}

	if c := layout(8); len(c) > 0 && len(a) > 0 {
		var sumC physics.Vec3
		for i := range c {
			sumC = sumC.Add(c[i])
		}
		if sumA.Sub(sumC).Len() < 0.01 {
			t.Error("two different seeds generated the same map")
		}
	}
}

// Scenery is cover: a beam has to stop at it and take nothing from it.
func TestPropsBlockLasersWithoutTakingDamage(t *testing.T) {
	s := newMapSim(t, func(c *Config) { c.PropCount = 0 })
	shooter := s.AddPlayer("red", 0)
	victim := s.AddPlayer("blue", 1)

	// Put a wreck squarely between the two ships.
	s.world.SetPosition(shooter.Ship, physics.Vec3{X: 0, Y: 0, Z: 700})
	s.world.SetVelocity(shooter.Ship, physics.Vec3{})
	s.stepN(1)

	e := s.world.SpawnSphere(0, 40, placeInFrontOf(s, shooter, 90))
	s.objects[e] = &Object{
		Entity: e, Kind: KindProp, Radius: 40, Team: NoTeam, Integrity: 1,
	}
	s.world.SetPosition(victim.Ship, placeInFrontOf(s, shooter, 200))
	s.world.SetVelocity(victim.Ship, physics.Vec3{})
	s.stepN(1)

	before := s.objects[victim.Ship].Health
	s.SetInput(shooter.ID, Input{Seq: 1, Fire: true})
	s.stepN(ShotCooldownTicks * 4)

	if got := s.objects[victim.Ship].Health; got != before {
		t.Errorf("a shot got through a derelict: victim %v -> %v", before, got)
	}
	if s.objects[e].Health != 0 {
		t.Errorf("the derelict took damage (%v); scenery is indestructible",
			s.objects[e].Health)
	}
}

// Every map must have exactly one landmark, and it must be the biggest thing on it.
func TestMapHasOneHugeCentrepiece(t *testing.T) {
	for seed := int64(1); seed <= 6; seed++ {
		s := newMapSim(t, func(c *Config) { c.Seed = seed })
		s.stepN(1)

		centre := s.Centrepiece()
		if centre == nil {
			t.Fatalf("seed %d: no centrepiece", seed)
		}
		if centre.Radius < CentrepieceMinRadius || centre.Radius > CentrepieceMaxRadius {
			t.Errorf("seed %d: centrepiece radius %v outside [%v, %v]",
				seed, centre.Radius, float32(CentrepieceMinRadius),
				float32(CentrepieceMaxRadius))
		}

		for _, o := range s.Props() {
			if o.Entity != centre.Entity && o.Radius >= centre.Radius {
				t.Errorf("seed %d: a %v m prop is not smaller than the %v m centrepiece",
					seed, o.Radius, centre.Radius)
			}
		}

		// It has to clear every hangar, or a crew starts the match inside it.
		st := s.states[centre.Entity]
		for team, e := range s.motherships {
			ms, ok := s.states[e]
			if !ok {
				continue
			}
			gap := st.Pos.Sub(ms.Pos).Len() - centre.Radius - MothershipRadius
			if gap < 0 {
				t.Errorf("seed %d: the centrepiece overlaps team %d's station by %.0f m",
					seed, team, -gap)
			}
		}
	}
}

// Which structure the landmark is has to vary, or every map is the same map.
func TestCentrepieceKindVaries(t *testing.T) {
	seen := map[Tier]bool{}
	for seed := int64(1); seed <= 24; seed++ {
		s := newMapSim(t, func(c *Config) { c.Seed = seed })
		if c := s.Centrepiece(); c != nil {
			seen[c.Tier] = true
		}
	}
	if len(seen) < 3 {
		t.Errorf("24 seeds produced only %d kinds of centrepiece (%v); the map should "+
			"not always be the same landmark", len(seen), seen)
	}
}

// Rocks generated inside a 400 m derelict are simply gone: unreachable and unshootable.
func TestAsteroidsDoNotSpawnInsideTheCentrepiece(t *testing.T) {
	s := newMapSim(t, func(c *Config) { c.Seed = 3 })
	s.stepN(2)

	centre := s.Centrepiece()
	if centre == nil {
		t.Fatal("no centrepiece")
	}
	cpos := s.states[centre.Entity].Pos

	checked := 0
	for e, o := range s.objects {
		if o.Kind != KindAsteroid && o.Kind != KindSalvage {
			continue
		}
		st, ok := s.states[e]
		if !ok {
			continue
		}
		checked++
		if gap := st.Pos.Sub(cpos).Len() - centre.Radius - o.Radius; gap < 0 {
			t.Errorf("a %.1f m rock is %.0f m inside the centrepiece", o.Radius, -gap)
		}
	}
	if checked == 0 {
		t.Fatal("no asteroids to check")
	}
}
