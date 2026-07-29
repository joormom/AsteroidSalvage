package sim

import "testing"

// kill destroys a player the way a laser would, going through the same death path so the
// life accounting under test is the real one.
func kill(s *Sim, victim, killer *Player) {
	s.destroyShip(victim, killer)
}

// killRepeatedly kills a player n times, putting them straight back in the seat between
// deaths rather than stepping out the respawn timer.
//
// Stepping RespawnTicks per death would run 4 seconds of match clock each time, and
// newMatchSim's rounds are 2 seconds long — the round kept ending mid-test, which reset
// the very life pool the test was counting.
func killRepeatedly(s *Sim, victim, killer *Player, n int) {
	for i := 0; i < n; i++ {
		if victim.Grounded() {
			return
		}
		if victim.Dead() {
			s.respawn(victim)
		}
		kill(s, victim, killer)
	}
}

// The pool has to actually count down, or it is decoration.
func TestDeathsSpendTeamLives(t *testing.T) {
	s := newMatchSim(t, func(c *Config) { c.Match.Lives = 8; c.Match.RoundSeconds = 120 })
	victim := s.AddPlayer("blue", 1)
	killer := s.AddPlayer("red", 0)

	s.stepN(TickHz + 2) // into a live round
	if got := s.LivesLeft(victim.Team); got != 8 {
		t.Fatalf("round started with %d lives, want 8", got)
	}

	kill(s, victim, killer)
	if got := s.LivesLeft(victim.Team); got != 7 {
		t.Errorf("after one death the team has %d lives, want 7", got)
	}
	if victim.Grounded() {
		t.Error("grounded with 7 lives still in the pool")
	}
}

// Spending the last life must stop the respawn, not merely delay it.
func TestLastLifeGroundsThePlayer(t *testing.T) {
	s := newMatchSim(t, func(c *Config) { c.Match.Lives = MinLives; c.Match.RoundSeconds = 120 })
	victim := s.AddPlayer("blue", 1)
	killer := s.AddPlayer("red", 0)

	// A crewmate who stays alive, so the round does not end the instant the victim is
	// grounded. This test is about one player not coming back; that the *round* ends
	// when a whole crew is wiped out is TestEliminatingEveryRivalWinsTheRound's job, and
	// letting it fire here would begin a new round and undo everything being asserted.
	mate := s.AddPlayer("blue-2", 1)
	if mate.Team != victim.Team {
		t.Fatalf("crewmate landed on team %d, want %d", mate.Team, victim.Team)
	}

	s.stepN(TickHz + 2)

	killRepeatedly(s, victim, killer, MinLives)

	if got := s.LivesLeft(victim.Team); got != 0 {
		t.Errorf("%d lives left after %d deaths, want 0", got, MinLives)
	}
	if !victim.Grounded() {
		t.Fatal("player is not grounded after the pool ran dry")
	}

	// And the wreck must stay a wreck: the respawn clock must not tick it back to life.
	s.stepN(RespawnTicks * 3)
	if !victim.Dead() {
		t.Error("a grounded player respawned anyway")
	}
	if !victim.Grounded() {
		t.Error("grounded flag cleared itself")
	}
}

// Wiping out the last rival ends the round, whatever the scoreboard said.
func TestEliminatingEveryRivalWinsTheRound(t *testing.T) {
	s := newMatchSim(t, func(c *Config) {
		c.TeamCount = 2
		c.Match.Lives = MinLives
		c.Match.RoundSeconds = 120
	})
	killer := s.AddPlayer("red", 0)
	victim := s.AddPlayer("blue", 1)
	if killer.Team == victim.Team {
		t.Fatalf("test needs two teams, both landed on %d", killer.Team)
	}
	s.stepN(TickHz + 2)

	// The losing side is well ahead on cargo. It should not save them.
	s.award(victim.Team, 9000)

	killRepeatedly(s, victim, killer, MinLives)

	if got := s.match.RoundWins[killer.Team]; got != 1 {
		t.Errorf("the surviving team has %d round wins, want 1", got)
	}
	if s.match.Phase != PhaseIntermission {
		t.Errorf("phase %v after a wipe-out, want the shop to open", s.match.Phase)
	}
}

// A new round has to give everyone their ships back.
func TestLivesAndGroundingResetEachRound(t *testing.T) {
	s := newMatchSim(t, func(c *Config) { c.Match.Lives = MinLives; c.Match.RoundSeconds = 120 })
	victim := s.AddPlayer("blue", 1)
	killer := s.AddPlayer("red", 0)
	s.stepN(TickHz + 2)

	killRepeatedly(s, victim, killer, MinLives)
	if !victim.Grounded() {
		t.Fatal("setup failed: player was never grounded")
	}

	// Force the round over and run the clock through the intermission into the next one,
	// rather than waiting out a round length the test does not care about.
	s.endRound()
	s.stepN(s.matchCfg.IntermissionSecs*TickHz + 4)

	if victim.Grounded() {
		t.Error("still grounded in the next round")
	}
	if got := s.LivesLeft(victim.Team); got != MinLives {
		t.Errorf("new round started on %d lives, want %d", got, MinLives)
	}
}

// Out-of-range values from a flag or an old config must not produce a broken round.
func TestLivesAreClamped(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{0, MinLives}, {3, MinLives}, {12, 12}, {99, MaxLives}, {-5, MinLives},
	} {
		if got := clampLives(tc.in); got != tc.want {
			t.Errorf("clampLives(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// Sandbox mode has no scoreboard to lose, so a life pool there would just strand a pilot.
func TestSandboxIgnoresLives(t *testing.T) {
	s := newBareSim(t, nil) // DisableMatchFlow
	victim := s.AddPlayer("blue", 1)
	killer := s.AddPlayer("red", 0)

	killRepeatedly(s, victim, killer, MaxLives+4)

	if victim.Grounded() {
		t.Error("a sandbox player was grounded by the life pool")
	}
}
