package sim

import "asteroidsalvage/physics"

// Team lives.
//
// A shared pool of respawns per round, spent by anyone on the crew. It turns dying from a
// private four-second inconvenience into something the team pays for: at twelve lives a
// crew of four can afford three deaths each, and the pilot who keeps ramming is spending
// everybody's budget.
//
// Running out does not eject you from the match — it stops you coming back *this round*.
// The round then ends as soon as only one crew still has anyone able to fly, which is
// the same shape as a station breach: a decisive way to take a round that has nothing to
// do with who hauled more.
//
// Lives reset every round, like team scores and station hulls. A round you lost on lives
// costs you the round, not the match.

const (
	// MinLives and MaxLives bound what the host can dial in. Below six a round can end
	// before anyone reaches the belt; above twenty the pool stops being a constraint and
	// the mechanic may as well not exist.
	MinLives = 6
	MaxLives = 20

	// DefaultLives is the middle of that range in practice: enough that early deaths are
	// survivable, few enough that the count is worth watching by the end of a round.
	DefaultLives = 12
)

// clampLives keeps a configured value inside the supported range. Applied at the config
// boundary rather than trusted from a flag, so `-lives 0` cannot produce a round that is
// over before it starts.
func clampLives(n int) int {
	if n < MinLives {
		return MinLives
	}
	if n > MaxLives {
		return MaxLives
	}
	return n
}

// LivesLeft is a team's remaining respawns this round.
func (s *Sim) LivesLeft(team uint8) int {
	if t, ok := s.teams[team]; ok {
		return t.Lives
	}
	return 0
}

// resetLives refills every team's pool. Called at the start of each round.
func (s *Sim) resetLives() {
	n := clampLives(s.matchCfg.Lives)
	for _, t := range s.teams {
		t.Lives = n
	}
}

// spendLife deducts one respawn from a team and reports whether the pilot may come back.
//
// Called once per death, from the one place a ship's respawn timer is started, so every
// way of dying — laser, turret, ramming — costs the same and none of them can forget to
// charge for it.
func (s *Sim) spendLife(team uint8) bool {
	// Sandbox and mechanics tests run one endless round with no scoreboard to lose, so a
	// life pool there would just strand a player on the floor of the map forever.
	//
	// King of the Hill is the same in spirit: the round is decided by holding ground, and
	// a crew permanently grounded halfway through would spend the rest of it unable to
	// contest the only thing that scores.
	if s.matchCfg.DisableMatchFlow || s.matchCfg.Mode == ModeKingOfTheHill {
		return true
	}
	t, ok := s.teams[team]
	if !ok {
		return true
	}
	if t.Lives <= 0 {
		return false
	}
	t.Lives--
	return t.Lives > 0
}

// eliminateTeam knocks a crew out of the round outright.
//
// Used when a station is breached: the pool is emptied so nobody can come back, and every
// ship still flying is destroyed with the base. Going through the pool rather than adding
// a separate "out" flag means TeamEliminated, the scoreboard and the round-end check all
// keep working without knowing there is a second way to be knocked out.
func (s *Sim) eliminateTeam(team uint8) {
	t, ok := s.teams[team]
	if !ok {
		return
	}
	t.Lives = 0

	for _, p := range s.players {
		if p.Team != team {
			continue
		}
		if !p.Dead() {
			if p.Held != 0 {
				s.release(p, EventDropped)
			}
			if o, ok := s.objects[p.Ship]; ok {
				o.Health = 0
			}
			s.world.SetVelocity(p.Ship, physics.Vec3{})
			s.world.SetPosition(p.Ship, physics.Vec3{X: 0, Y: 0, Z: -100000})
			s.emit(Event{
				Type: EventShipDestroyed, Entity: p.Ship, Value: float32(team),
			})
		}
		p.respawnIn = 0
		p.grounded = true
	}
}

// TeamEliminated reports whether a team has run out of respawns and has nobody flying.
//
// Both halves matter: a crew on zero lives with pilots still alive is very much still in
// the round — they simply cannot afford another mistake.
func (s *Sim) TeamEliminated(team uint8) bool {
	t, ok := s.teams[team]
	if !ok || t.Lives > 0 {
		return false
	}
	for _, p := range s.players {
		if p.Team == team && !p.Dead() {
			return false
		}
	}
	return true
}

// checkElimination ends the round when only one crew is left standing.
//
// Called after any death. A team with no members at all is ignored rather than treated as
// eliminated, or a three-team server with one empty slot would end every round instantly.
func (s *Sim) checkElimination() {
	if s.matchCfg.DisableMatchFlow || s.match.Phase != PhaseRound {
		return
	}
	if len(s.teams) < 2 {
		return // co-op: there is nobody to be the last one standing
	}
	// King of the Hill is decided on points and nothing else, so being wiped out does
	// not end a round there — it costs you the hill for a while, which is punishment
	// enough and keeps the mode's one win condition its only one.
	if s.matchCfg.Mode == ModeKingOfTheHill {
		return
	}

	var alive []uint8
	for id := range s.teams {
		if s.teamHasMembers(id) && !s.TeamEliminated(id) {
			alive = append(alive, id)
		}
	}

	// Only decisive when exactly one crew remains. Zero means everyone was wiped out on
	// the same tick, which is a draw the clock can settle.
	if len(alive) == 1 {
		s.emit(Event{Type: EventTeamEliminated, Value: float32(alive[0])})
		s.endRoundByElimination(alive[0])
	}
}

func (s *Sim) teamHasMembers(team uint8) bool {
	for _, p := range s.players {
		if p.Team == team {
			return true
		}
	}
	return false
}
