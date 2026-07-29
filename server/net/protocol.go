// Package net implements the wire protocol and the WebSocket server.
//
// shared/protocol.md is the authoritative specification. Go and Python implement it
// independently, so any change belongs in that document first.
package net

import (
	"encoding/binary"
	"errors"
	"math"

	"asteroidsalvage/physics"
	"asteroidsalvage/sim"
)

// Message type tags. 0x01-0x7F are client->server, 0x80-0xFF server->client.
const (
	MsgInput        byte = 0x01
	MsgHello        byte = 0x02
	MsgBuyUpgrade   byte = 0x03
	MsgBuyOffer     byte = 0x04
	MsgSetTeamName  byte = 0x05
	MsgSetTeamColor byte = 0x06
	MsgDebugSet     byte = 0x10

	MsgWelcome     byte = 0x80
	MsgSnapshot    byte = 0x81
	MsgEvent       byte = 0x82
	MsgTeamState   byte = 0x83
	MsgMatchState  byte = 0x84
	MsgPlayerState byte = 0x85
	MsgShopOffers  byte = 0x86
	MsgShots       byte = 0x87
	MsgDebugStats  byte = 0x90
)

// Input flag bits.
const (
	inputFlagGrab  = 1 << 0
	inputFlagBoost = 1 << 1
	inputFlagBrake = 1 << 2
	inputFlagFire  = 1 << 3
)

// Body record flag bits.
const (
	bodyFlagHeld     = 1 << 0
	bodyFlagSleeping = 1 << 1
	bodyFlagDamaged  = 1 << 2
	bodyFlagDead     = 1 << 3 // a destroyed ship awaiting respawn: do not draw it

	// bodyFlagBoosting marks a ship whose engines are actually running hot. It rides on
	// the body rather than in the owner's PlayerState because every client draws every
	// ship's plume, and only the ship's own session receives its PlayerState.
	bodyFlagBoosting = 1 << 4
)

// bodyRecordSize is the fixed per-body cost in a snapshot: entity(4) + kind(1) +
// flags(1) + tier(1) + team(1) + health(1) + radius(2) + pos(12) + packed rotation(4).
//
// Team travels with every body rather than in a side table because the client colours
// ships and motherships by team, and a body can appear in a snapshot before any other
// message has told the client who owns it.
const bodyRecordSize = 27

// radiusQuantum is the resolution of the packed radius field, in metres.
//
// This used to be one byte at 0.25 m, covering 0-63.75 m — fine when the largest thing
// in the world was a 30 m mothership, and a hard ceiling the moment asteroids grew past
// it. Two bytes at 0.05 m covers 0-3276 m with resolution finer than anything visible,
// and costs one byte per body: about 3 KB/s at full load, which is nothing.
const radiusQuantum = 0.05

func packRadius(r float32) uint16 {
	q := int(r/radiusQuantum + 0.5)
	if q < 0 {
		q = 0
	}
	if q > 0xFFFF {
		q = 0xFFFF
	}
	return uint16(q)
}

var errShort = errors.New("net: message too short")

/*============================================================================
 * Quaternion packing
 *
 * Smallest-three: the largest-magnitude component is dropped and recovered as
 * sqrt(1 - a^2 - b^2 - c^2); its index goes in the top 2 bits and the remaining three
 * components take 10 bits each over +/-1/sqrt(2). 4 bytes instead of 16, for about
 * 0.1 degrees of error — well below anything visible.
 *===========================================================================*/

const invSqrt2 = 0.7071067811865476

func packQuat(q physics.Quat) uint32 {
	// q and -q are the same rotation, so the dropped component can always be made
	// positive; that is what lets the decoder recover it with a bare sqrt.
	c := [4]float64{float64(q.X), float64(q.Y), float64(q.Z), float64(q.W)}

	largest := 0
	for i := 1; i < 4; i++ {
		if math.Abs(c[i]) > math.Abs(c[largest]) {
			largest = i
		}
	}
	if c[largest] < 0 {
		for i := range c {
			c[i] = -c[i]
		}
	}

	out := uint32(largest) << 30
	shift := 20
	for i := 0; i < 4; i++ {
		if i == largest {
			continue
		}
		// Map [-1/sqrt2, +1/sqrt2] onto [0, 1023].
		v := c[i] / invSqrt2
		if v < -1 {
			v = -1
		}
		if v > 1 {
			v = 1
		}
		q10 := uint32(math.Round((v + 1) * 0.5 * 1023))
		out |= (q10 & 0x3FF) << uint(shift)
		shift -= 10
	}
	return out
}

// unpackQuat is the inverse of packQuat. The server does not need it, but it keeps the
// packing honest under test and documents the decode the Python client must mirror.
func unpackQuat(v uint32) physics.Quat {
	largest := int(v >> 30)

	var c [4]float64
	sumSq := 0.0
	shift := 20
	for i := 0; i < 4; i++ {
		if i == largest {
			continue
		}
		q10 := (v >> uint(shift)) & 0x3FF
		c[i] = ((float64(q10)/1023)*2 - 1) * invSqrt2
		sumSq += c[i] * c[i]
		shift -= 10
	}

	rem := 1 - sumSq
	if rem < 0 {
		rem = 0
	}
	c[largest] = math.Sqrt(rem)

	return physics.Quat{
		X: float32(c[0]), Y: float32(c[1]), Z: float32(c[2]), W: float32(c[3]),
	}
}

/*============================================================================
 * Little-endian writers
 *===========================================================================*/

func putU16(b []byte, v uint16) []byte { return binary.LittleEndian.AppendUint16(b, v) }
func putU32(b []byte, v uint32) []byte { return binary.LittleEndian.AppendUint32(b, v) }

func putF32(b []byte, v float32) []byte {
	return binary.LittleEndian.AppendUint32(b, math.Float32bits(v))
}

func getU16(b []byte) uint16 { return binary.LittleEndian.Uint16(b) }
func getU32(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }

func getF32(b []byte) float32 {
	return math.Float32frombits(binary.LittleEndian.Uint32(b))
}

/*============================================================================
 * Client -> server decoding
 *
 * Every decoder here is fed straight from the network and must not trust its input.
 *===========================================================================*/

// DecodeInput parses a 0x01 Input message body (tag already stripped).
func DecodeInput(b []byte) (sim.Input, error) {
	// seq(4) + 6 floats(24) + flags(1)
	if len(b) < 29 {
		return sim.Input{}, errShort
	}

	flags := b[28]
	in := sim.Input{
		Seq:         getU32(b[0:]),
		ThrustFwd:   clamp1(getF32(b[4:])),
		ThrustRight: clamp1(getF32(b[8:])),
		ThrustUp:    clamp1(getF32(b[12:])),
		Yaw:         clamp1(getF32(b[16:])),
		Pitch:       clamp1(getF32(b[20:])),
		Roll:        clamp1(getF32(b[24:])),
		Grab:        flags&inputFlagGrab != 0,
		Boost:       flags&inputFlagBoost != 0,
		Brake:       flags&inputFlagBrake != 0,
		Fire:        flags&inputFlagFire != 0,
	}
	return in, nil
}

// clamp1 bounds an axis to [-1, 1] and scrubs NaN. A client sending 1e9 for thrust
// would otherwise fling its ship across the map.
func clamp1(v float32) float32 {
	if math.IsNaN(float64(v)) {
		return 0
	}
	if v < -1 {
		return -1
	}
	if v > 1 {
		return 1
	}
	return v
}

// DecodeHello parses a 0x02 Hello body, returning the requested name and team.
func DecodeHello(b []byte) (name string, teamPref uint8, err error) {
	if len(b) < 1 {
		return "", 0, errShort
	}
	n := int(b[0])
	if n > 32 || len(b) < 1+n+1 {
		return "", 0, errShort
	}
	return sanitizeName(string(b[1 : 1+n])), b[1+n], nil
}

// sanitizeName strips control characters so a client cannot inject escape sequences
// into server logs or other players' HUDs.
func sanitizeName(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r >= 0x20 && r != 0x7F {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return "pilot"
	}
	return string(out)
}

// DecodeDebugSet parses a 0x10 DebugSet body.
func DecodeDebugSet(b []byte) (param uint16, value float32, err error) {
	if len(b) < 6 {
		return 0, 0, errShort
	}
	v := getF32(b[2:])
	if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
		return 0, 0, errors.New("net: non-finite debug value")
	}
	return getU16(b), v, nil
}

/*============================================================================
 * Server -> client encoding
 *===========================================================================*/

// EncodeWelcome builds a 0x80 Welcome.
func EncodeWelcome(playerID uint32, ship physics.EntityID, team uint8, tickRate, snapRate uint8) []byte {
	b := make([]byte, 0, 16)
	b = append(b, MsgWelcome)
	b = putU32(b, playerID)
	b = putU32(b, uint32(ship))
	b = append(b, team, tickRate, snapRate)
	return b
}

// SnapshotBody is one record in a snapshot.
type SnapshotBody struct {
	Entity physics.EntityID
	Kind   sim.Kind
	Flags  uint8
	Tier   sim.Tier
	Radius float32

	// Team is the owning crew, or 0xFF for neutral bodies like asteroids.
	Team uint8
	// Health runs 0-1 as a fraction of the body's maximum. Sent as a byte because it
	// only ever drives a bar and a damage tint.
	Health float32

	Pos physics.Vec3
	Rot physics.Quat
}

func packUnit(v float32) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 255
	}
	return uint8(v*255 + 0.5)
}

// EncodeSnapshot builds a 0x81 Snapshot. buf is reused across ticks to keep the
// broadcast path allocation-free.
func EncodeSnapshot(buf []byte, tick, serverMS uint32, bodies []SnapshotBody) []byte {
	need := 1 + 4 + 4 + 2 + len(bodies)*bodyRecordSize
	if cap(buf) < need {
		buf = make([]byte, 0, need*2)
	}
	b := buf[:0]

	b = append(b, MsgSnapshot)
	b = putU32(b, tick)
	b = putU32(b, serverMS)
	b = putU16(b, uint16(len(bodies)))

	for i := range bodies {
		s := &bodies[i]
		b = putU32(b, uint32(s.Entity))
		b = append(b, byte(s.Kind), s.Flags, byte(s.Tier), s.Team, packUnit(s.Health))
		b = putU16(b, packRadius(s.Radius))
		b = putF32(b, s.Pos.X)
		b = putF32(b, s.Pos.Y)
		b = putF32(b, s.Pos.Z)
		b = putU32(b, packQuat(s.Rot))
	}
	return b
}

// EncodeEvent builds a 0x82 Event.
func EncodeEvent(e sim.Event) []byte {
	b := make([]byte, 0, 14)
	b = append(b, MsgEvent, byte(e.Type))
	b = putU32(b, uint32(e.Entity))
	b = putU32(b, uint32(e.Player))
	b = putF32(b, e.Value)
	return b
}

// EncodeTeamState builds a 0x83 TeamState. Team names are length-prefixed and variable,
// so this message is no longer fixed-width.
func EncodeTeamState(teams []sim.Team) []byte {
	b := make([]byte, 0, 2+len(teams)*24)
	b = append(b, MsgTeamState, byte(len(teams)))
	for _, t := range teams {
		b = append(b, t.ID)
		b = putF32(b, t.Score)
		b = append(b, byte(t.Members))

		name := []byte(t.Name)
		if len(name) > 20 {
			name = name[:20]
		}
		b = append(b, byte(len(name)))
		b = append(b, name...)

		// Station upgrade levels ride along with the team rather than in the personal
		// PlayerState, because they are the one thing in the shop that is not personal:
		// every client needs a rival's shield level to draw their bubble, and its own
		// team's to show what the crew has bought.
		b = append(b, byte(t.Station[sim.UpgradeShields]), byte(t.Station[sim.UpgradeTurrets]))

		// Remaining respawns this round. Everyone sees everyone's, deliberately: knowing
		// a rival crew is down to its last two ships is what makes pressing an attack a
		// decision rather than a guess.
		lives := t.Lives
		if lives < 0 {
			lives = 0
		}
		if lives > 255 {
			lives = 255
		}
		b = append(b, byte(lives), t.Color)
	}
	return b
}

// EncodeShopOffers builds a 0x86 ShopOffers — this intermission's randomised choices.
func EncodeShopOffers(offers []sim.Offer) []byte {
	b := make([]byte, 0, 2+len(offers)*6)
	b = append(b, MsgShopOffers, byte(len(offers)))
	for _, o := range offers {
		b = append(b, byte(o.Upgrade), byte(o.Level))
		b = putF32(b, o.Cost)
	}
	return b
}

/*============================================================================
 * Shots
 *
 * Lasers are hitscan, so there is nothing to simulate on the client — only something to
 * draw for a few frames. Shots are broadcast as a per-snapshot list rather than as
 * events, because a beam that misses is still worth drawing and events are allowed to
 * be dropped.
 *===========================================================================*/

// shotRecordSize: shooter(4) + team(1).
//
// A shot is only the muzzle now. Where the round goes is carried by the bolt bodies in the
// snapshot, so length, a hit flag and a target no longer have anything to say at the
// moment the trigger is pulled.
const shotRecordSize = 5

// EncodeShots builds a 0x87 Shots.
func EncodeShots(shots []sim.Shot) []byte {
	b := make([]byte, 0, 2+len(shots)*shotRecordSize)
	b = append(b, MsgShots, byte(len(shots)))
	for _, s := range shots {
		b = putU32(b, uint32(s.Shooter))
		b = append(b, s.Team)
	}
	return b
}

// DecodeShots parses a 0x87 body.
func DecodeShots(b []byte) ([]sim.Shot, error) {
	if len(b) < 1 {
		return nil, errShort
	}
	n := int(b[0])
	if len(b) < 1+n*shotRecordSize {
		return nil, errShort
	}

	out := make([]sim.Shot, 0, n)
	off := 1
	for i := 0; i < n; i++ {
		r := b[off : off+shotRecordSize]
		out = append(out, sim.Shot{
			Shooter: physics.EntityID(getU32(r)),
			Team:    r[4],
		})
		off += shotRecordSize
	}
	return out, nil
}

// DecodeBuyOffer parses a 0x04 body: the offer slot the player picked.
func DecodeBuyOffer(b []byte) (int, error) {
	if len(b) < 1 {
		return 0, errShort
	}
	return int(b[0]), nil
}

// EncodeBuyOffer builds a 0x04 BuyOffer.
func EncodeBuyOffer(slot int) []byte { return []byte{MsgBuyOffer, byte(slot)} }

// DecodeSetTeamName parses a 0x05 body.
func DecodeSetTeamName(b []byte) (string, error) {
	if len(b) < 1 {
		return "", errShort
	}
	n := int(b[0])
	if n > 20 || len(b) < 1+n {
		return "", errShort
	}
	return sanitizeName(string(b[1 : 1+n])), nil
}

// DecodeSetTeamColor parses a 0x06 body: the palette entry the crew picked.
func DecodeSetTeamColor(b []byte) (uint8, error) {
	if len(b) < 1 {
		return 0, errShort
	}
	return b[0], nil
}

// EncodeSetTeamColor builds a 0x06 SetTeamColor.
func EncodeSetTeamColor(color uint8) []byte {
	return []byte{MsgSetTeamColor, color}
}

// EncodeSetTeamName builds a 0x05 SetTeamName.
func EncodeSetTeamName(name string) []byte {
	raw := []byte(name)
	if len(raw) > 20 {
		raw = raw[:20]
	}
	return append([]byte{MsgSetTeamName, byte(len(raw))}, raw...)
}

// EncodeDebugStats builds a 0x90 DebugStats.
func EncodeDebugStats(tickMS float32, bodies, sessions uint16) []byte {
	b := make([]byte, 0, 9)
	b = append(b, MsgDebugStats)
	b = putF32(b, tickMS)
	b = putU16(b, bodies)
	b = putU16(b, sessions)
	return b
}

// EncodeMatchState builds a 0x84 MatchState.
func EncodeMatchState(m sim.MatchState, cfg sim.MatchConfig, teams []sim.Team) []byte {
	// Sandbox mode runs an endless round whose remaining-time counter is effectively
	// infinite. Sent raw it wraps the u16 seconds field and the HUD showed a nonsense
	// clock like "145:39", so it is advertised as best-of-0 with no time, which the
	// client renders as free play.
	bestOf := cfg.BestOf
	secs := m.TimeLeftSeconds()
	if cfg.DisableMatchFlow {
		bestOf, secs = 0, 0
	}
	if secs > 0xFFFF {
		secs = 0xFFFF
	}

	b := make([]byte, 0, 16+len(teams)*2)
	b = append(b, MsgMatchState, byte(m.Phase), byte(m.Round), byte(bestOf),
		byte(cfg.WinsNeeded()))
	b = putU16(b, uint16(secs))
	b = append(b, m.Winner, byte(len(teams)))
	// Mode is appended after the team block rather than inserted, so a decoder that stops
	// at the end of the teams still parses everything it understands.
	for _, t := range teams {
		b = append(b, t.ID, byte(m.RoundWins[t.ID]))
	}
	b = append(b, byte(cfg.Mode))
	return b
}

// PlayerState is the decoded 0x85: everything about *you* that the HUD and the shop
// need, as opposed to the world state in a snapshot.
type PlayerState struct {
	Credits   float32
	Upgrades  map[sim.UpgradeID]int
	Energy    float32
	MaxEnergy float32
	Health    float32 // 0-1
	RespawnIn float32 // seconds; 0 when alive
	Boost     float32 // seconds of burn left in the tank
	MaxBoost  float32

	// BoostCooldown is the forced stall left after running the tank dry, in seconds.
	// Non-zero means the bar is not filling and boost cannot be engaged at all, which is
	// a different state from "low" and has to look like one.
	BoostCooldown float32

	// Grounded means the team's life pool ran out and there is no respawn coming.
	Grounded bool
}

// EncodePlayerState builds a 0x85 PlayerState.
//
// The combat fields are appended after the upgrade table rather than inserted before it,
// so the length-prefixed table stays at a fixed offset and an older decoder that stops
// reading at its end still parses the message it understands.
func EncodePlayerState(p *sim.Player, health float32) []byte {
	b := make([]byte, 0, 24+len(sim.UpgradeSpecs)*2)
	b = append(b, MsgPlayerState)
	b = putF32(b, p.Credits)
	b = append(b, byte(len(sim.UpgradeSpecs)))
	for _, spec := range sim.UpgradeSpecs {
		b = append(b, byte(spec.ID), byte(p.Upgrades[spec.ID]))
	}
	b = putF32(b, p.Energy)
	b = putF32(b, p.MaxEnergy())
	b = putF32(b, health)
	b = putF32(b, p.RespawnSeconds())
	b = putF32(b, p.Boost)
	b = putF32(b, p.MaxBoost())
	b = putF32(b, p.BoostCooldown())

	// Grounded cannot be inferred from respawn_in: a player who is out for the round has
	// no countdown running, so the timer reads zero — exactly like being alive.
	var grounded byte
	if p.Grounded() {
		grounded = 1
	}
	b = append(b, grounded)
	return b
}

// DecodePlayerState parses a 0x85 body.
func DecodePlayerState(b []byte) (PlayerState, error) {
	if len(b) < 5 {
		return PlayerState{}, errShort
	}
	n := int(b[4])
	if len(b) < 5+n*2 {
		return PlayerState{}, errShort
	}

	ps := PlayerState{Credits: getF32(b), Upgrades: make(map[sim.UpgradeID]int, n)}
	off := 5
	for i := 0; i < n; i++ {
		ps.Upgrades[sim.UpgradeID(b[off])] = int(b[off+1])
		off += 2
	}

	if len(b) >= off+16 {
		ps.Energy = getF32(b[off:])
		ps.MaxEnergy = getF32(b[off+4:])
		ps.Health = getF32(b[off+8:])
		ps.RespawnIn = getF32(b[off+12:])
	}
	if len(b) >= off+24 {
		ps.Boost = getF32(b[off+16:])
		ps.MaxBoost = getF32(b[off+20:])
	}
	if len(b) >= off+28 {
		ps.BoostCooldown = getF32(b[off+24:])
	}
	if len(b) >= off+29 {
		ps.Grounded = b[off+28] != 0
	}
	return ps, nil
}

// DecodeBuyUpgrade parses a 0x03 body.
func DecodeBuyUpgrade(b []byte) (sim.UpgradeID, error) {
	if len(b) < 1 {
		return 0, errShort
	}
	return sim.UpgradeID(b[0]), nil
}

// EncodeBuyUpgrade builds a 0x03 BuyUpgrade.
func EncodeBuyUpgrade(u sim.UpgradeID) []byte {
	return []byte{MsgBuyUpgrade, byte(u)}
}

/*============================================================================
 * The client side of the protocol.
 *
 * The Go bot (used for load testing and for the Phase 3 gate) speaks these, and having
 * both directions here means every encoder has a decoder to be tested against. The
 * Python client mirrors this same layout.
 *===========================================================================*/

// EncodeHello builds a 0x02 Hello. Names longer than 32 bytes are truncated.
func EncodeHello(name string, teamPref uint8) []byte {
	n := []byte(name)
	if len(n) > 32 {
		n = n[:32]
	}
	b := make([]byte, 0, 3+len(n))
	b = append(b, MsgHello, byte(len(n)))
	b = append(b, n...)
	return append(b, teamPref)
}

// EncodeInput builds a 0x01 Input.
func EncodeInput(in sim.Input) []byte {
	var flags byte
	if in.Grab {
		flags |= inputFlagGrab
	}
	if in.Boost {
		flags |= inputFlagBoost
	}
	if in.Brake {
		flags |= inputFlagBrake
	}
	if in.Fire {
		flags |= inputFlagFire
	}

	b := make([]byte, 0, 30)
	b = append(b, MsgInput)
	b = putU32(b, in.Seq)
	b = putF32(b, in.ThrustFwd)
	b = putF32(b, in.ThrustRight)
	b = putF32(b, in.ThrustUp)
	b = putF32(b, in.Yaw)
	b = putF32(b, in.Pitch)
	b = putF32(b, in.Roll)
	return append(b, flags)
}

// EncodeDebugSet builds a 0x10 DebugSet.
func EncodeDebugSet(param uint16, value float32) []byte {
	b := make([]byte, 0, 7)
	b = append(b, MsgDebugSet)
	b = putU16(b, param)
	return putF32(b, value)
}

// Welcome is the decoded 0x80.
type Welcome struct {
	PlayerID     uint32
	Ship         physics.EntityID
	Team         uint8
	TickRate     uint8
	SnapshotRate uint8
}

// DecodeWelcome parses a 0x80 body.
func DecodeWelcome(b []byte) (Welcome, error) {
	if len(b) < 11 {
		return Welcome{}, errShort
	}
	return Welcome{
		PlayerID:     getU32(b),
		Ship:         physics.EntityID(getU32(b[4:])),
		Team:         b[8],
		TickRate:     b[9],
		SnapshotRate: b[10],
	}, nil
}

// Snapshot is the decoded 0x81.
type Snapshot struct {
	Tick     uint32
	ServerMS uint32
	Bodies   []SnapshotBody
}

// DecodeSnapshot parses a 0x81 body. The caller may pass a slice to reuse.
func DecodeSnapshot(b []byte, reuse []SnapshotBody) (Snapshot, error) {
	if len(b) < 10 {
		return Snapshot{}, errShort
	}
	count := int(getU16(b[8:]))
	if len(b) < 10+count*bodyRecordSize {
		return Snapshot{}, errShort
	}

	bodies := reuse[:0]
	off := 10
	for i := 0; i < count; i++ {
		r := b[off : off+bodyRecordSize]
		bodies = append(bodies, SnapshotBody{
			Entity: physics.EntityID(getU32(r)),
			Kind:   sim.Kind(r[4]),
			Flags:  r[5],
			Tier:   sim.Tier(r[6]),
			Team:   r[7],
			Health: float32(r[8]) / 255,
			Radius: float32(getU16(r[9:])) * radiusQuantum,
			Pos:    physics.Vec3{X: getF32(r[11:]), Y: getF32(r[15:]), Z: getF32(r[19:])},
			Rot:    unpackQuat(getU32(r[23:])),
		})
		off += bodyRecordSize
	}

	return Snapshot{Tick: getU32(b), ServerMS: getU32(b[4:]), Bodies: bodies}, nil
}

// DecodeEvent parses a 0x82 body.
func DecodeEvent(b []byte) (sim.Event, error) {
	if len(b) < 13 {
		return sim.Event{}, errShort
	}
	return sim.Event{
		Type:   sim.EventType(b[0]),
		Entity: physics.EntityID(getU32(b[1:])),
		Player: sim.PlayerID(getU32(b[5:])),
		Value:  getF32(b[9:]),
	}, nil
}

// DecodeTeamState parses a 0x83 body.
func DecodeTeamState(b []byte) ([]sim.Team, error) {
	if len(b) < 1 {
		return nil, errShort
	}
	n := int(b[0])

	teams := make([]sim.Team, 0, n)
	off := 1
	for i := 0; i < n; i++ {
		if len(b) < off+7 {
			return nil, errShort
		}
		t := sim.Team{
			ID:      b[off],
			Score:   getF32(b[off+1:]),
			Members: int(b[off+5]),
		}
		nameLen := int(b[off+6])
		off += 7
		if len(b) < off+nameLen {
			return nil, errShort
		}
		t.Name = string(b[off : off+nameLen])
		off += nameLen

		if len(b) < off+4 {
			return nil, errShort
		}
		t.Station = map[sim.UpgradeID]int{
			sim.UpgradeShields: int(b[off]),
			sim.UpgradeTurrets: int(b[off+1]),
		}
		t.Lives = int(b[off+2])
		t.Color = b[off+3]
		off += 4

		teams = append(teams, t)
	}
	return teams, nil
}

// DebugStats is the decoded 0x90.
type DebugStats struct {
	TickMS   float32
	Bodies   uint16
	Sessions uint16
}

// DecodeDebugStats parses a 0x90 body.
func DecodeDebugStats(b []byte) (DebugStats, error) {
	if len(b) < 8 {
		return DebugStats{}, errShort
	}
	return DebugStats{
		TickMS:   getF32(b),
		Bodies:   getU16(b[4:]),
		Sessions: getU16(b[6:]),
	}, nil
}
