package sim

import (
	"math"

	"asteroidsalvage/physics"
)

// King of the Hill.
//
// One contested volume somewhere on the map. A crew scores for every second they are the
// only crew inside it — the moment a rival ship arrives the hill is *contested* and
// nobody scores at all, which is what turns the mode into a fight over position rather
// than a race to park.
//
// Three rules, and each of them is doing a job:
//
//   - Presence, not kills. A single surviving ship holds the hill against nobody, so a
//     crew that is losing the shooting can still win by being somewhere.
//   - Contested means zero for everyone. If the leader also scored while contested, the
//     correct play against a leading crew would be to leave, which is backwards.
//   - The hill moves. A fixed one becomes an entrenched position and a queue of people
//     flying into it; relocating forces everyone to travel and gives the crew that just
//     lost it an even start on the next one.
//
// The hill is not a physics body. It is a volume you fly *through*, and giving it a
// collider would make holding it a matter of bouncing off it. The snapshot carries it as
// a synthetic body so clients can draw it (see net.appendHillBody) without it existing in
// the world.

const (
	// HillRadius is the capture volume, in metres. Big enough that holding it is an area
	// to patrol rather than a pixel to sit on, small enough that one crew can plausibly
	// cover it and that "am I in it" is never ambiguous.
	HillRadius = 130.0

	// HillEntity is the id the hill is broadcast under. Far above anything the physics
	// world hands out, so it can never collide with a real body's id.
	HillEntity physics.EntityID = 0xFFFFFF01

	// hillRingMin and hillRingMax bound where a hill can appear: outside the centrepiece
	// but inside the mothership ring, so it is always contested ground rather than a spot
	// next to somebody's front door.
	hillRingMin = 200.0
	hillRingMax = 900.0

	// hillPlacementAttempts before falling back to the last spot that worked.
	hillPlacementAttempts = 80
)

// HillState is what the client needs to draw the hill and what the HUD reports.
type HillState struct {
	Pos    physics.Vec3
	Radius float32

	// Team is the crew currently scoring, or NoTeam when the hill is empty or contested.
	Team uint8

	// Contested is true when more than one crew has a ship inside. Distinct from an empty
	// hill: both score nothing, but they mean opposite things and the HUD says so.
	Contested bool
}

// Hill exposes the current control point. Only meaningful in King of the Hill.
func (s *Sim) Hill() HillState {
	return HillState{
		Pos: s.hillPos, Radius: HillRadius,
		Team: s.hillTeam, Contested: s.hillContested,
	}
}

// placeHill drops the control point somewhere legal and resets its timer.
func (s *Sim) placeHill() {
	for i := 0; i < hillPlacementAttempts; i++ {
		theta := s.rng.Float64() * 2 * math.Pi
		dist := hillRingMin + s.rng.Float32()*(hillRingMax-hillRingMin)

		pos := physics.Vec3{
			X: float32(math.Cos(theta)) * dist,
			Y: float32(math.Sin(theta)) * dist,
			Z: float32(s.rng.NormFloat64()) * 90,
		}

		// Not inside the landmark, and not on a hangar door — a hill overlapping a
		// station would hand that crew the round for sitting at home.
		if !s.clearOfCentrepiece(pos, HillRadius) {
			continue
		}
		if !s.hillClearOfStations(pos) {
			continue
		}

		s.hillPos = pos
		s.hillTicks = s.matchCfg.Modes.KothShiftSecs * TickHz
		s.emit(Event{Type: EventHillMoved, Value: HillRadius})
		return
	}

	// Nowhere legal was found in the attempts allowed. Keeping the previous spot is
	// strictly better than dropping the hill into a station or a derelict.
	s.hillTicks = s.matchCfg.Modes.KothShiftSecs * TickHz
}

func (s *Sim) hillClearOfStations(pos physics.Vec3) bool {
	for team := range s.motherships {
		home := s.mothershipPos(team, len(s.teams))
		if pos.Sub(home).Len() < HillRadius+MothershipRadius+120 {
			return false
		}
	}
	return true
}

// updateKoth scores the hill for one tick. Called from Step; a no-op in other modes.
func (s *Sim) updateKoth() {
	if s.matchCfg.Mode != ModeKingOfTheHill || !s.scoringActive() {
		return
	}

	// Relocate on schedule. A zero interval pins the hill for the whole round.
	if s.matchCfg.Modes.KothShiftSecs > 0 {
		s.hillTicks--
		if s.hillTicks <= 0 {
			s.placeHill()
		}
	}

	// Who is inside? Dead ships do not hold ground.
	inside := map[uint8]int{}
	for _, p := range s.players {
		if p.Dead() {
			continue
		}
		st, ok := s.states[p.Ship]
		if !ok {
			continue
		}
		if st.Pos.Sub(s.hillPos).Len() <= HillRadius {
			inside[p.Team]++
		}
	}

	switch len(inside) {
	case 0:
		s.hillTeam, s.hillContested = NoTeam, false
		return
	case 1:
		// Exactly one crew present: they hold it.
		for team := range inside {
			s.hillTeam, s.hillContested = team, false
			s.scoreHill(team)
		}
	default:
		// Two or more crews: nobody scores. Contesting a hill you cannot hold is a
		// legitimate and important play, which is why this is not "most ships wins".
		s.hillTeam, s.hillContested = NoTeam, true
	}
}

// scoreHill awards a tick of control and ends the round if that wins it.
func (s *Sim) scoreHill(team uint8) {
	rate := s.matchCfg.Modes.KothRate

	t, ok := s.teams[team]
	if !ok {
		return
	}
	t.Score += rate * TickDuration

	// Everyone on the crew is paid, not only whoever happens to be standing in the
	// volume: holding a hill is escorting, screening and chasing people off it, and
	// paying only the ship parked in the middle would reward the least useful job.
	if credits := s.matchCfg.Modes.KothCredits; credits > 0 {
		earned := credits * TickDuration
		for _, p := range s.players {
			if p.Team == team {
				p.Credits += earned
			}
		}
	}

	target := s.matchCfg.Modes.KothTarget
	if target <= 0 || s.matchCfg.DisableMatchFlow || s.match.Phase != PhaseRound {
		return
	}
	if t.Score >= target {
		s.match.RoundWins[team]++
		s.emit(Event{Type: EventRoundEnd, Player: PlayerID(team), Value: t.Score})
		s.concludeRound()
	}
}
