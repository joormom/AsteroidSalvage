package sim

// Game modes.
//
// A mode changes how points are earned and how a round is won. Everything else — the
// physics, the map, combat, lives, the match structure — is shared, which is deliberate:
// a mode that reimplemented flight or scoring would drift from the one that gets played
// most, and the whole appeal of a mode list is that they feel like the same game.
//
// Each mode carries its own options block so the host screen can offer different settings
// per mode rather than one union of everything. Options that genuinely apply to every
// mode (round length, lives, teams) stay in MatchConfig.

type GameMode uint8

const (
	// ModeSalvage is the original game: tractor a rock home and bank it at your station.
	ModeSalvage GameMode = 0

	// ModeHoard is the scramble. Rocks are collected by *flying into them* rather than
	// hauled, so there is no trip home and no cargo to protect — the whole round is a
	// race to be where the valuable rocks are. Named after the board game for a reason:
	// everyone is lunging at the same pile at once.
	ModeHoard GameMode = 1

	// ModeKingOfTheHill is the territory game: one contested volume somewhere on the
	// map, and a crew scores for every second they hold it alone. Holding is the whole
	// activity — the rocks are scenery, and a fight is about position rather than kills.
	ModeKingOfTheHill GameMode = 2
)

// ModeName is what the HUD and the host screen call a mode.
func (m GameMode) Name() string {
	switch m {
	case ModeHoard:
		return "HOARD"
	case ModeKingOfTheHill:
		return "KING OF THE HILL"
	default:
		return "SALVAGE"
	}
}

// ModeConfig holds the per-mode settings a host can edit.
//
// One struct with a field per mode rather than an interface: there are a handful of
// modes, the values are all scalars, and this keeps the whole thing copyable, comparable
// and trivially serialisable to a command line.
type ModeConfig struct {
	// HoardTarget ends a Hoard round early if a crew reaches it. Zero means the round
	// runs its full clock. A target is what stops a runaway leader from having to sit
	// through two more minutes of a decided round.
	HoardTarget float32

	// HoardPickupBonus scales the value of a rock collected by flying into it, relative
	// to what hauling the same rock home would have paid. Above 1 because a Hoard round
	// has no delivery leg: the rock is worth less effort, so it is worth more per rock
	// to keep round scores in the same range as Salvage.
	HoardPickupBonus float32

	// KothTarget ends a King of the Hill round once a crew reaches it. This is the main
	// pacing dial for the mode: with the default rate it is a bit over a minute of
	// uninterrupted control, which nobody ever gets.
	KothTarget float32

	// KothRate is points per second for the crew holding the hill alone.
	KothRate float32

	// KothCredits is credits per second paid to each member of the holding crew.
	//
	// Separate from KothRate because they buy different things: round points win the
	// round and reset with it, while credits are personal, persist across the match and
	// are what the intermission shop spends. Holding the hill therefore pays twice —
	// once toward this round, once toward the rest of the match — which gives a crew that
	// has already lost a round on the clock a reason to keep contesting it.
	KothCredits float32

	// KothShiftSecs is how often the hill moves to a new spot. Zero pins it in place for
	// the whole round. A hill that never moves turns into one entrenched position and a
	// queue of people flying into it; moving it periodically forces everyone to travel
	// and hands the losing crew a fresh start.
	KothShiftSecs int
}

// DefaultModeConfig is the tuned starting point for every mode.
func DefaultModeConfig() ModeConfig {
	return ModeConfig{
		HoardTarget:      4000,
		HoardPickupBonus: 1.6,
		KothTarget:       500,
		KothRate:         7,
		KothCredits:      20,
		KothShiftSecs:    45,
	}
}

// Mode returns the mode this match is playing.
func (s *Sim) Mode() GameMode { return s.matchCfg.Mode }

/*============================================================================
 * Hoard
 *===========================================================================*/

// hoardPickupRange is the extra reach on a collection, beyond the two radii touching.
//
// A little forgiveness, because "did I touch it" at speed is otherwise a coin toss and
// missing a rock you clearly flew through is the single most annoying thing a mode like
// this can do.
const hoardPickupRange = 3.0

// updateHoard collects any rock a ship is touching. Called from Step in Hoard mode only.
//
// Deliberately not driven off the collision buffer: a rock is collected when you *reach*
// it, and a contact that arrives a tick after the visual overlap reads as a miss. This
// runs against positions instead, so it triggers on the frame it looks like it should.
func (s *Sim) updateHoard() {
	if s.matchCfg.Mode != ModeHoard || !s.scoringActive() {
		return
	}

	for _, p := range s.players {
		if p.Dead() {
			continue
		}
		ship, ok := s.states[p.Ship]
		if !ok {
			continue
		}

		for e, o := range s.objects {
			if o.Kind != KindAsteroid && o.Kind != KindSalvage {
				continue
			}
			st, ok := s.states[e]
			if !ok {
				continue
			}
			if st.Pos.Sub(ship.Pos).Len() > ShipRadius+o.Radius+hoardPickupRange {
				continue
			}

			s.collectRock(p, o)
		}
	}
}

// collectRock banks a rock on contact and removes it from the world.
func (s *Sim) collectRock(p *Player, o *Object) {
	bonus := s.matchCfg.Modes.HoardPickupBonus
	if bonus <= 0 {
		bonus = 1
	}
	// Integrity still counts: a rock somebody else has been bouncing off the scenery is
	// worth less, which keeps the shared-pile scramble from being purely about reflexes.
	value := o.Value * o.Integrity * bonus

	p.Credits += value
	if t, ok := s.teams[p.Team]; ok {
		t.Score += value
	}

	s.emit(Event{
		Type: EventDeposited, Entity: o.Entity, Player: p.ID, Value: value,
	})

	s.world.Despawn(o.Entity)
	delete(s.objects, o.Entity)

	s.checkHoardTarget()
}

// checkHoardTarget ends the round early once a crew has run away with it.
func (s *Sim) checkHoardTarget() {
	target := s.matchCfg.Modes.HoardTarget
	if target <= 0 || s.matchCfg.DisableMatchFlow || s.match.Phase != PhaseRound {
		return
	}
	for id, t := range s.teams {
		if t.Score >= target {
			s.match.RoundWins[id]++
			s.emit(Event{Type: EventRoundEnd, Player: PlayerID(id), Value: t.Score})
			s.concludeRound()
			return
		}
	}
}
