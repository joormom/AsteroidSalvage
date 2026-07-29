package net

import (
	"context"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"asteroidsalvage/physics"
	"asteroidsalvage/sim"

	"github.com/kevinfling/sprocket"
)

// Snapshots go out every SnapshotEveryNTicks simulation ticks.
//
// 2 ticks gives an even 15 Hz. A nominal 20 Hz would need 1.5 ticks, which in practice
// alternates 33 ms / 67 ms — jittery spacing is worse for interpolation than a slightly
// lower but even rate. The client buffers ~100 ms regardless, so 15 Hz is smooth; raise
// this to 1 (30 Hz) if bandwidth ever proves cheaper than latency.
const SnapshotEveryNTicks = 2

const (
	teamStateEveryNTicks  = 15 // 2 Hz
	debugStatsEveryNTicks = 8  // ~4 Hz
)

const (
	roomMatch = "match"
	roomDebug = "debug"

	sessionKeyPlayer   = "pid"
	sessionKeyDebug    = "dbg"
	sessionKeyLastRecv = "rx"
)

// Read-deadline keepalive.
//
// sprocket sets a session's read deadline to now+PongWait when the connection opens and
// only ever extends it from its pong handler (session.go:241-246). Ordinary incoming
// messages do not refresh it. The gws transport's OnPing and OnPong are empty stubs
// (transports/gws/gws.go:73,75), so that pong handler never fires and the server never
// answers a client's ping either.
//
// The result is that every connection is dropped at exactly PongWait — 60 seconds —
// no matter how much traffic is flowing. The 16-bot load test missed it by running for
// exactly 60 seconds.
//
// So the deadline is refreshed here instead, driven by actual received messages. For a
// game whose clients send input 30 times a second that is a far better liveness signal
// than pongs: a client that has genuinely stopped talking is gone within IdleTimeout,
// and one that is playing never drops.
const (
	keepaliveInterval = 5 * time.Second
	clientIdleTimeout = 30 * time.Second
	readDeadlineGrant = 2 * clientIdleTimeout

	// Long enough that sprocket's pong-based session reaper can never fire. Not
	// time.Duration's maximum, which overflows once added to a timestamp.
	pongWaitDisabled = 30 * 24 * time.Hour
)

// Server owns the simulation and the WebSocket engine.
//
// The simulation is single-threaded by design. sprocket handlers run on connection
// goroutines, so they never touch the Sim directly — they post closures to cmds, which
// the tick loop drains at the top of each tick. That keeps the whole rules layer
// lock-free without any of it needing to know that the network exists.
type Server struct {
	engine *sprocket.Engine
	sim    *sim.Sim
	cfg    sim.Config

	devMode  bool
	feelPath string

	cmds chan func()

	started  time.Time
	snapBuf  []byte
	bodyBuf  []SnapshotBody
	sessions atomic.Int64
}

// Options configures a Server.
type Options struct {
	Sim      sim.Config
	DevMode  bool   // enables the /debug endpoint and DebugSet handling
	FeelPath string // config/feel.toml; loaded at boot, written on request
}

// NewServer builds the simulation and wires up the WebSocket engine.
func NewServer(opts Options) (*Server, error) {
	s, err := sim.New(opts.Sim)
	if err != nil {
		return nil, err
	}

	if opts.FeelPath != "" {
		if err := s.Tuning.Load(opts.FeelPath); err != nil {
			log.Printf("tuning: %v (continuing with defaults)", err)
		} else {
			log.Printf("tuning: loaded %s", opts.FeelPath)
		}
	}

	srv := &Server{
		sim:      s,
		cfg:      opts.Sim,
		devMode:  opts.DevMode,
		feelPath: opts.FeelPath,
		cmds:     make(chan func(), 1024),
		started:  time.Now(),
		snapBuf:  make([]byte, 0, 64*1024),
		bodyBuf:  make([]SnapshotBody, 0, 2048),
	}

	srv.engine = sprocket.New(
		// Inputs are 30 bytes; nothing a client sends should be anywhere near 4 KB.
		sprocket.WithMaxMessageSize(4096),
		sprocket.WithWriteFlushInterval(2*time.Millisecond),

		// Disable sprocket's pong-based liveness entirely — see the keepalive note
		// above. Its pingWorker closes any session whose lastPong is older than
		// PongWait, and because the gws transport's OnPong is an empty stub lastPong is
		// never updated after the connection opens. Every session was therefore killed
		// on the worker's second tick, around 108 seconds, however healthy it was.
		//
		// A PongWait far beyond any real session makes that check unreachable.
		// runKeepalive owns liveness instead, based on messages actually received.
		sprocket.WithPongWait(pongWaitDisabled),
	)

	srv.engine.HandleConnect(srv.onConnect)
	srv.engine.HandleDisconnect(srv.onDisconnect)
	srv.engine.HandleMessageBinary(srv.onBinary)
	srv.engine.HandleError(func(sess *sprocket.Session, err error) {
		log.Printf("session %s: %v", sess.ID, err)
	})

	return srv, nil
}

// Handler returns the HTTP mux serving both endpoints.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	play := func(w http.ResponseWriter, r *http.Request) {
		if err := s.engine.HandleRequest(w, r); err != nil {
			log.Printf("upgrade failed: %v", err)
		}
	}
	mux.HandleFunc("/ws", play)

	if s.devMode {
		mux.HandleFunc("/debug", play)
	}

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	return mux
}

// appendHillBody puts the King of the Hill control point in the snapshot.
//
// The hill is a volume you fly through, not an object — giving it a collider would make
// holding it a matter of bouncing off it — so it does not exist in the physics world and
// has no entity of its own. Sending it as a synthetic body means clients draw it through
// exactly the machinery they already have, with no new message and no special cadence.
//
// Team carries whoever currently holds it, which is what colours it, and 0xFF covers both
// empty and contested.
func (s *Server) appendHillBody() {
	if s.sim.Mode() != sim.ModeKingOfTheHill {
		return
	}
	h := s.sim.Hill()
	s.bodyBuf = append(s.bodyBuf, SnapshotBody{
		Entity: sim.HillEntity,
		Kind:   sim.KindHazard,
		Team:   h.Team,
		Radius: h.Radius,
		Health: 1,
		Pos:    h.Pos,
		Rot:    physics.Quat{W: 1},
	})
}

// appendBoltBodies puts every laser round in flight into the snapshot.
//
// Bolts are not physics bodies, so like the hill they are synthetic entries. Sending them
// as bodies rather than letting clients simulate their own means what you see is exactly
// what the server will resolve against — with a travelling, falling round, a client that
// integrated its own arc would drift and start drawing near-misses as hits.
//
// The rotation is derived from the velocity so the client can stretch a bolt along its
// own flight path without needing the vector itself.
func (s *Server) appendBoltBodies() {
	for _, b := range s.sim.Bolts() {
		// A missile rides the tier byte, the same trick props use for their structure
		// type: same kind, different thing to draw, no new field on every body record.
		tier := uint8(0)
		if b.Missile {
			tier = 1
		}
		s.bodyBuf = append(s.bodyBuf, SnapshotBody{
			Entity: physics.EntityID(boltEntityBase + b.ID),
			Kind:   sim.KindBolt,
			Tier:   sim.Tier(tier),
			Team:   b.Team,
			Radius: b.Radius(),
			Health: 1,
			Pos:    b.Pos,
			Rot:    physics.LookRotation(b.Vel),
		})
	}
}

// appendPickupBodies puts every cargo box into the snapshot.
//
// Boxes have no physics body either — flying into one collects it rather than bouncing off
// it — so their positions come from the sim's own table. The Tier byte carries which item
// is inside, which is what lets a client colour a box from a distance.
func (s *Server) appendPickupBodies() {
	for e, o := range s.sim.Objects() {
		if o.Kind != sim.KindPickup {
			continue
		}
		pos, ok := s.sim.PickupPos(e)
		if !ok {
			continue
		}
		s.bodyBuf = append(s.bodyBuf, SnapshotBody{
			Entity: e,
			Kind:   sim.KindPickup,
			Tier:   o.Tier,
			Team:   sim.NoTeam,
			Radius: o.Radius,
			Health: 1,
			Pos:    pos,
			Rot:    physics.Quat{W: 1},
		})
	}
}

// boltEntityBase keeps synthetic bolt ids clear of both real entities and the hill.
const boltEntityBase = 0xF0000000

// broadcastBinary sends data to every member of a room.
//
// This deliberately does NOT use sprocket's Room.BroadcastBinary, which does not
// deliver. That path enqueues via Session.writeSharedNoSignal (which skips the write
// pump signal by design) and then calls Engine.wakeWorkers, but wakeWorkers only pushes
// nil sentinels and writeWorker treats nil as a no-op — it has no registry of sessions
// with pending writes, and its periodic ticker branch is empty. Queued room broadcasts
// are therefore never flushed. Session.WriteBinary works because it pushes the session
// pointer itself onto the pump.
//
// Session.WriteBinary also copies the payload, whereas the shared-message path retains
// the caller's slice — so this is additionally what makes reusing snapBuf across ticks
// safe. Worth fixing upstream in sprocket; at 16 players the per-session cost is noise.
func (s *Server) broadcastBinary(room *sprocket.Room, data []byte) {
	for _, sess := range room.Members() {
		if sess.IsOpen() {
			_ = sess.WriteBinary(data)
		}
	}
}

// sendPlayerState pushes a player's private state: wallet, owned upgrades, hull and
// weapon charge. Must run on the tick goroutine — it reads the Sim.
func (s *Server) sendPlayerState(sess *sprocket.Session, pid sim.PlayerID) {
	p, ok := s.sim.Players()[pid]
	if !ok || !sess.IsOpen() {
		return
	}
	health := float32(1)
	if o, ok := s.sim.Object(p.Ship); ok && o.MaxHealth > 0 {
		health = o.Health / o.MaxHealth
	}
	_ = sess.WriteBinary(EncodePlayerState(p, health))
}

// sendShopOffers pushes this player's intermission choices. Separate from PlayerState
// because the offers change twice a match while energy and health change every tick.
func (s *Server) sendShopOffers(sess *sprocket.Session, pid sim.PlayerID) {
	if !sess.IsOpen() {
		return
	}
	_ = sess.WriteBinary(EncodeShopOffers(s.sim.Offers(pid)))
}

// eachPlayerSession runs fn for every session in the match room that has a player.
func (s *Server) eachPlayerSession(fn func(*sprocket.Session, sim.PlayerID)) {
	for _, sess := range s.engine.Room(roomMatch).Members() {
		v, ok := sess.Get(sessionKeyPlayer)
		if !ok || !sess.IsOpen() {
			continue
		}
		fn(sess, v.(sim.PlayerID))
	}
}

// extendDeadline pushes a session's read deadline out. See the keepalive note above.
func (s *Server) extendDeadline(sess *sprocket.Session, now time.Time) {
	conn := sess.RawConn()
	if conn == nil {
		return
	}
	_ = conn.SetReadDeadline(now.Add(readDeadlineGrant))
}

// runKeepalive refreshes read deadlines for sessions that are still talking, and closes
// ones that have gone quiet. Runs until ctx is cancelled.
func (s *Server) runKeepalive(ctx context.Context) {
	ticker := time.NewTicker(keepaliveInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			for _, sess := range s.engine.Sessions() {
				if !sess.IsOpen() {
					continue
				}

				v, ok := sess.Get(sessionKeyLastRecv)
				last, isTime := v.(time.Time)
				if !ok || !isTime {
					// Never sent anything; give it the grace period from connect.
					s.extendDeadline(sess, now)
					continue
				}

				if now.Sub(last) > clientIdleTimeout {
					log.Printf("session %s: idle for %s, closing", sess.ID, now.Sub(last).Round(time.Second))
					_ = sess.Close()
					continue
				}

				s.extendDeadline(sess, now)
			}
		}
	}
}

// post queues a mutation to run on the tick goroutine. Dropping on a full queue is
// deliberate: a client flooding input must not be able to stall the simulation.
func (s *Server) post(fn func()) {
	select {
	case s.cmds <- fn:
	default:
		log.Print("command queue full; dropping")
	}
}

/*============================================================================
 * Connection handling (runs on connection goroutines)
 *===========================================================================*/

func (s *Server) onConnect(sess *sprocket.Session) {
	s.sessions.Add(1)
	sess.Set(sessionKeyLastRecv, time.Now())
	s.extendDeadline(sess, time.Now())

	if sess.Request != nil && sess.Request.URL.Path == "/debug" {
		if !s.devMode {
			_ = sess.Close()
			return
		}
		sess.Set(sessionKeyDebug, true)
		s.engine.Room(roomDebug).Join(sess)
		log.Printf("dev console attached (%s)", sess.ID)
		return
	}

	// Players are admitted to the match room only once they have said Hello, so a
	// half-open connection never receives snapshots.
	s.engine.Room(roomMatch).Join(sess)
}

func (s *Server) onDisconnect(sess *sprocket.Session) {
	s.sessions.Add(-1)

	s.engine.Room(roomMatch).Leave(sess)

	if v, ok := sess.Get(sessionKeyPlayer); ok {
		pid := v.(sim.PlayerID)
		// Leave the room first, so the roster that goes out afterwards is not also sent
		// to the socket that just closed. Their seat frees up for the next joiner.
		s.post(func() {
			s.sim.RemovePlayer(pid)
			s.broadcastRoster()
		})
	}

	if s.devMode {
		s.engine.Room(roomDebug).Leave(sess)
	}
}

func (s *Server) onBinary(sess *sprocket.Session, data []byte) {
	// Any traffic proves the client is alive; the keepalive loop turns this into a
	// read-deadline extension.
	sess.Set(sessionKeyLastRecv, time.Now())

	if len(data) < 1 {
		return
	}
	tag, body := data[0], data[1:]

	switch tag {
	case MsgHello:
		s.handleHello(sess, body)

	case MsgInput:
		v, ok := sess.Get(sessionKeyPlayer)
		if !ok {
			return // input before Hello
		}
		in, err := DecodeInput(body)
		if err != nil {
			return
		}
		pid := v.(sim.PlayerID)
		s.post(func() { s.sim.SetInput(pid, in) })

	case MsgStartMatch:
		v, ok := sess.Get(sessionKeyPlayer)
		if !ok {
			return
		}
		pid := v.(sim.PlayerID)
		// The Sim decides whether this player may start and whether there is a lobby to
		// start; a non-host pressing it is a no-op rather than an error.
		s.post(func() {
			if s.sim.StartMatch(pid) {
				log.Printf("player %d started the match", pid)
				s.broadcastRoster()
			}
		})

	case MsgSetTeam:
		v, ok := sess.Get(sessionKeyPlayer)
		if !ok {
			return
		}
		team, err := DecodeSetTeam(body)
		if err != nil {
			return
		}
		pid := v.(sim.PlayerID)
		s.post(func() {
			// The old crew has to be read before the move, so the session can be taken
			// out of the right team room — that room is what scopes team chat, and a
			// player left in their previous one would keep reading their old crew's
			// messages.
			from, ok := s.sim.TeamOf(pid)
			if !ok || !s.sim.SetTeam(pid, team) {
				return
			}
			s.engine.Room(teamRoomName(from)).Leave(sess)
			s.engine.Room(teamRoomName(team)).Join(sess)
			s.broadcastRoster()
			s.sendPlayerState(sess, pid)
			log.Printf("player %d moved from team %d to %d", pid, from, team)
		})

	case MsgChat:
		v, ok := sess.Get(sessionKeyPlayer)
		if !ok {
			return
		}
		channel, text, err := DecodeChat(body)
		if err != nil || text == "" {
			return // an empty message after sanitising is not worth a broadcast
		}
		pid := v.(sim.PlayerID)
		s.post(func() { s.relayChat(pid, channel, text) })

	case MsgUseItem:
		v, ok := sess.Get(sessionKeyPlayer)
		if !ok {
			return
		}
		slot, err := DecodeUseItem(body)
		if err != nil {
			return
		}
		pid := v.(sim.PlayerID)
		// The Sim decides whether the slot holds anything and whether this player is in
		// a position to use it; pressing an empty slot is a no-op, not an error.
		s.post(func() {
			if s.sim.UseItem(pid, slot) {
				s.sendPlayerState(sess, pid)
			}
		})

	case MsgBuyUpgrade:
		v, ok := sess.Get(sessionKeyPlayer)
		if !ok {
			return
		}
		up, err := DecodeBuyUpgrade(body)
		if err != nil {
			return
		}
		pid := v.(sim.PlayerID)
		// Purchases mutate the Sim, so they go through the tick goroutine. The Sim
		// rejects anything outside the intermission.
		s.post(func() {
			if _, ok := s.sim.Buy(pid, up); ok {
				s.sendPlayerState(sess, pid)
				s.sendShopOffers(sess, pid)
			}
		})

	case MsgBuyOffer:
		v, ok := sess.Get(sessionKeyPlayer)
		if !ok {
			return
		}
		slot, err := DecodeBuyOffer(body)
		if err != nil {
			return
		}
		pid := v.(sim.PlayerID)
		s.post(func() {
			if ok, _ := s.sim.BuyOffer(pid, slot); ok {
				s.sendPlayerState(sess, pid)
				s.sendShopOffers(sess, pid)
			}
		})

	case MsgSetTeamName:
		v, ok := sess.Get(sessionKeyPlayer)
		if !ok {
			return
		}
		name, err := DecodeSetTeamName(body)
		if err != nil {
			return
		}
		pid := v.(sim.PlayerID)
		s.post(func() { s.sim.SetTeamName(pid, name) })

	case MsgSetTeamColor:
		v, ok := sess.Get(sessionKeyPlayer)
		if !ok {
			return
		}
		color, err := DecodeSetTeamColor(body)
		if err != nil {
			return
		}
		pid := v.(sim.PlayerID)
		s.post(func() { s.sim.SetTeamColor(pid, color) })

	case MsgDebugSet:
		if !s.devMode {
			return
		}
		if _, ok := sess.Get(sessionKeyDebug); !ok {
			return // only the dev console may retune the world
		}
		param, value, err := DecodeDebugSet(body)
		if err != nil {
			return
		}
		// Tuning is mutex-guarded, so this needs no trip through the tick loop.
		s.sim.Tuning.SetByID(param, value)

	default:
		// Unknown tags are ignored rather than fatal, so an older client talking to a
		// newer server degrades instead of disconnecting.
	}
}

func (s *Server) handleHello(sess *sprocket.Session, body []byte) {
	name, teamPref, err := DecodeHello(body)
	if err != nil {
		_ = sess.Close()
		return
	}
	if _, exists := sess.Get(sessionKeyPlayer); exists {
		return // Hello twice; ignore
	}

	// Spawning touches the Sim, so it happens on the tick goroutine; the Welcome is
	// sent from there once the player actually exists.
	s.post(func() {
		p := s.sim.AddPlayer(name, teamPref)
		if p == nil {
			// Every crew is full. Closing the socket is the whole rejection: a joiner
			// with no seat has nothing to draw and no way to become playable, and the
			// client already reports a connection that closes as a failed join.
			log.Printf("refused %q: every team is full", name)
			_ = sess.Close()
			return
		}
		sess.Set(sessionKeyPlayer, p.ID)

		s.engine.Room(teamRoomName(p.Team)).Join(sess)

		_ = sess.WriteBinary(EncodeWelcome(
			uint32(p.ID), p.Ship, p.Team, sim.TickHz, sim.TickHz/SnapshotEveryNTicks,
		))
		log.Printf("player %d (%s) joined team %d", p.ID, p.Name, p.Team)

		// Everyone's lobby gains a row, including the joiner's own.
		s.broadcastRoster()
	})
}

// relayChat sends a message on to whoever is entitled to read it.
//
// The sender's name and team come from the Sim rather than from the message, so a client
// cannot put words in somebody else's mouth or claim a crew it is not on. Team chat goes
// to the team room, which is the same room the server already keeps for per-crew traffic.
func (s *Server) relayChat(pid sim.PlayerID, channel uint8, text string) {
	p, ok := s.sim.Players()[pid]
	if !ok {
		return
	}

	out := EncodeChatSay(channel, p.Team, p.Name, text)
	if channel == ChatTeam {
		s.broadcastBinary(s.engine.Room(teamRoomName(p.Team)), out)
		return
	}
	s.broadcastBinary(s.engine.Room(roomMatch), out)
}

// broadcastRoster tells every client who is connected. Sent on change rather than on a
// cadence: joins and leaves are rare, and a lobby that redraws itself fifteen times a
// second for no reason is worse than one that redraws when something happens.
func (s *Server) broadcastRoster() {
	s.broadcastBinary(s.engine.Room(roomMatch), EncodeRoster(
		s.sim.HostID(), s.sim.TeamCapacity(), s.sim.Roster(),
	))
}

func teamRoomName(team uint8) string {
	return "team:" + string(rune('0'+team))
}

/*============================================================================
 * Tick loop (owns the simulation)
 *===========================================================================*/

// Run drives the simulation until ctx is cancelled.
func (s *Server) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second / sim.TickHz)
	defer ticker.Stop()

	log.Printf("simulation running at %d Hz, snapshots at %d Hz",
		sim.TickHz, sim.TickHz/SnapshotEveryNTicks)

	go s.runKeepalive(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Print("simulation stopping")
			s.sim.Close()
			_ = s.engine.Close()
			return

		case <-ticker.C:
			s.drainCommands()
			s.sim.Step()
			s.publish()
		}
	}
}

// drainCommands applies everything queued by connection goroutines. Bounded so a flood
// of joins can never starve the simulation of its tick.
func (s *Server) drainCommands() {
	const maxPerTick = 512
	for i := 0; i < maxPerTick; i++ {
		select {
		case fn := <-s.cmds:
			fn()
		default:
			return
		}
	}
}

func (s *Server) publish() {
	tick := s.sim.Tick()

	// Events are discrete and must not be batched away, so they go out every tick.
	if events := s.sim.DrainEvents(); len(events) > 0 {
		room := s.engine.Room(roomMatch)
		for _, e := range events {
			// The results table goes out just ahead of the event that sets the
			// end-of-match sequence running, so the screen that sequence leads into is
			// never waiting on a message that has not arrived. It is sent once, on the
			// transition — the match is over, and nothing in it can change after this.
			if e.Type == sim.EventMatchOver {
				s.broadcastBinary(room, EncodeMatchResults(
					s.sim.Match().Winner, s.sim.Results()))
			}
			s.broadcastBinary(room, EncodeEvent(e))
		}
	}

	// Lasers are hitscan, so a shot exists for exactly one tick. Missing the broadcast
	// means the beam is never drawn at all, hence every tick rather than the snapshot
	// cadence.
	if shots := s.sim.Shots(); len(shots) > 0 {
		s.broadcastBinary(s.engine.Room(roomMatch), EncodeShots(shots))
	}

	if tick%SnapshotEveryNTicks == 0 {
		s.broadcastSnapshot(tick)

		// Energy and hull are personal and drive live HUD bars, so they ride the
		// snapshot cadence. Broadcasting them would tell every player how much charge
		// their opponents have left, which is exactly the information a duel is about.
		s.eachPlayerSession(s.sendPlayerState)
	}

	if tick%teamStateEveryNTicks == 0 {
		room := s.engine.Room(roomMatch)
		teams := s.sim.Teams()
		s.broadcastBinary(room, EncodeTeamState(teams))
		s.broadcastBinary(room, EncodeMatchState(s.sim.Match(), s.sim.MatchConfig(), teams))

		// Offers are personal too: a player must not see what everyone else was offered.
		s.eachPlayerSession(s.sendShopOffers)
	}

	if s.devMode && tick%debugStatsEveryNTicks == 0 {
		s.broadcastBinary(s.engine.Room(roomDebug), EncodeDebugStats(
			float32(s.sim.LastStepMs()),
			uint16(s.sim.BodyCount()),
			uint16(s.sessions.Load()),
		))
	}
}

func (s *Server) broadcastSnapshot(tick uint32) {
	objects := s.sim.Objects()
	states := s.sim.States()

	s.bodyBuf = s.bodyBuf[:0]
	for e, o := range objects {
		st, ok := states[e]
		if !ok {
			continue
		}

		var flags uint8
		if o.IsHeld() {
			flags |= bodyFlagHeld
		}
		if st.Sleeping {
			flags |= bodyFlagSleeping
		}
		if o.Integrity < 1 {
			flags |= bodyFlagDamaged
		}
		// A destroyed ship's body is parked far off the field until it respawns. Flagging
		// it lets the client hide the model instead of drawing it streaking away.
		if o.Kind == sim.KindShip && o.MaxHealth > 0 && o.Health <= 0 {
			flags |= bodyFlagDead
		}
		// Engines actually running hot, which is not the same as the key being held —
		// see sim/boost.go. Every client draws every ship's plume from this.
		if o.Kind == sim.KindShip && s.sim.ShipBoosting(e) {
			flags |= bodyFlagBoosting
		}

		// Full health for anything that cannot be shot, so the client's damage tint keys
		// off a single field without special-casing motherships and rubble.
		health := float32(1)
		if o.MaxHealth > 0 {
			health = o.Health / o.MaxHealth
		}

		s.bodyBuf = append(s.bodyBuf, SnapshotBody{
			Entity: e, Kind: o.Kind, Flags: flags, Tier: o.Tier, Radius: o.Radius,
			Team: o.Team, Health: health, Pos: st.Pos, Rot: st.Rot,
		})
	}

	s.appendHillBody()
	s.appendBoltBodies()
	s.appendPickupBodies()

	elapsed := uint32(time.Since(s.started).Milliseconds())
	s.snapBuf = EncodeSnapshot(s.snapBuf, tick, elapsed, s.bodyBuf)
	s.broadcastBinary(s.engine.Room(roomMatch), s.snapBuf)
}

// SaveTuning writes the current feel values. Called on shutdown in dev mode so a good
// session is not lost.
func (s *Server) SaveTuning() error {
	if s.feelPath == "" {
		return nil
	}
	return s.sim.Tuning.Save(s.feelPath)
}
