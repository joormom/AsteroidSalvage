// Package sim holds the authoritative game rules: ship control, the grab constraint,
// salvage fragility, deposits and scoring. It owns all game meaning; package physics
// knows only masses and forces.
//
// A Sim is driven by a single goroutine at a fixed 30 Hz and is not safe for concurrent
// use. Network input arrives via SetInput, which the server marshals onto the tick
// goroutine.
package sim

import (
	"math"
	"math/rand"
	"sort"
	"strings"

	"asteroidsalvage/physics"
)

// TickHz is the fixed simulation rate. Snapshots go out at a lower rate (see net).
const TickHz = 30

// Ship mass sets the whole game's sense of scale, because hauling feel is really a
// ratio: cargo mass over ship mass. Spawned asteroids run roughly 20-150 kg (radius
// 1.5-5.5 m at density 0.9), so a 120 kg ship makes a small rock barely noticeable and
// the biggest ones genuinely hard work — without ever fully immobilising the player.
//
// An earlier 8 kg ship was lighter than nearly every rock in the belt, and grabbing one
// brought the ship to a dead stop: 184 m of travel unladen versus 1 m carrying a 600 kg
// rock. Correct physics, unplayable game.
const (
	ShipMass   = 120.0
	ShipRadius = 2.0

	// MothershipRadius is the hull as everything *sees* it: the snapshot radius the client
	// scales the model by, and what a laser is tested against. The capture volume is
	// Config.DepositRadius.
	MothershipRadius = 30.0

	// MothershipColliderRadius is the sphere the physics world actually uses, and it is
	// deliberately much smaller. **Stopgap.**
	//
	// The station model is a flattened saucer — an ellipsoid 30 m across and 12.6 m tall —
	// but ag_resolve_collisions in bridge.c only resolves sphere-sphere contacts, so a
	// collider can only ever be a sphere. At the full 30 m that left roughly 17 m of solid
	// nothing above and below the saucer: you flew over your own station, hit an invisible
	// wall, and once collisions started doing damage by speed that killed you.
	//
	// 12.6 m is the largest sphere that fits entirely inside the visible hull, so nothing
	// invisible can be hit any more. The cost is the opposite error, and a cosmetic one:
	// the outer rim of the saucer is now flown through rather than bounced off.
	//
	// The real fix is a ring station — a circle of sphere colliders matching a rebuilt
	// model, with the rim solid and the middle open — which needs the model, the colliders
	// and the parenting of shots and rams all changed together.
	MothershipColliderRadius = 12.6

	// MothershipRing is how far each team's mothership sits from the centre. Far
	// enough apart that a team's home is defensible and worth flying back to, close
	// enough that the asteroid belt between them is contested.
	MothershipRing = 520.0

	// MapRadius bounds the play volume: the belt fits inside it with room to spare, and
	// it is what a laser's range is measured against — a shot travels until it leaves
	// this sphere rather than fading out at some arbitrary distance.
	//
	// Not enforced as a wall. Nothing stops a player flying past it; it only defines
	// where the world stops being interesting, which is all the weapon needs to know.
	MapRadius = 1500.0

	// ShipRestitution is how bouncy every collidable surface is. lagrange takes the
	// minimum of the two materials in a contact, so this is applied uniformly to ships,
	// rocks and the mothership — a single dull surface would flatten every impact
	// against it. 0.8 is springy enough that a collision is unmistakable.
	ShipRestitution = 0.8
)

// TickDuration in seconds.
const TickDuration = 1.0 / float32(TickHz)

type PlayerID uint32

// Kind labels what an entity means to the game. Wire values — see shared/protocol.md.
type Kind uint8

const (
	KindShip       Kind = 0
	KindAsteroid   Kind = 1
	KindSalvage    Kind = 2
	KindMothership Kind = 3
	KindHazard     Kind = 4
	// KindBolt is a laser round in flight. Not a physics body — see projectiles.go —
	// so it reaches clients as a synthetic snapshot entry.
	KindBolt Kind = 6

	// KindProp is static map scenery: derelicts, wrecks, broken worlds. Solid and
	// indestructible; the Tier byte carries which structure to draw. See props.go.
	KindProp Kind = 5

	// KindPickup is a cargo box drifting in a bubble. Not a physics body — flying into
	// one collects it rather than bouncing off it — so like the hill and bolts it exists
	// only as a snapshot entry. The Tier byte carries which item is inside. See items.go.
	KindPickup Kind = 7
)

// EventType values are wire values — see shared/protocol.md.
type EventType uint8

const (
	EventGrabbed   EventType = 0
	EventDropped   EventType = 1
	EventDeposited EventType = 2
	EventDamaged   EventType = 3
	EventDestroyed EventType = 4

	// Match flow. For these the Player field carries a *team* id rather than a player
	// id (0xFF = nobody / draw), and Value carries the round number or winning score.
	EventRoundStart EventType = 5
	EventRoundEnd   EventType = 6
	EventMatchOver  EventType = 7

	// Combat.
	EventShipHit           EventType = 8
	EventShipDestroyed     EventType = 9  // Value carries the victim's team
	EventShipRespawn       EventType = 10 // Value carries the team
	EventAsteroidHit       EventType = 11
	EventAsteroidDestroyed EventType = 12 // Value carries the tier that broke

	// Sieging a station. Value carries the damage on a hit and the owning team on a
	// kill; Player is the shooter, so a client can tell "we are being hit" from "we are
	// hitting them" without looking anything up.
	EventMothershipHit       EventType = 13
	EventMothershipDestroyed EventType = 14 // Value carries the destroyed team
	EventShieldAbsorbed      EventType = 15 // a shielded station shrugged off a hit

	// Value carries the team id of the last crew standing.
	EventTeamEliminated EventType = 16

	// The King of the Hill control point relocated. Value carries its radius; the new
	// position rides in the snapshot as a synthetic body.
	EventHillMoved EventType = 17

	// Cargo boxes. Value carries the ItemID in both cases, so a client can play a
	// different sound for a missile than for a speed boost without tracking inventory.
	EventItemPickedUp EventType = 18
	EventItemUsed     EventType = 19
)

// NoTeam marks objects that belong to nobody, such as asteroids.
const NoTeam uint8 = 0xFF

// physicsZero is a shared zero vector for resets.
var physicsZero = physics.Vec3{}

// Event is a discrete occurrence for the client to react to (sound, HUD flash).
// Events are not interpolated and are dropped if the client misses them.
type Event struct {
	Type   EventType
	Entity physics.EntityID
	Player PlayerID
	Value  float32
}

// Object is the game's view of a physics body.
type Object struct {
	Entity physics.EntityID
	Kind   Kind
	Tier   Tier
	Radius float32

	// Team owns this object: which crew a ship belongs to, or which mothership this is.
	// 0xFF for neutral things like asteroids.
	Team uint8

	// Health is only meaningful for things that can be shot. Asteroids take laser
	// damage until they break; ships take it until they are destroyed.
	Health    float32
	MaxHealth float32

	// Value is what the object banks when deposited, before integrity is applied.
	Value float32
	// Integrity runs 1 (pristine) down to 0 (worthless). Hard impacts reduce it, which
	// is the whole risk/reward of hauling fast.
	Integrity float32

	// RequiredBeams is how much combined tractor strength it takes to actually move
	// this rock. Massive asteroids need 2, so they are a two-player job unless someone
	// has bought the tractor upgrade.
	RequiredBeams int

	// Holders is every player currently beaming this object. More than one is the
	// point: their forces sum, which is how a crew moves something one pilot cannot.
	Holders []PlayerID
}

// IsHeld reports whether anyone has a beam on this object.
func (o *Object) IsHeld() bool { return len(o.Holders) > 0 }

// HeldByPlayer reports whether a specific player is beaming this object.
func (o *Object) HeldByPlayer(id PlayerID) bool {
	for _, h := range o.Holders {
		if h == id {
			return true
		}
	}
	return false
}

func (o *Object) addHolder(id PlayerID) {
	if !o.HeldByPlayer(id) {
		o.Holders = append(o.Holders, id)
	}
}

func (o *Object) removeHolder(id PlayerID) {
	for i, h := range o.Holders {
		if h == id {
			o.Holders = append(o.Holders[:i], o.Holders[i+1:]...)
			return
		}
	}
}

// Input is the latest control state received from a client.
type Input struct {
	Seq         uint32
	ThrustFwd   float32
	ThrustRight float32
	ThrustUp    float32
	Yaw         float32
	Pitch       float32
	Roll        float32
	Grab        bool
	Boost       bool
	Brake       bool
	Fire        bool
}

// PlayerStats is a pilot's match record. Four numbers rather than a full log, because
// the only thing that reads them is the end-of-match screen and the question it answers
// is "what did each of us actually do", not "what happened when".
//
// Deliveries and kills are counted where they are already emitted as events, so a stat
// cannot disagree with what the client saw and heard.
type PlayerStats struct {
	Delivered int     // rocks banked at the station
	Banked    float32 // their total value, which is not the same ranking as the count
	Kills     int
	Deaths    int // every death, including rams and station turrets
}

// Player is a connected participant.
type Player struct {
	ID   PlayerID
	Name string
	Team uint8
	Ship physics.EntityID

	input   Input
	lastSeq uint32

	// grabCooldown blocks re-acquisition for a few ticks after cargo breaks free, so
	// overreaching still costs you something even though the grab button is held down.
	grabCooldown int

	// holdSlack is how far the cargo is currently allowed to trail behind the hold
	// point. It starts at whatever range the beam locked on at and ratchets down as the
	// object is reeled in, so a long-range grab is not instantly torn off while normal
	// break rules still apply once it is tucked in close.
	holdSlack float32

	Held physics.EntityID // 0 when empty-handed

	// BeamStrength is how many beams this player counts as. The tractor upgrade raises
	// it to 2, which is what lets a solo pilot move a massive asteroid.
	BeamStrength int

	// Credits are personal earnings, spent between rounds. Separate from team score:
	// the team wins the match, but you upgrade your own ship.
	Credits float32

	// Upgrades persist for the whole match, which is what makes an early round matter
	// even if you lose it.
	Upgrades map[UpgradeID]int

	// Offers are this intermission's randomised choices, rerolled every round.
	Offers []Offer

	// Items carried, and the effects the used ones are running. Cleared on death: an
	// item is a window, and one that survives a respawn is a permanent upgrade with
	// extra steps. See items.go.
	Items   [ItemSlots]ItemID
	Effects Effects

	// Stats are what the pilot did over the whole match, for the results screen. They
	// accumulate like Credits rather than resetting with the round: a round you lost is
	// still work you did, and the screen that shows them appears once, at the end.
	Stats PlayerStats

	// impactCooldown blocks repeat collision damage while a contact is sustained. See
	// HullImpactCooldownTicks.
	impactCooldown int

	// Combat.
	Energy    float32
	cooldown  int // ticks until this ship can fire again
	respawnIn int // ticks until a destroyed ship comes back; 0 when alive

	// grounded means the team's life pool ran dry on this player's death, so there is no
	// respawn coming — they are out until the next round. Distinct from respawnIn, which
	// is a countdown; this is an end state and must not tick down into a free respawn.
	grounded bool

	// Boost is seconds of burn left in the tank. See boost.go.
	Boost         float32
	boosting      bool
	boostLocked   bool    // ran dry; must release the key before boosting again
	boostCooldown float32 // seconds of forced stall left after running dry
}

// Dead reports whether this player is waiting to respawn.
func (p *Player) Dead() bool { return p.respawnIn > 0 || p.grounded }

// Grounded reports that this player is out for the rest of the round because their team
// spent its last life. There is no countdown to show — that is the point of it.
func (p *Player) Grounded() bool { return p.grounded }

// RespawnSeconds is what the HUD counts down.
func (p *Player) RespawnSeconds() float32 {
	if p.respawnIn <= 0 {
		return 0
	}
	return float32(p.respawnIn) / float32(TickHz)
}

// Team accumulates score. In co-op there is exactly one team containing everyone.
type Team struct {
	ID      uint8
	Name    string
	Score   float32
	Members int

	// Lives is the crew's shared pool of respawns for the current round. See lives.go.
	Lives int

	// Color is the palette entry this crew wears, 0-15. Defaults to the team id, which
	// is the fixed mapping teams had before colours became a choice.
	Color uint8

	// Station holds the upgrades bolted onto this team's mothership. They live here
	// rather than on the player who paid because the thing they modify is shared: two
	// crewmates buying a shield must not each get their own, and a shield bought in round
	// two has to still be there for the teammate who joins in round three. Anyone may buy
	// the next level with their own credits — it is the one thing in the shop you buy for
	// the team rather than for yourself.
	Station map[UpgradeID]int
}

// DefaultTeamName is what a team is called until someone renames it.
func DefaultTeamName(id uint8) string {
	return "Team " + string(rune('A'+id))
}

// SetTeamName renames a player's team. Any member may rename it — this is a game for
// friends, and arbitrating who "owns" the name is not worth the complexity.
func (s *Sim) SetTeamName(id PlayerID, name string) bool {
	p, ok := s.players[id]
	if !ok {
		return false
	}
	t, ok := s.teams[p.Team]
	if !ok {
		return false
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 20 {
		return false
	}
	t.Name = name
	return true
}

// TeamColorCount is how many palette entries a crew can choose between. Mirrored by
// TEAM_COLOR_COUNT in the Python protocol.
const TeamColorCount = 16

// SetTeamColor puts a player's crew into a palette entry.
//
// Any member may change it, for the same reason any member may rename the team: this is a
// game for friends and arbitrating ownership is not worth the complexity. Out-of-range
// values are rejected rather than wrapped, so a malformed client cannot put a team into a
// colour no other client can render.
func (s *Sim) SetTeamColor(id PlayerID, color uint8) bool {
	p, ok := s.players[id]
	if !ok {
		return false
	}
	t, ok := s.teams[p.Team]
	if !ok || int(color) >= TeamColorCount {
		return false
	}
	t.Color = color
	return true
}

// Config describes a match.
type Config struct {
	MaxEntities   int
	TeamSize      int  // players per team; 4 in the standard 16-player match
	TeamCount     int  // 4 in the standard match, 1 in co-op
	Coop          bool // single shared score pool, no competition
	Seed          int64
	DepositRadius float32 // how close to the mothership a haul must get to bank

	// AutoRestock spawns the next wave once the belt is nearly cleared. Tests turn it
	// off so they can control exactly what is in the world.
	AutoRestock bool

	// Match sizes the best-of-N tournament.
	Match MatchConfig

	// StartingCredits seeds every player's wallet. Zero for real matches; useful for
	// exercising the shop without playing a full round first.
	StartingCredits float32

	// MinTierEra floors the progress gate on asteroid tiers, so the biggest rocks can
	// be put in the belt immediately. Zero for real matches; the point of the gate is
	// that a crew has had an intermission to buy the guns for a titan, and this exists
	// only so that behaviour can be exercised without playing three rounds first.
	MinTierEra int

	// PropCount is how many pieces of map scenery to generate. Zero for a bare volume,
	// which is what the mechanics tests want — a wreck the size of a station drifting
	// into a carefully positioned test is not a useful surprise. See props.go.
	PropCount int
}

// DefaultConfig is the standard 16-player, 4-team match.
func DefaultConfig() Config {
	return Config{
		MaxEntities: 2048,
		TeamSize:    4,
		TeamCount:   4,
		Coop:        false,
		Seed:        1,
		// Generous: the mothership captures cargo rather than requiring a precise
		// approach. Its hull is 30 m, so this is a 30 m shell around it.
		DepositRadius: 60,
		AutoRestock:   true,
		PropCount:     PropCount,
		Match:         DefaultMatchConfig(),
	}
}

// Sim is the authoritative game world.
type Sim struct {
	cfg    Config
	world  *physics.World
	Tuning *Tuning

	objects map[physics.EntityID]*Object
	players map[PlayerID]*Player
	teams   map[uint8]*Team

	// One mothership per team. Deposits only count at your own, which is what makes a
	// team's corner of the map theirs.
	motherships map[uint8]physics.EntityID

	match    *MatchState
	matchCfg MatchConfig

	shots    []Shot
	events   []Event
	tick     uint32
	wave     int
	rng      *rand.Rand
	nextPID  PlayerID
	forceBuf []physics.ForceCmd

	// Cargo boxes. They have no physics body, so their positions live here rather than in
	// the state index, and pickupRespawn is a list of countdowns — one per box taken.
	pickupPos     map[physics.EntityID]physics.Vec3
	pickupRespawn []int
	nextPickupID  uint32

	// hostID is the pilot allowed to start the match from the lobby. 0 until somebody
	// connects, and it deliberately does not move if they leave: handing the button to
	// whoever happens to be next would let a joiner start a match the host was still
	// setting up.
	hostID PlayerID

	// Rebuilt each tick from the physics snapshot so rules code can look up positions
	// without paying lagrange's linear scan.
	states map[physics.EntityID]physics.BodyState

	// Laser rounds in flight, and the counter that gives each one a stable id for the
	// snapshot. See projectiles.go.
	bolts      []Bolt
	nextBoltID uint32

	// King of the Hill: where the control point is, who holds it, and how long until it
	// moves. See koth.go.
	hillPos       physics.Vec3
	hillTeam      uint8
	hillContested bool
	hillTicks     int

	// The map's single defining structure, and its geometry cached for the spawn checks
	// that run every wave. See props.go.
	centrepiece       physics.EntityID
	centrepiecePos    physics.Vec3
	centrepieceRadius float32

	// lastCollisions holds the contacts seen during the most recent Step. The sim
	// drains the physics buffer as part of damage handling, so without keeping a copy
	// there is no way to observe collisions afterwards — which made "are contacts even
	// being generated?" impossible to answer from a test.
	lastCollisions []physics.Collision
}

// LastCollisions returns the contacts observed during the most recent Step.
func (s *Sim) LastCollisions() []physics.Collision { return s.lastCollisions }

// New creates a simulation with the mothership placed and the first wave spawned.
func New(cfg Config) (*Sim, error) {
	w, err := physics.NewWorld(TickDuration, cfg.MaxEntities)
	if err != nil {
		return nil, err
	}

	s := &Sim{
		cfg:       cfg,
		matchCfg:  cfg.Match,
		world:     w,
		Tuning:    DefaultTuning(),
		objects:   make(map[physics.EntityID]*Object, cfg.MaxEntities),
		players:   make(map[PlayerID]*Player, 16),
		teams:     make(map[uint8]*Team, 4),
		rng:       rand.New(rand.NewSource(cfg.Seed)),
		nextPID:   1,
		states:    make(map[physics.EntityID]physics.BodyState, cfg.MaxEntities),
		forceBuf:  make([]physics.ForceCmd, 0, 128),
		pickupPos: make(map[physics.EntityID]physics.Vec3, PickupCount*2),
	}

	teamCount := cfg.TeamCount
	if cfg.Coop || teamCount < 1 {
		teamCount = 1
	}
	for i := 0; i < teamCount; i++ {
		s.teams[uint8(i)] = &Team{
			ID: uint8(i), Name: DefaultTeamName(uint8(i)),
			Color:   uint8(i),
			Station: map[UpgradeID]int{},
		}
	}

	// A mothership per team, ringed around the origin so every crew has its own corner
	// to haul back to and nobody shares a drop-off.
	s.motherships = make(map[uint8]physics.EntityID, teamCount)
	for i := 0; i < teamCount; i++ {
		team := uint8(i)
		pos := s.mothershipPos(team, teamCount)

		// Static: a destination, not a physics participant. The collider is smaller than
		// the hull the client draws — see MothershipColliderRadius.
		e := w.SpawnSphere(0, MothershipColliderRadius, pos)
		// Without this the mothership keeps lagrange's default 0.3 restitution, and
		// since contacts take the minimum, every ship that touched it barely bounced.
		w.SetMaterial(e, ShipRestitution, 0.25)

		s.motherships[team] = e
		s.objects[e] = &Object{
			Entity: e, Kind: KindMothership, Radius: MothershipRadius,
			Team: team, Integrity: 1,
			Health: MothershipMaxHealth, MaxHealth: MothershipMaxHealth,
		}
	}

	s.spawnProps()
	s.hillTeam = NoTeam
	if s.matchCfg.Mode == ModeKingOfTheHill {
		s.placeHill()
	}
	s.initMatch()
	s.SpawnWave()
	s.spawnPickups()
	return s, nil
}

// Close releases the physics world.
func (s *Sim) Close() { s.world.Close() }

// Tick returns the current simulation tick.
func (s *Sim) Tick() uint32 { return s.tick }

// Wave returns the current wave number (1-based after the first SpawnWave).
func (s *Sim) Wave() int { return s.wave }

// BodyCount returns the number of live physics bodies.
func (s *Sim) BodyCount() int { return s.world.Count() }

// LastStepMs reports the most recent physics step duration, for DebugStats.
func (s *Sim) LastStepMs() float64 { return s.world.LastStepMs() }

// Teams returns the current team scores.
func (s *Sim) Teams() []Team {
	out := make([]Team, 0, len(s.teams))
	for _, t := range s.teams {
		out = append(out, *t)
	}
	return out
}

// Object looks up game metadata for an entity.
func (s *Sim) Object(e physics.EntityID) (*Object, bool) {
	o, ok := s.objects[e]
	return o, ok
}

// Objects exposes all game objects, for snapshot encoding.
func (s *Sim) Objects() map[physics.EntityID]*Object { return s.objects }

// Players exposes all connected players.
func (s *Sim) Players() map[PlayerID]*Player { return s.players }

// PlayerResult is one pilot's row on the end-of-match screen: who they were, and what
// their match came to.
type PlayerResult struct {
	ID      PlayerID
	Team    uint8
	Name    string
	Stats   PlayerStats
	Credits float32
}

// Results is every pilot's match record, in the order the end-of-match screen shows them:
// by crew, and within a crew by what they banked.
//
// The order is decided here rather than on each client because Go map iteration is
// random — without this, one match would draw a differently-ordered table for every
// player watching it, and a different one again on the next tick.
func (s *Sim) Results() []PlayerResult {
	out := make([]PlayerResult, 0, len(s.players))
	for _, p := range s.players {
		out = append(out, PlayerResult{
			ID: p.ID, Team: p.Team, Name: p.Name, Stats: p.Stats, Credits: p.Credits,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Team != b.Team {
			return a.Team < b.Team
		}
		if a.Stats.Banked != b.Stats.Banked {
			return a.Stats.Banked > b.Stats.Banked
		}
		// Two pilots who banked exactly the same — usually both zero — still need a
		// stable order, or the table reshuffles between ticks for no visible reason.
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	return out
}

// RosterEntry is one seat in the lobby.
type RosterEntry struct {
	ID   PlayerID
	Team uint8
	Name string
}

// Roster is who is currently connected, ordered by crew and then by join order.
//
// Sorted for the same reason Results is: the lobby is a list everyone is looking at
// together, and Go map iteration would otherwise reshuffle it several times a second.
// Within a crew the order is by id, which is join order — so a pilot's row does not jump
// around as other people arrive.
func (s *Sim) Roster() []RosterEntry {
	out := make([]RosterEntry, 0, len(s.players))
	for _, p := range s.players {
		out = append(out, RosterEntry{ID: p.ID, Team: p.Team, Name: p.Name})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Team != out[j].Team {
			return out[i].Team < out[j].Team
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// States returns the physics state index built during the last Step.
func (s *Sim) States() map[physics.EntityID]physics.BodyState { return s.states }

// DrainEvents returns events produced since the last call.
func (s *Sim) DrainEvents() []Event {
	out := s.events
	s.events = nil
	return out
}

func (s *Sim) emit(e Event) { s.events = append(s.events, e) }

/*============================================================================
 * Players
 *===========================================================================*/

// AddPlayer spawns a ship and assigns a team. teamPref of 0xFF auto-balances onto the
// smallest team, which is what makes the match scale from 2 players to 16 without
// special cases.
// AddPlayer seats a joining pilot, or returns nil when every crew is full.
func (s *Sim) AddPlayer(name string, teamPref uint8) *Player {
	team, ok := s.pickTeam(teamPref)
	if !ok {
		return nil
	}

	pos := s.spawnPointFor(s.nextPID, team)
	ship := s.world.SpawnSphere(ShipMass, ShipRadius, pos)
	// Ships must never sleep — a sleeping ship would ignore thrust input.
	s.world.SetSleepAllowed(ship, false)
	// Damping is applied explicitly as a force each tick so the console slider is live,
	// so the body's own damping is zeroed here.
	s.world.SetDamping(ship, 0, 0)

	// Ships bounce off things rather than mushing into them.
	//
	// lagrange combines contact restitution with fminf (sim.h:574) — the *lower* of the
	// two materials wins — so every surface in the game has to be springy or the
	// bounciest one is wasted. Hence high values here, on the rocks, and on the
	// mothership.
	s.world.SetMaterial(ship, ShipRestitution, 0.25)

	p := &Player{
		ID: s.nextPID, Name: name, Team: team, Ship: ship,
		BeamStrength: 1, Upgrades: map[UpgradeID]int{},
		Credits: s.cfg.StartingCredits,
	}
	p.Energy = p.MaxEnergy()
	p.Boost = p.MaxBoost()
	s.nextPID++

	// The first pilot through the door owns the lobby. A hosting client launches the
	// server and connects to it immediately, before it has told anybody else the
	// address, so in practice this is always the person who pressed HOST — and it needs
	// no shared secret between a process and the client that spawned it.
	if s.hostID == 0 {
		s.hostID = p.ID
	}

	s.players[p.ID] = p
	s.objects[ship] = &Object{
		Entity: ship, Kind: KindShip, Radius: ShipRadius, Integrity: 1,
		Team: team, Health: ShipMaxHealth, MaxHealth: ShipMaxHealth,
	}
	s.teams[team].Members++

	return p
}

// mothershipPos places a team's home evenly around a ring. One team sits at the centre,
// since with nobody to contest there is no reason to be off to one side.
func (s *Sim) mothershipPos(team uint8, teamCount int) physics.Vec3 {
	if teamCount <= 1 {
		return physics.Vec3{}
	}
	angle := 2 * math.Pi * float64(team) / float64(teamCount)
	return physics.Vec3{
		X: float32(math.Cos(angle) * MothershipRing),
		Y: float32(math.Sin(angle) * MothershipRing),
		Z: 0,
	}
}

// MothershipFor returns a team's mothership entity.
func (s *Sim) MothershipFor(team uint8) (physics.EntityID, bool) {
	e, ok := s.motherships[team]
	return e, ok
}

// Motherships exposes every team's home, for snapshot encoding and the client's arrow.
func (s *Sim) Motherships() map[uint8]physics.EntityID { return s.motherships }

// spawnPointFor rings players around their OWN mothership, outside its capture radius.
// Spawning inside it meant any rock grabbed near home banked the instant the beam
// touched it.
func (s *Sim) spawnPointFor(id PlayerID, team uint8) physics.Vec3 {
	home := s.mothershipPos(team, len(s.teams))
	angle := float64(id) * 1.4
	r := float64(s.cfg.DepositRadius) + 45.0
	return physics.Vec3{
		X: home.X + float32(math.Cos(angle)*r),
		Y: home.Y + float32(math.Sin(angle)*r),
		Z: home.Z,
	}
}

// pickTeam chooses a crew for a joining player, reporting false when every crew is
// already at TeamSize.
//
// The fallback used to take the smallest team unconditionally, which meant team size was
// only ever a preference: a seventeenth player in a 4x4 match silently made one crew five
// strong. The lobby shows slots per team, and a slot count that a join can overrun is
// worse than no slot count at all.
func (s *Sim) pickTeam(pref uint8) (uint8, bool) {
	if s.cfg.Coop {
		// Co-op is one team containing everyone, so its cap is the whole match — sized
		// from the configured team count, not from len(s.teams), which is 1 here by
		// definition and would cap a sixteen-player co-op game at four.
		t := s.teams[0]
		if t != nil && t.Members >= s.coopCapacity() {
			return 0, false
		}
		return 0, true
	}
	if t, ok := s.teams[pref]; ok && t.Members < s.cfg.TeamSize {
		return pref, true
	}
	// Smallest team with room wins; ties break toward the lowest id for determinism.
	best := uint8(0)
	bestN := math.MaxInt
	found := false
	for id := 0; id < len(s.teams); id++ {
		t := s.teams[uint8(id)]
		if t != nil && t.Members < bestN && t.Members < s.cfg.TeamSize {
			best, bestN, found = t.ID, t.Members, true
		}
	}
	return best, found
}

// Full reports whether every crew is at capacity, so the match cannot take anyone else.
func (s *Sim) Full() bool {
	_, ok := s.pickTeam(NoTeam)
	return !ok
}

// coopCapacity is how many pilots the single co-op crew holds: the whole match.
func (s *Sim) coopCapacity() int {
	teams := s.cfg.TeamCount
	if teams < 1 {
		teams = 1
	}
	return s.cfg.TeamSize * teams
}

// TeamCapacity is how many pilots one crew holds. The lobby draws this many slots.
func (s *Sim) TeamCapacity() int {
	if s.cfg.Coop {
		return s.coopCapacity()
	}
	return s.cfg.TeamSize
}

// RemovePlayer drops a player's ship and releases anything they were holding.
func (s *Sim) RemovePlayer(id PlayerID) {
	p, ok := s.players[id]
	if !ok {
		return
	}
	if p.Held != 0 {
		if o, ok := s.objects[p.Held]; ok {
			o.removeHolder(id)
		}
	}
	if t, ok := s.teams[p.Team]; ok {
		t.Members--
	}
	s.world.Despawn(p.Ship)
	delete(s.objects, p.Ship)
	delete(s.players, id)
}

// SetInput records the latest input for a player. Out-of-order packets are dropped.
func (s *Sim) SetInput(id PlayerID, in Input) {
	p, ok := s.players[id]
	if !ok {
		return
	}
	if in.Seq != 0 && in.Seq <= p.lastSeq {
		return
	}
	p.lastSeq = in.Seq
	p.input = in
}

/*============================================================================
 * Wave spawning
 *===========================================================================*/

// SpawnWave scatters a shell of asteroids around the mothership. Each wave is larger
// and reaches further out, so the haul gets longer as the match goes on.
func (s *Sim) SpawnWave() {
	s.wave++
	// Twice the volume wants more to find in it, or the belt reads as empty.
	count := 40 + s.wave*12

	innerR := 240.0 + float64(s.wave)*40.0
	outerR := innerR + 440.0

	for i := 0; i < count; i++ {
		// Tier decides colour, size band, density and payout — see tiers.go. The era
		// gates the biggest rocks behind match progress.
		tier := pickTier(float32(s.rng.Float64()), s.tierEra())
		spec := tier.Spec()

		radius := spec.MinRadius + float32(s.rng.Float64())*(spec.MaxRadius-spec.MinRadius)
		mass := MassFor(tier, radius)
		value := ValueFor(tier, radius)

		pos, ok := s.findSpawnPoint(innerR, outerR, radius)
		if !ok {
			continue // nowhere clear for something this big; try the next rock
		}

		e := s.world.SpawnSphere(mass, radius, pos)
		if e == 0 {
			break // storage full
		}

		// Rocks bounce off each other and off ships rather than mushing together.
		s.world.SetMaterial(e, ShipRestitution, 0.25)

		// A slow tumble so the field looks alive.
		s.world.SetVelocity(e, physics.Vec3{
			X: float32(s.rng.NormFloat64() * 0.6),
			Y: float32(s.rng.NormFloat64() * 0.6),
			Z: float32(s.rng.NormFloat64() * 0.2),
		})

		required := requiredBeamsFor(tier)

		s.objects[e] = &Object{
			Entity: e, Kind: KindAsteroid, Tier: tier, Radius: radius,
			Team: NoTeam, Value: value, Integrity: 1, RequiredBeams: required,
			Health: HealthFor(tier, radius), MaxHealth: HealthFor(tier, radius),
		}
	}
}

// mothershipKeepOut is the clear space demanded around every station, on top of the two
// radii involved. Enough that the hangar approach and the spawn ring stay unobstructed.
const mothershipKeepOut = 50.0

// findSpawnPoint picks a spot in the belt shell that is not on top of a mothership.
//
// This did not matter when the biggest rock was 8 m across: an overlap resolved itself
// in a tick or two and nobody noticed. A 60 m titan spawning inside a station is a very
// different event, and the belt shell (240-880 m) straddles the 520 m mothership ring,
// so it would happen regularly rather than never.
//
// Rejection sampling with a bounded number of tries: the keep-out volumes are a small
// fraction of the shell, so this almost always succeeds first time, and giving up after
// a few attempts is better than looping when a huge rock genuinely has nowhere to go.
func (s *Sim) findSpawnPoint(innerR, outerR float64, radius float32) (physics.Vec3, bool) {
	const tries = 12
	clearance := float64(MothershipRadius + radius + mothershipKeepOut)

	for i := 0; i < tries; i++ {
		// Uniform-ish direction on a sphere.
		theta := s.rng.Float64() * 2 * math.Pi
		phi := math.Acos(2*s.rng.Float64() - 1)
		r := innerR + s.rng.Float64()*(outerR-innerR)

		pos := physics.Vec3{
			X: float32(r * math.Sin(phi) * math.Cos(theta)),
			Y: float32(r * math.Sin(phi) * math.Sin(theta)),
			Z: float32(r * math.Cos(phi) * 0.35), // flatten into a rough belt
		}

		// The landmark in the middle of the map is the largest body in the world, and
		// the belt shell runs straight through it. A rock generated inside it is simply
		// gone — unreachable and unshootable — so this is not cosmetic.
		if !s.clearOfCentrepiece(pos, radius) {
			continue
		}

		clear := true
		for team := range s.motherships {
			home := s.mothershipPos(team, len(s.teams))
			if float64(pos.DistTo(home)) < clearance {
				clear = false
				break
			}
		}
		if clear {
			return pos, true
		}
	}
	return physics.Vec3{}, false
}

// tierEra is how far into the match we are, for gating which asteroid tiers appear.
//
// Rounds rather than waves, because the point of gating is that a crew has had an
// intermission to buy the guns a titan needs. Sandbox has no rounds, so it falls back to
// the wave counter and the big rocks turn up as the belt is worked through.
func (s *Sim) tierEra() int {
	era := s.wave
	if !s.matchCfg.DisableMatchFlow && s.match != nil {
		era = s.match.Round
	}
	if era < s.cfg.MinTierEra {
		return s.cfg.MinTierEra
	}
	return era
}

// requiredBeamsFor is how much combined tractor strength a tier needs to be moved.
func requiredBeamsFor(t Tier) int {
	spec := t.Spec()
	if !spec.Towable {
		// Nothing can move it; the number only matters for the HUD's "needs a crew"
		// hint, and the beam refuses to lock on at all.
		return 99
	}
	if t == TierMassive {
		return 2
	}
	return 1
}

// remainingSalvage counts asteroids still out there.
func (s *Sim) remainingSalvage() int {
	n := 0
	for _, o := range s.objects {
		if o.Kind == KindAsteroid || o.Kind == KindSalvage {
			n++
		}
	}
	return n
}

/*============================================================================
 * Tick
 *===========================================================================*/

// Step advances the simulation one fixed tick.
func (s *Sim) Step() {
	tune := s.Tuning.Snapshot()

	// 0. Tournament clock: may start a round, end one, or open the shop.
	s.advanceMatch()

	// 1. Read the world as of the end of the previous step.
	s.refreshStates()

	// 2. Turn player intent into forces.
	s.forceBuf = s.forceBuf[:0]
	for _, p := range s.players {
		// The tank fills for everyone, including the dead — a respawn should not also
		// be a wait for boost.
		s.updateBoost(p)

		// A wreck does not steer or grab. Its body is parked far below the field until
		// the respawn timer runs out.
		if p.Dead() {
			continue
		}
		s.controlShip(p, &tune)
		s.updateGrab(p, &tune)
	}

	// 3. Advance physics.
	s.world.ApplyForces(s.forceBuf)
	s.world.Step()

	// 4. React to what happened.
	s.applyImpactDamage(&tune)
	// Straight after the impact pass, which is what refreshes lastCollisions.
	s.applyRamming()
	// And then what the ram rule does not cover: flying into rocks and scenery, which
	// scales with speed rather than being all-or-nothing.
	s.applyHullImpacts()
	s.updateWeapons()
	// After the players, so a station's guns and a pilot's land in the same shot list and
	// go out in the same broadcast.
	s.updateBolts()
	s.updateTurrets()
	s.checkDeposits()
	// Hoard collects on contact instead of at a station; a no-op in every other mode.
	s.updateHoard()
	// King of the Hill scores by presence; a no-op in every other mode.
	s.updateKoth()

	// Cargo boxes: collect anything flown through, run the item timers down.
	s.updatePickups()
	s.updateEffects()

	// Keep the field stocked: once the belt is nearly cleared, escalate. Not during
	// intermissions or after the match — the field is deliberately empty then.
	if s.cfg.AutoRestock && s.scoringActive() && s.remainingSalvage() < 8 {
		s.SpawnWave()
	}

	s.tick++
}

func (s *Sim) refreshStates() {
	clear(s.states)
	for _, b := range s.world.Snapshot() {
		s.states[b.Entity] = b
	}
}

func (s *Sim) addForce(e physics.EntityID, force, torque physics.Vec3) {
	s.forceBuf = append(s.forceBuf, physics.ForceCmd{Entity: e, Force: force, Torque: torque})
}

// controlShip converts 6DOF input into world-space force and torque.
func (s *Sim) controlShip(p *Player, tune *Tuning) {
	st, ok := s.states[p.Ship]
	if !ok {
		return
	}

	rot := st.Rot
	fwd, right, up := rot.Forward(), rot.Right(), rot.Up()

	in := p.input
	// Engine upgrades scale this player's thrust only — Tuning is shared by everyone,
	// so an upgrade must never be applied by mutating it.
	thrust := tune.ShipThrust * p.ThrustMultiplier()
	// Whether the engines are actually running hot, not whether the key is down — the
	// tank can be empty (see boost.go).
	if p.boosting {
		thrust *= BoostThrustScale
	}

	force := fwd.Scale(in.ThrustFwd).
		Add(right.Scale(in.ThrustRight)).
		Add(up.Scale(in.ThrustUp)).
		Scale(thrust)

	// Explicit damping rather than lagrange's per-body damping, so the console slider
	// takes effect on the very next tick.
	damp := tune.ShipLinearDamping
	if in.Brake {
		damp *= 6.0
	}
	force = force.Add(st.Vel.Scale(-damp * st.Mass))

	torque := up.Scale(in.Yaw).
		Add(right.Scale(in.Pitch)).
		Add(fwd.Scale(in.Roll)).
		Scale(tune.ShipTorque)

	// Angular damping keeps the ship from spinning forever after a nudge.
	torque = torque.Add(st.AngVel.Scale(-tune.ShipAngularDamping))

	s.addForce(p.Ship, force, torque)
}

/*============================================================================
 * Impacts and deposits
 *===========================================================================*/

// applyImpactDamage turns collisions into lost value. lagrange reports contacts before
// resolution, so RelSpeed is the true impact speed.
func (s *Sim) applyImpactDamage(tune *Tuning) {
	s.lastCollisions = append(s.lastCollisions[:0], s.world.DrainCollisions()...)

	for _, c := range s.lastCollisions {
		if c.RelSpeed <= tune.SalvageDamageThreshold {
			continue
		}
		excess := c.RelSpeed - tune.SalvageDamageThreshold
		loss := excess * tune.SalvageDamageScale

		for _, e := range [2]physics.EntityID{c.A, c.B} {
			o, ok := s.objects[e]
			if !ok || (o.Kind != KindAsteroid && o.Kind != KindSalvage) {
				continue
			}
			if o.Integrity <= 0 {
				continue
			}

			// Cargo dampeners protect what you are actually carrying. Use the best
			// dampener among the holders, so bringing a well-equipped teammate onto a
			// massive rock protects it too.
			applied := loss
			if o.IsHeld() {
				best := float32(1)
				for _, hid := range o.Holders {
					if hp, ok := s.players[hid]; ok {
						if sc := hp.CargoDamageScale(); sc < best {
							best = sc
						}
					}
				}
				applied *= best
			}

			o.Integrity -= applied
			if o.Integrity <= 0 {
				o.Integrity = 0
				// The rock survives as a physics body but is worth nothing. Splitting
				// it into fragments would be more dramatic; that is future work.
				s.emit(Event{Type: EventDestroyed, Entity: e, Value: 0})
			} else {
				s.emit(Event{Type: EventDamaged, Entity: e, Value: applied})
			}
		}
	}
}

// checkDeposits banks cargo brought near the mothership.
//
// The mothership reaches out and takes it: get inside the capture radius and it is
// pulled in and banked automatically, no button. The old behaviour required the *cargo*
// to cross a tight ring, which meant lining up precisely while the rock trailed behind
// you — bounced approaches would sit a couple of metres outside and nothing happened.
func (s *Sim) checkDeposits() {
	// Deliveries only count while a round is live; hauling during an intermission
	// would let a player bank a head start while everyone else is shopping.
	if !s.scoringActive() {
		return
	}

	for _, p := range s.players {
		if p.Held == 0 {
			continue
		}

		// Your own team's mothership, and only that one. Hauling a rock into a rival's
		// hangar should not pay you, and it is what makes a team's corner of the map
		// somewhere they defend.
		home, ok := s.motherships[p.Team]
		if !ok {
			continue
		}
		mship, ok := s.states[home]
		if !ok {
			continue
		}

		o, ok := s.objects[p.Held]
		if !ok {
			p.Held = 0
			continue
		}

		cargo, ok := s.states[p.Held]
		if !ok {
			continue
		}
		ship, shipOK := s.states[p.Ship]

		// Either the cargo or the ship being inside the capture radius is enough. The
		// cargo can trail well behind the ship, and requiring the trailing end to make
		// it was the difference between a delivery landing and quietly not.
		inRange := cargo.Pos.DistTo(mship.Pos) <= s.cfg.DepositRadius
		if !inRange && shipOK {
			inRange = ship.Pos.DistTo(mship.Pos) <= s.cfg.DepositRadius
		}
		if !inRange {
			continue
		}

		// Careful hauling pays: a battered rock banks a fraction of its value.
		banked := o.Value * o.Integrity
		if t, ok := s.teams[p.Team]; ok {
			t.Score += banked
		}
		// Everyone with a beam on it shares the credit — a two-player haul should pay
		// both crews, or nobody would ever help with a massive.
		holders := o.Holders
		if len(holders) == 0 {
			holders = []PlayerID{p.ID}
		}
		share := banked / float32(len(holders))
		for _, hid := range holders {
			if hp, ok := s.players[hid]; ok {
				hp.Credits += share
				// Stats follow the payout: a rock two crews dragged home is a delivery
				// for both of them, worth what each was actually paid. Crediting only
				// the one who tripped the radius would make the second beam on a massive
				// look like it did nothing.
				hp.Stats.Delivered++
				hp.Stats.Banked += share
				hp.Held = 0
			}
		}

		s.emit(Event{Type: EventDeposited, Entity: o.Entity, Player: p.ID, Value: banked})

		s.world.Despawn(o.Entity)
		delete(s.objects, o.Entity)
	}
}
