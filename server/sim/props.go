package sim

import (
	"math"

	"asteroidsalvage/physics"
)

// Map scenery: the big derelict structures a match is flown around.
//
// The belt on its own is a uniform cloud of rocks in an empty sphere, which is readable
// but featureless — every direction looks like every other direction, and there is
// nothing to use. Props give the volume landmarks you can navigate by, cover you can
// break line of sight behind, and something for a chase to go around.
//
// They are static, indestructible and solid. Indestructible on purpose: cover that can be
// deleted stops being cover, and a wreck the size of a station coming apart would be a
// physics problem rather than a feature. What they are is *in the way*, which is the whole
// point — a laser stops at one, and so does a ship.
//
// Layout is seeded, so the same seed is the same map: hosts can share one, and a bug in
// generation can be reproduced rather than hunted.

// PropKind selects which structure the client draws. Travels in the snapshot's tier byte,
// which is unused for props, so scenery costs nothing extra on the wire.
type PropKind uint8

const (
	PropBattlestation PropKind = 0 // a cracked armoured sphere with a dish
	PropCapitalWreck  PropKind = 1 // the broken spine of something enormous
	PropPlanetChunk   PropKind = 2 // a shard of a world that did not survive
	PropStationRuin   PropKind = 3 // a snapped-off ring station

	propKindCount = 4
)

// PropSpec bounds one structure type.
type PropSpec struct {
	Kind PropKind
	Name string

	MinRadius float32
	MaxRadius float32

	// Weight is the relative frequency in a map. The battlestation is rarer than the
	// rest because it is the biggest landmark on the field and two of them stops either
	// one being "the" landmark.
	Weight float32
}

// PropSpecs is the scenery table.
var PropSpecs = []PropSpec{
	{PropBattlestation, "battlestation", 130, 190, 1},
	{PropCapitalWreck, "capital wreck", 90, 150, 3},
	{PropPlanetChunk, "planet chunk", 70, 130, 3},
	{PropStationRuin, "station ruin", 60, 110, 3},
}

const (
	// PropCount is how many structures a map gets. Enough that there is a landmark in
	// most directions from the middle, few enough that the belt is still mostly open
	// space to fly through.
	PropCount = 9

	// propKeepOut is the clearance a structure needs from a mothership hull and from
	// every other structure, in metres. Generous: these are the largest bodies in the
	// world, and two of them interpenetrating is both ugly and a physics problem.
	propKeepOut = 220.0

	// propRingMin and propRingMax bound how far out scenery is placed. Inside the
	// mothership ring (520 m) as well as outside it, so the middle of the map has
	// structure rather than being a featureless bowl with decoration around the rim.
	propRingMin = 620.0
	propRingMax = 1250.0

	// propSpawnAttempts is how many placements are tried before giving up on one prop.
	// Failing to place is fine — a map with eight structures instead of nine is not worth
	// an infinite loop.
	propSpawnAttempts = 60
)

/*============================================================================
 * The centrepiece
 *
 * Every map is built around exactly one enormous structure sitting in the middle of the
 * volume, and which of the four it is changes from match to match. That single choice is
 * what makes a map feel like a place rather than a random scattering: crews describe a
 * game as "the one with the broken world in the middle", the belt is worked around it,
 * and it is visible from any station on the ring.
 *===========================================================================*/

const (
	// CentrepieceMinRadius and CentrepieceMaxRadius bound the landmark, in metres.
	//
	// The ceiling is set by geometry, not taste: stations sit on a 520 m ring with 30 m
	// hulls, so anything past about 420 m of radius would be touching somebody's hangar.
	// 400 leaves a 90 m corridor at the tightest point, which is comfortable to fly and
	// still means the thing fills the sky from the middle of the map.
	CentrepieceMinRadius = 260.0
	CentrepieceMaxRadius = 400.0

	// centrepieceDrift is how far off dead centre it may sit. A landmark exactly on the
	// origin makes every station's approach identical; a little asymmetry gives the map
	// a near side and a far side.
	centrepieceDrift = 90.0

	// centrepieceKeepOut is the clear space demanded around it for asteroid spawning, on
	// top of both radii. Rocks generated inside a 400 m derelict are unreachable, and
	// rocks generated just outside one are the interesting ones.
	centrepieceKeepOut = 60.0
)

// Centrepiece returns the map's landmark, or nil if this map has none.
func (s *Sim) Centrepiece() *Object {
	if s.centrepiece == 0 {
		return nil
	}
	return s.objects[s.centrepiece]
}

// spawnCentrepiece places the map's single defining structure.
func (s *Sim) spawnCentrepiece() {
	spec := PropSpecs[s.rng.Intn(len(PropSpecs))]
	radius := CentrepieceMinRadius +
		s.rng.Float32()*(CentrepieceMaxRadius-CentrepieceMinRadius)

	pos := physics.Vec3{
		X: (s.rng.Float32()*2 - 1) * centrepieceDrift,
		Y: (s.rng.Float32()*2 - 1) * centrepieceDrift,
		Z: (s.rng.Float32()*2 - 1) * centrepieceDrift * 0.4,
	}

	e := s.world.SpawnSphere(0, radius, pos)
	if e == 0 {
		return
	}
	s.world.SetMaterial(e, ShipRestitution, 0.25)

	s.objects[e] = &Object{
		Entity: e, Kind: KindProp, Radius: radius,
		Tier: Tier(spec.Kind), Team: NoTeam, Integrity: 1,
	}
	s.centrepiece = e
	s.centrepiecePos = pos
	s.centrepieceRadius = radius
}

// clearOfCentrepiece reports whether a body of this radius can be placed here without
// ending up inside the landmark.
func (s *Sim) clearOfCentrepiece(pos physics.Vec3, radius float32) bool {
	if s.centrepiece == 0 {
		return true
	}
	need := s.centrepieceRadius + radius + centrepieceKeepOut
	return pos.Sub(s.centrepiecePos).Len() >= need
}

// Props returns every scenery body currently in the world.
func (s *Sim) Props() []*Object {
	out := make([]*Object, 0, PropCount)
	for _, o := range s.objects {
		if o.Kind == KindProp {
			out = append(out, o)
		}
	}
	return out
}

// spawnProps lays out a map's scenery. Called once at world creation — props outlive
// rounds, because the map is the map.
func (s *Sim) spawnProps() {
	if s.cfg.PropCount <= 0 {
		return
	}

	s.spawnCentrepiece()

	placed := make([]physics.Vec3, 0, s.cfg.PropCount)
	radii := make([]float32, 0, s.cfg.PropCount)

	for i := 0; i < s.cfg.PropCount; i++ {
		spec := s.pickPropSpec()
		radius := spec.MinRadius + s.rng.Float32()*(spec.MaxRadius-spec.MinRadius)

		pos, ok := s.findPropSpot(radius, placed, radii)
		if !ok {
			continue // the map is full; eight landmarks is still a map
		}

		e := s.world.SpawnSphere(0, radius, pos) // mass 0: static, like a mothership
		if e == 0 {
			return // storage full
		}
		s.world.SetMaterial(e, ShipRestitution, 0.25)

		s.objects[e] = &Object{
			Entity: e, Kind: KindProp, Radius: radius,
			Tier: Tier(spec.Kind), Team: NoTeam, Integrity: 1,
			// No health at all: MaxHealth 0 is what tells the snapshot encoder this is
			// not a thing with a hull bar, and what keeps it off the shootable list.
		}

		placed = append(placed, pos)
		radii = append(radii, radius)
	}
}

// pickPropSpec chooses a structure type by weight.
func (s *Sim) pickPropSpec() PropSpec {
	var total float32
	for _, sp := range PropSpecs {
		total += sp.Weight
	}
	roll := s.rng.Float32() * total
	for _, sp := range PropSpecs {
		roll -= sp.Weight
		if roll <= 0 {
			return sp
		}
	}
	return PropSpecs[0]
}

// findPropSpot rejection-samples a position clear of the stations and of everything
// already placed.
func (s *Sim) findPropSpot(radius float32, placed []physics.Vec3,
	radii []float32) (physics.Vec3, bool) {

	for attempt := 0; attempt < propSpawnAttempts; attempt++ {
		// Uniform on a shell, flattened toward the plane the stations sit in so the map
		// reads as a disc with depth rather than a sphere of clutter.
		theta := s.rng.Float64() * 2 * math.Pi
		dist := propRingMin + s.rng.Float32()*(propRingMax-propRingMin)
		height := float32(s.rng.NormFloat64()) * 180

		pos := physics.Vec3{
			X: float32(math.Cos(theta)) * dist,
			Y: float32(math.Sin(theta)) * dist,
			Z: height,
		}

		if s.propClearOfStations(pos, radius) &&
			s.clearOfCentrepiece(pos, radius+propKeepOut) &&
			propClearOfOthers(pos, radius, placed, radii) {
			return pos, true
		}
	}
	return physics.Vec3{}, false
}

func (s *Sim) propClearOfStations(pos physics.Vec3, radius float32) bool {
	for _, e := range s.motherships {
		st, ok := s.states[e]
		if !ok {
			// States are not built until the first tick, so fall back to the ring
			// geometry the stations were placed on. Without this every prop is placed
			// against an empty map at startup and can land on a hangar door.
			for team := 0; team < len(s.motherships); team++ {
				home := s.mothershipPos(uint8(team), len(s.motherships))
				if pos.Sub(home).Len() < radius+MothershipRadius+propKeepOut {
					return false
				}
			}
			return true
		}
		if pos.Sub(st.Pos).Len() < radius+MothershipRadius+propKeepOut {
			return false
		}
	}
	return true
}

func propClearOfOthers(pos physics.Vec3, radius float32, placed []physics.Vec3,
	radii []float32) bool {

	for i, p := range placed {
		if pos.Sub(p).Len() < radius+radii[i]+propKeepOut {
			return false
		}
	}
	return true
}
