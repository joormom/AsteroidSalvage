package sim

// Best-of-N match structure.
//
// A match is a sequence of timed rounds separated by intermissions. Each round is scored
// independently: the team with the most banked value when the clock runs out takes the
// round, and the first team to win a majority takes the match. Team scores reset every
// round; personal credits and upgrades do not, which is what makes an early round matter
// even if you lose it.
//
// The phase machine is driven from Sim.Step at the fixed tick rate rather than from wall
// clock time, so it stays deterministic and in lockstep with the physics.

type Phase uint8

const (
	PhaseWarmup       Phase = 0 // waiting to start; players can fly around
	PhaseRound        Phase = 1 // scoring
	PhaseIntermission Phase = 2 // shop is open, field cleared
	PhaseMatchOver    Phase = 3
)

// MatchState is the whole tournament layer.
type MatchState struct {
	Phase Phase
	Round int // 1-based once play starts

	// ticksLeft counts down the current phase.
	ticksLeft int

	// RoundWins is per team; first to WinsNeeded takes the match.
	RoundWins map[uint8]int

	// Winner is set once the match ends. 0xFF means a draw.
	Winner uint8
}

// MatchConfig sizes the tournament.
type MatchConfig struct {
	BestOf           int // 5 => first to 3 rounds
	RoundSeconds     int
	IntermissionSecs int
	WarmupSeconds    int
	DisableMatchFlow bool // tests and free-play sandboxes want a single endless round

	// Lives is each team's shared pool of respawns per round. Clamped to
	// [MinLives, MaxLives] when a round starts — see lives.go.
	Lives int

	// Mode is which game is being played, and Modes carries that mode's own settings.
	// See modes.go.
	Mode  GameMode
	Modes ModeConfig
}

// DefaultMatchConfig is the standard best-of-5.
func DefaultMatchConfig() MatchConfig {
	return MatchConfig{
		BestOf:           5,
		RoundSeconds:     180,
		IntermissionSecs: 45,
		WarmupSeconds:    15,
		Lives:            DefaultLives,
		Mode:             ModeSalvage,
		Modes:            DefaultModeConfig(),
	}
}

// WinsNeeded is the majority of BestOf.
func (c MatchConfig) WinsNeeded() int { return c.BestOf/2 + 1 }

// TimeLeftSeconds is what the HUD shows.
func (m *MatchState) TimeLeftSeconds() int {
	if m.ticksLeft <= 0 {
		return 0
	}
	return (m.ticksLeft + TickHz - 1) / TickHz
}

// Match exposes the tournament state.
func (s *Sim) Match() MatchState { return *s.match }

// MatchConfig exposes the tournament sizing.
func (s *Sim) MatchConfig() MatchConfig { return s.matchCfg }

func (s *Sim) initMatch() {
	s.match = &MatchState{
		Phase:     PhaseWarmup,
		RoundWins: map[uint8]int{},
		Winner:    0xFF,
	}
	for id := range s.teams {
		s.match.RoundWins[id] = 0
	}
	s.match.ticksLeft = s.matchCfg.WarmupSeconds * TickHz
	// Filled in before the first round so the HUD has a real number during warmup rather
	// than showing every crew on zero lives.
	s.resetLives()

	if s.matchCfg.DisableMatchFlow {
		// Sandbox: one endless round, no clock, no intermissions.
		s.match.Phase = PhaseRound
		s.match.Round = 1
		s.match.ticksLeft = 1 << 30
	}
}

// advanceMatch runs the phase machine one tick. Called from Step.
func (s *Sim) advanceMatch() {
	if s.matchCfg.DisableMatchFlow || s.match.Phase == PhaseMatchOver {
		return
	}

	s.match.ticksLeft--
	if s.match.ticksLeft > 0 {
		return
	}

	switch s.match.Phase {
	case PhaseWarmup:
		s.beginRound(1)

	case PhaseRound:
		s.endRound()

	case PhaseIntermission:
		s.beginRound(s.match.Round + 1)
	}
}

func (s *Sim) beginRound(n int) {
	s.match.Phase = PhaseRound
	s.match.Round = n
	s.match.ticksLeft = s.matchCfg.RoundSeconds * TickHz

	// Fresh field and a clean scoreboard: each round is its own contest.
	for _, t := range s.teams {
		t.Score = 0
	}
	s.restoreMotherships()
	s.resetLives()
	// A fresh hill each round, so the crew that lost the last one does not also inherit
	// the other crew's position on it.
	if s.matchCfg.Mode == ModeKingOfTheHill {
		s.hillTeam, s.hillContested = NoTeam, false
		s.placeHill()
	}
	s.clearSalvage()
	s.wave = 0
	s.SpawnWave()

	// Everyone starts empty-handed at a spawn point, so nobody carries an advantage
	// across the intermission. Anyone grounded by the last round's life pool flies again.
	for _, p := range s.players {
		if p.Held != 0 {
			s.release(p, EventDropped)
		}
		p.grounded = false
		s.resetShip(p)
	}

	s.emit(Event{Type: EventRoundStart, Value: float32(n)})
}

func (s *Sim) endRound() {
	// Highest banked value takes the round; a tie awards nobody.
	var best uint8 = 0xFF
	var bestScore float32 = -1
	tied := false

	for id, t := range s.teams {
		switch {
		case t.Score > bestScore:
			best, bestScore, tied = id, t.Score, false
		case t.Score == bestScore:
			tied = true
		}
	}

	if !tied && best != 0xFF && bestScore > 0 {
		s.match.RoundWins[best]++
		s.emit(Event{Type: EventRoundEnd, Player: PlayerID(best), Value: bestScore})
	} else {
		s.emit(Event{Type: EventRoundEnd, Player: PlayerID(0xFF), Value: bestScore})
	}

	s.concludeRound()
}

// endRoundByElimination ends the round because every other crew has run out of lives and
// has nobody left flying. Same shape as a breach: the last team standing takes it.
func (s *Sim) endRoundByElimination(winnerTeam uint8) {
	if s.matchCfg.DisableMatchFlow || s.match.Phase != PhaseRound {
		return
	}

	var score float32
	if t, ok := s.teams[winnerTeam]; ok {
		score = t.Score
	}
	s.match.RoundWins[winnerTeam]++
	s.emit(Event{Type: EventRoundEnd, Player: PlayerID(winnerTeam), Value: score})

	s.concludeRound()
}

// concludeRound is everything that happens once a round's winner is decided: end the
// match if someone has the majority, otherwise open the shop.
func (s *Sim) concludeRound() {
	// Match over?
	needed := s.matchCfg.WinsNeeded()
	for id, wins := range s.match.RoundWins {
		if wins >= needed {
			s.match.Phase = PhaseMatchOver
			s.match.Winner = id
			s.match.ticksLeft = 0
			s.clearSalvage()
			s.emit(Event{Type: EventMatchOver, Player: PlayerID(id), Value: float32(wins)})
			return
		}
	}

	if s.match.Round >= s.matchCfg.BestOf {
		// All rounds played without a majority — most round wins takes it.
		s.match.Phase = PhaseMatchOver
		s.match.Winner = s.leaderOnRoundWins()
		s.match.ticksLeft = 0
		s.clearSalvage()
		s.emit(Event{Type: EventMatchOver, Player: PlayerID(s.match.Winner), Value: 0})
		return
	}

	// Otherwise: shop time. The field is cleared so the intermission is not a free
	// head start for whoever is already parked next to a good rock.
	s.match.Phase = PhaseIntermission
	s.match.ticksLeft = s.matchCfg.IntermissionSecs * TickHz
	s.clearSalvage()
	for _, p := range s.players {
		if p.Held != 0 {
			s.release(p, EventDropped)
		}
		// Fresh randomised choices every intermission.
		p.Offers = s.rollOffers(p)
	}
}

func (s *Sim) leaderOnRoundWins() uint8 {
	var best uint8 = 0xFF
	bestWins := -1
	tied := false
	for id, wins := range s.match.RoundWins {
		switch {
		case wins > bestWins:
			best, bestWins, tied = id, wins, false
		case wins == bestWins:
			tied = true
		}
	}
	if tied {
		return 0xFF
	}
	return best
}

// clearSalvage removes every asteroid, leaving ships and the mothership.
func (s *Sim) clearSalvage() {
	for e, o := range s.objects {
		if o.Kind == KindAsteroid || o.Kind == KindSalvage {
			s.world.Despawn(e)
			delete(s.objects, e)
		}
	}
}

// resetShip returns a player to a spawn point, stationary.
func (s *Sim) resetShip(p *Player) {
	pos := s.spawnPointFor(p.ID, p.Team)
	s.world.SetPosition(p.Ship, pos)
	s.world.SetVelocity(p.Ship, physicsZero)

	// A new round is a clean slate: full hull, full charge. Carrying battle damage
	// across an intermission would punish the team that had to fight for the last round.
	if o, ok := s.objects[p.Ship]; ok {
		o.Health = o.MaxHealth
	}
	p.respawnIn = 0
	p.grounded = false
	p.Energy = p.MaxEnergy()
	p.Boost = p.MaxBoost()
	p.boostLocked = false
	p.boostCooldown = 0
}

// scoringActive reports whether deliveries should count right now.
func (s *Sim) scoringActive() bool {
	return s.matchCfg.DisableMatchFlow || s.match.Phase == PhaseRound
}
