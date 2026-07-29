package sim

import "testing"

// Team capacity used to be advisory: pickTeam honoured it for a player's *preferred*
// crew and then fell back to "smallest team", which had no cap at all. The lobby draws a
// fixed number of slots per crew, so a join that can overrun them is a join that makes
// the lobby lie.

func TestTeamsFillToCapacityAndThenRefuse(t *testing.T) {
	s := newBareSim(t, func(c *Config) {
		c.TeamCount, c.TeamSize = 2, 3
	})

	for i := 0; i < 6; i++ {
		if p := s.AddPlayer("p", NoTeam); p == nil {
			t.Fatalf("player %d refused with seats left", i)
		}
	}

	if !s.Full() {
		t.Error("six players in two crews of three: Full() = false, want true")
	}
	if p := s.AddPlayer("late", NoTeam); p != nil {
		t.Errorf("seventh player seated on team %d, want refusal", p.Team)
	}

	for id, team := range s.teams {
		if team.Members != 3 {
			t.Errorf("team %d has %d members, want 3", id, team.Members)
		}
	}
}

func TestLeavingFreesASeat(t *testing.T) {
	s := newBareSim(t, func(c *Config) {
		c.TeamCount, c.TeamSize = 1, 2
	})

	first := s.AddPlayer("a", NoTeam)
	s.AddPlayer("b", NoTeam)
	if !s.Full() {
		t.Fatal("two players in a crew of two: not full")
	}

	s.RemovePlayer(first.ID)
	if s.Full() {
		t.Error("still full after someone left")
	}
	if s.AddPlayer("c", NoTeam) == nil {
		t.Error("refused a join into the freed seat")
	}
}

// Co-op is one crew holding the entire match, so its cap is TeamSize*TeamCount rather
// than TeamSize. Sizing it off len(s.teams) — which is 1 in co-op by definition — capped
// a sixteen-player co-op game at four.
func TestCoopCapacityIsTheWholeMatch(t *testing.T) {
	s := newBareSim(t, func(c *Config) {
		c.Coop, c.TeamCount, c.TeamSize = true, 4, 4
	})

	if got := s.TeamCapacity(); got != 16 {
		t.Fatalf("co-op capacity = %d, want 16", got)
	}
	for i := 0; i < 16; i++ {
		if p := s.AddPlayer("p", NoTeam); p == nil {
			t.Fatalf("co-op player %d refused below capacity", i)
		}
	}
	if s.AddPlayer("late", NoTeam) != nil {
		t.Error("seventeenth co-op player seated, want refusal")
	}
}

func TestFirstPlayerIsHost(t *testing.T) {
	s := newBareSim(t, nil)

	first := s.AddPlayer("host", NoTeam)
	second := s.AddPlayer("joiner", NoTeam)

	if s.HostID() != first.ID {
		t.Errorf("host = %d, want the first player %d", s.HostID(), first.ID)
	}

	// The button does not move when the host leaves: handing it to whoever is next
	// would let a joiner start a match somebody else was still setting up.
	s.RemovePlayer(first.ID)
	if s.HostID() == second.ID {
		t.Error("host passed to the joiner when the original host left")
	}
}

func TestLobbyHoldsUntilTheHostStartsIt(t *testing.T) {
	s := newBareSim(t, func(c *Config) {
		c.Match.DisableMatchFlow = false
		c.Match.Lobby = true
		c.Match.WarmupSeconds = 2
	})

	if s.Match().Phase != PhaseLobby {
		t.Fatalf("phase = %d, want PhaseLobby", s.Match().Phase)
	}

	host := s.AddPlayer("host", NoTeam)
	joiner := s.AddPlayer("joiner", NoTeam)

	// A lobby has no clock, so no amount of ticking may start the match on its own.
	for i := 0; i < TickHz*5; i++ {
		s.Step()
	}
	if s.Match().Phase != PhaseLobby {
		t.Errorf("phase = %d after five seconds, want it still held in PhaseLobby",
			s.Match().Phase)
	}

	if s.StartMatch(joiner.ID) {
		t.Error("a non-host started the match")
	}
	if s.Match().Phase != PhaseLobby {
		t.Fatalf("phase = %d after a rejected start, want PhaseLobby", s.Match().Phase)
	}

	if !s.StartMatch(host.ID) {
		t.Fatal("the host could not start the match")
	}
	if s.Match().Phase != PhaseWarmup {
		t.Errorf("phase = %d after starting, want PhaseWarmup", s.Match().Phase)
	}

	// And it is a one-way door: a second press does nothing.
	if s.StartMatch(host.ID) {
		t.Error("StartMatch succeeded twice")
	}
}

// Crews are chosen in the lobby, where you can see them filling. Switching mid-match
// would let somebody join whichever crew is winning, and would strand everything their
// old crew was counting on them for.
func TestTeamSwitchingIsLobbyOnly(t *testing.T) {
	s := newBareSim(t, func(c *Config) {
		c.Match.DisableMatchFlow = false
		c.Match.Lobby = true
		c.TeamCount, c.TeamSize = 3, 2
	})

	p := s.AddPlayer("pilot", 0)
	if p.Team != 0 {
		t.Fatalf("test setup: joined team %d", p.Team)
	}

	if !s.SetTeam(p.ID, 2) {
		t.Fatal("could not switch crew in the lobby")
	}
	if p.Team != 2 {
		t.Errorf("on team %d after switching to 2", p.Team)
	}
	// Member counts have to follow, or the seats the lobby draws stop matching reality.
	if s.teams[0].Members != 0 || s.teams[2].Members != 1 {
		t.Errorf("members after the move: team0=%d team2=%d",
			s.teams[0].Members, s.teams[2].Members)
	}
	// And the hull, which is what the snapshot and the "may I shoot this" test read.
	if o, ok := s.objects[p.Ship]; !ok || o.Team != 2 {
		t.Error("the ship still belongs to the old crew")
	}

	// Full crews refuse.
	a := s.AddPlayer("a", 1)
	b := s.AddPlayer("b", 1)
	if a.Team != 1 || b.Team != 1 {
		t.Fatalf("test setup: crews %d and %d", a.Team, b.Team)
	}
	if s.SetTeam(p.ID, 1) {
		t.Error("moved into a crew that was already at capacity")
	}

	// And once the match starts, nobody moves.
	if !s.StartMatch(s.HostID()) {
		t.Fatal("could not start the match")
	}
	if s.SetTeam(p.ID, 0) {
		t.Error("switched crew after the match had started")
	}
}

// Auto-assign spreads people across crews before doubling any up: the first four players
// are one per crew, the next four are each crew's second seat, and so on. Four crews
// filling evenly is what makes an early game playable rather than three-on-one.
func TestAutoAssignFillsCrewsBreadthFirst(t *testing.T) {
	s := newBareSim(t, func(c *Config) { c.TeamCount, c.TeamSize = 4, 4 })

	// Seat position within a crew, which is the slot the lobby draws them in.
	slot := map[uint8]int{}
	want := []struct {
		team uint8
		slot int
	}{
		{0, 1}, {1, 1}, {2, 1}, {3, 1},
		{0, 2}, {1, 2}, {2, 2}, {3, 2},
		{0, 3}, {1, 3}, {2, 3}, {3, 3},
		{0, 4}, {1, 4}, {2, 4}, {3, 4},
	}

	for i, w := range want {
		p := s.AddPlayer("p", NoTeam)
		if p == nil {
			t.Fatalf("player %d refused with seats left", i+1)
		}
		slot[p.Team]++
		if p.Team != w.team || slot[p.Team] != w.slot {
			t.Errorf("player %d landed on team %d slot %d, want team %d slot %d",
				i+1, p.Team, slot[p.Team], w.team, w.slot)
		}
	}

	if !s.Full() {
		t.Error("sixteen players in four crews of four: not full")
	}
}

func TestRosterIsOrderedByCrewThenJoinOrder(t *testing.T) {
	s := newBareSim(t, func(c *Config) {
		c.TeamCount, c.TeamSize = 2, 2
	})

	// Preferences, so the crews are known rather than assigned round-robin.
	s.AddPlayer("b1", 1)
	s.AddPlayer("a0", 0)
	s.AddPlayer("b2", 1)
	s.AddPlayer("a1", 0)

	rows := s.Roster()
	if len(rows) != 4 {
		t.Fatalf("roster has %d rows, want 4", len(rows))
	}

	want := []string{"a0", "a1", "b1", "b2"}
	for i, name := range want {
		if rows[i].Name != name {
			t.Errorf("row %d is %q, want %q", i, rows[i].Name, name)
		}
	}
}
