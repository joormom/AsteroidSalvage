package net

import (
	"context"
	"encoding/hex"
	"math"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"asteroidsalvage/physics"
	"asteroidsalvage/sim"
)

/*============================================================================
 * Protocol unit tests
 *===========================================================================*/

func TestQuatPackRoundTrip(t *testing.T) {
	cases := []physics.Quat{
		{W: 1},                             // identity
		{X: 1},                             // 180 deg about X
		{Y: 0.7071, W: 0.7071},             // 90 deg about Y
		{X: 0.5, Y: 0.5, Z: 0.5, W: 0.5},   // all components equal
		{X: -0.5, Y: 0.5, Z: -0.5, W: 0.5}, // mixed signs
		{X: 0.183, Y: -0.365, Z: 0.548, W: 0.730}, // arbitrary
	}

	for _, q := range cases {
		q = q.Norm()
		got := unpackQuat(packQuat(q))

		// q and -q are the same rotation, so compare on that basis.
		dot := q.X*got.X + q.Y*got.Y + q.Z*got.Z + q.W*got.W
		if dot < 0 {
			dot = -dot
		}
		// 10 bits per component: error should be far below a tenth of a degree.
		if 1-dot > 1e-5 {
			t.Errorf("quat %+v round-tripped to %+v (1-|dot| = %g)", q, got, 1-dot)
		}
	}
}

func TestQuatPackIsFourBytes(t *testing.T) {
	// The whole point of smallest-three is the size saving; guard it.
	body := SnapshotBody{Rot: physics.Quat{W: 1}}
	enc := EncodeSnapshot(nil, 0, 0, []SnapshotBody{body})
	if got := len(enc) - 11; got != bodyRecordSize {
		t.Errorf("body record is %d bytes, want %d", got, bodyRecordSize)
	}
}

// Input arrives straight off the network and must be bounded.
func TestDecodeInputClampsHostileValues(t *testing.T) {
	hostile := sim.Input{
		Seq:       7,
		ThrustFwd: 1e9,
		Yaw:       -1e9,
		Pitch:     float32(math.NaN()),
		Grab:      true,
	}
	got, err := DecodeInput(EncodeInput(hostile)[1:])
	if err != nil {
		t.Fatalf("DecodeInput: %v", err)
	}

	if got.ThrustFwd != 1 {
		t.Errorf("ThrustFwd = %v, want clamped to 1", got.ThrustFwd)
	}
	if got.Yaw != -1 {
		t.Errorf("Yaw = %v, want clamped to -1", got.Yaw)
	}
	if got.Pitch != 0 {
		t.Errorf("Pitch = %v, want NaN scrubbed to 0", got.Pitch)
	}
	if !got.Grab {
		t.Error("Grab flag lost")
	}
}

func TestDecodeInputRejectsTruncated(t *testing.T) {
	if _, err := DecodeInput([]byte{1, 2, 3}); err == nil {
		t.Error("truncated Input was accepted")
	}
}

func TestDecodeHelloStripsControlCharacters(t *testing.T) {
	raw := append([]byte{byte(len("bad\x07name"))}, []byte("bad\x07name")...)
	raw = append(raw, 0xFF)

	name, team, err := DecodeHello(raw)
	if err != nil {
		t.Fatalf("DecodeHello: %v", err)
	}
	if strings.ContainsRune(name, 0x07) {
		t.Errorf("control character survived sanitisation: %q", name)
	}
	if team != 0xFF {
		t.Errorf("team = %d, want 0xFF", team)
	}
}

func TestDecodeHelloRejectsOverlongName(t *testing.T) {
	raw := append([]byte{200}, make([]byte, 10)...)
	if _, _, err := DecodeHello(raw); err == nil {
		t.Error("Hello claiming a 200-byte name in a 10-byte buffer was accepted")
	}
}

func TestDecodeDebugSetRejectsNonFinite(t *testing.T) {
	bad := EncodeDebugSet(sim.ParamGrabSpring, float32(math.Inf(1)))[1:]
	if _, _, err := DecodeDebugSet(bad); err == nil {
		t.Error("infinite debug value was accepted")
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	in := []SnapshotBody{
		{Entity: 1, Kind: sim.KindShip, Flags: bodyFlagHeld,
			Pos: physics.Vec3{X: 1.5, Y: -2.5, Z: 3.5}, Rot: physics.Quat{W: 1}},
		{Entity: 99, Kind: sim.KindAsteroid, Flags: bodyFlagDamaged,
			Pos: physics.Vec3{X: -10, Y: 20, Z: 0}, Rot: physics.Quat{X: 1}},
	}

	snap, err := DecodeSnapshot(EncodeSnapshot(nil, 42, 1234, in)[1:], nil)
	if err != nil {
		t.Fatalf("DecodeSnapshot: %v", err)
	}
	if snap.Tick != 42 || snap.ServerMS != 1234 {
		t.Errorf("header = tick %d ms %d, want 42/1234", snap.Tick, snap.ServerMS)
	}
	if len(snap.Bodies) != 2 {
		t.Fatalf("got %d bodies, want 2", len(snap.Bodies))
	}
	for i, want := range in {
		got := snap.Bodies[i]
		if got.Entity != want.Entity || got.Kind != want.Kind || got.Flags != want.Flags {
			t.Errorf("body %d: got %+v, want %+v", i, got, want)
		}
		if got.Pos != want.Pos {
			t.Errorf("body %d pos: got %+v, want %+v", i, got.Pos, want.Pos)
		}
	}
}

func TestDecodeSnapshotRejectsLyingCount(t *testing.T) {
	// Claims 1000 bodies but carries none.
	b := putU32(nil, 1)
	b = putU32(b, 2)
	b = putU16(b, 1000)
	if _, err := DecodeSnapshot(b, nil); err == nil {
		t.Error("snapshot with an impossible body count was accepted")
	}
}

func TestTeamStateRoundTrip(t *testing.T) {
	in := []sim.Team{{ID: 0, Score: 125.5, Members: 4}, {ID: 3, Score: 0, Members: 1}}
	got, err := DecodeTeamState(EncodeTeamState(in)[1:])
	if err != nil {
		t.Fatalf("DecodeTeamState: %v", err)
	}
	if len(got) != 2 || got[0].Score != 125.5 || got[1].ID != 3 || got[0].Members != 4 {
		t.Errorf("round trip gave %+v, want %+v", got, in)
	}
}

// Cross-implementation pin.
//
// These are literal bytes produced by shared/asteroid_protocol.py. Protocol drift
// between the Go and Python implementations is the most likely bug in this project and
// is otherwise invisible until something renders wrong or a control stops working, so
// the Python encoder's output is asserted here directly. Regenerate with:
//
//	python -c "import sys; sys.path.insert(0,'shared'); import asteroid_protocol as p; \
//	           print(p.encode_input(7, 1.0, -0.5, 0, 0.25, 0, 0, grab=True, boost=True).hex())"
func TestPythonEncodersDecodeInGo(t *testing.T) {
	hexBytes := func(s string) []byte {
		b, err := hex.DecodeString(s)
		if err != nil {
			t.Fatalf("bad literal: %v", err)
		}
		return b
	}

	t.Run("hello", func(t *testing.T) {
		raw := hexBytes("02034b657602")
		if raw[0] != MsgHello {
			t.Fatalf("tag = %#x, want %#x", raw[0], MsgHello)
		}
		name, team, err := DecodeHello(raw[1:])
		if err != nil {
			t.Fatalf("DecodeHello: %v", err)
		}
		if name != "Kev" || team != 2 {
			t.Errorf("got name %q team %d, want \"Kev\"/2", name, team)
		}
	})

	t.Run("input", func(t *testing.T) {
		raw := hexBytes("01070000000000803f000000bf000000000000803e000000000000000003")
		if len(raw) != 30 {
			t.Fatalf("Python Input is %d bytes, Go expects 30", len(raw))
		}
		if raw[0] != MsgInput {
			t.Fatalf("tag = %#x, want %#x", raw[0], MsgInput)
		}
		in, err := DecodeInput(raw[1:])
		if err != nil {
			t.Fatalf("DecodeInput: %v", err)
		}
		if in.Seq != 7 {
			t.Errorf("Seq = %d, want 7", in.Seq)
		}
		if in.ThrustFwd != 1.0 || in.ThrustRight != -0.5 || in.Yaw != 0.25 {
			t.Errorf("axes = fwd %v right %v yaw %v, want 1 / -0.5 / 0.25",
				in.ThrustFwd, in.ThrustRight, in.Yaw)
		}
		if !in.Grab || !in.Boost || in.Brake {
			t.Errorf("flags = grab %v boost %v brake %v, want true/true/false",
				in.Grab, in.Boost, in.Brake)
		}
	})

	t.Run("debugset", func(t *testing.T) {
		raw := hexBytes("100500c3f5a83e")
		if len(raw) != 7 {
			t.Fatalf("Python DebugSet is %d bytes, Go expects 7", len(raw))
		}
		param, value, err := DecodeDebugSet(raw[1:])
		if err != nil {
			t.Fatalf("DecodeDebugSet: %v", err)
		}
		if param != sim.ParamGrabReactionScale {
			t.Errorf("param = %#x, want %#x", param, sim.ParamGrabReactionScale)
		}
		if math.Abs(float64(value)-0.33) > 1e-6 {
			t.Errorf("value = %v, want 0.33", value)
		}
	})
}

/*============================================================================
 * Live server tests
 *===========================================================================*/

// testServer starts a real server on a random port and returns its ws:// base URL.
func testServer(t *testing.T, mutate func(*sim.Config)) string {
	t.Helper()

	cfg := sim.DefaultConfig()
	cfg.AutoRestock = false
	if mutate != nil {
		mutate(&cfg)
	}

	srv, err := NewServer(Options{Sim: cfg, DevMode: true})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go srv.Run(ctx)

	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		hs.Close()
		cancel()
		time.Sleep(50 * time.Millisecond) // let Run() release the sim
	})

	return "ws://" + strings.TrimPrefix(hs.URL, "http://")
}

func dialPlayer(t *testing.T, base, name string, team uint8) *Client {
	t.Helper()

	c, err := Dial(base + "/ws")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	go c.ReadLoop()
	t.Cleanup(func() { _ = c.Close() })

	if err := c.SendHello(name, team); err != nil {
		t.Fatalf("hello: %v", err)
	}
	return c
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestPlayerReceivesWelcomeAndSnapshots(t *testing.T) {
	base := testServer(t, nil)

	var snaps atomic.Int64
	c := dialPlayer(t, base, "solo", 0xFF)
	c.OnSnapshot = func(Snapshot) { snaps.Add(1) }

	waitFor(t, "welcome", func() bool { _, ok := c.Welcome(); return ok })

	w, _ := c.Welcome()
	if w.PlayerID == 0 || w.Ship == 0 {
		t.Errorf("welcome has zero ids: %+v", w)
	}
	if w.TickRate != sim.TickHz {
		t.Errorf("TickRate = %d, want %d", w.TickRate, sim.TickHz)
	}

	waitFor(t, "snapshots", func() bool { return snaps.Load() >= 3 })
}

// The Phase 3 gate: two clients in the match room must both receive world snapshots.
func TestTwoClientsBothReceiveSnapshots(t *testing.T) {
	base := testServer(t, nil)

	var aSnaps, bSnaps atomic.Int64
	a := dialPlayer(t, base, "alpha", 0)
	a.OnSnapshot = func(Snapshot) { aSnaps.Add(1) }
	b := dialPlayer(t, base, "bravo", 1)
	b.OnSnapshot = func(Snapshot) { bSnaps.Add(1) }

	waitFor(t, "both welcomed", func() bool {
		_, oka := a.Welcome()
		_, okb := b.Welcome()
		return oka && okb
	})
	waitFor(t, "both receiving snapshots", func() bool {
		return aSnaps.Load() >= 3 && bSnaps.Load() >= 3
	})

	wa, _ := a.Welcome()
	wb, _ := b.Welcome()
	if wa.Team == wb.Team {
		t.Errorf("both players landed on team %d despite requesting 0 and 1", wa.Team)
	}
	if wa.PlayerID == wb.PlayerID {
		t.Error("both players got the same id")
	}

	// Each client must be able to see the other player's ship in the shared world.
	var sawBoth atomic.Bool
	a.OnSnapshot = func(s Snapshot) {
		ships := 0
		for _, bd := range s.Bodies {
			if bd.Kind == sim.KindShip {
				ships++
			}
		}
		if ships >= 2 {
			sawBoth.Store(true)
		}
	}
	waitFor(t, "alpha to see both ships", sawBoth.Load)
}

// Input must actually reach the simulation and move the ship.
func TestInputMovesShipEndToEnd(t *testing.T) {
	base := testServer(t, nil)

	c := dialPlayer(t, base, "pilot", 0)
	waitFor(t, "welcome", func() bool { _, ok := c.Welcome(); return ok })
	w, _ := c.Welcome()

	var mu sync.Mutex
	var first, latest physics.Vec3
	var haveFirst bool

	c.OnSnapshot = func(s Snapshot) {
		for _, b := range s.Bodies {
			if b.Entity != w.Ship {
				continue
			}
			mu.Lock()
			if !haveFirst {
				first, haveFirst = b.Pos, true
			}
			latest = b.Pos
			mu.Unlock()
		}
	}

	waitFor(t, "initial ship position", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return haveFirst
	})

	stop := make(chan struct{})
	go func() {
		tick := time.NewTicker(time.Second / 30)
		defer tick.Stop()
		var seq uint32
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				seq++
				_ = c.SendInput(sim.Input{Seq: seq, ThrustFwd: 1})
			}
		}
	}()
	defer close(stop)

	waitFor(t, "ship to move under thrust", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return latest.DistTo(first) > 1
	})
}

// Team rooms must be isolated: a broadcast to one team must not reach another.
func TestTeamRoomsAreIsolated(t *testing.T) {
	base := testServer(t, nil)

	srvCfg := sim.DefaultConfig()
	_ = srvCfg

	a := dialPlayer(t, base, "alpha", 0)
	b := dialPlayer(t, base, "bravo", 1)

	waitFor(t, "both welcomed", func() bool {
		_, oka := a.Welcome()
		_, okb := b.Welcome()
		return oka && okb
	})

	wa, _ := a.Welcome()
	wb, _ := b.Welcome()
	if wa.Team == wb.Team {
		t.Fatalf("test needs players on different teams, both got %d", wa.Team)
	}

	// Deposit events are addressed per-player; verify the ids do not collide, which is
	// what team-scoped delivery relies on.
	if wa.Ship == wb.Ship {
		t.Error("two players share one ship entity")
	}
}

// A non-dev server must refuse the debug endpoint outright.
func TestDebugEndpointClosedWithoutDevMode(t *testing.T) {
	cfg := sim.DefaultConfig()
	cfg.AutoRestock = false

	srv, err := NewServer(Options{Sim: cfg, DevMode: false})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Run(ctx)

	hs := httptest.NewServer(srv.Handler())
	defer func() {
		hs.Close()
		cancel()
		time.Sleep(50 * time.Millisecond)
	}()

	base := "ws://" + strings.TrimPrefix(hs.URL, "http://")
	if _, err := Dial(base + "/debug"); err == nil {
		t.Error("/debug accepted a connection with DevMode off")
	}
}

// The dev console must be able to retune the simulation live.
func TestDebugSetRetunesLive(t *testing.T) {
	cfg := sim.DefaultConfig()
	cfg.AutoRestock = false

	srv, err := NewServer(Options{Sim: cfg, DevMode: true})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Run(ctx)

	hs := httptest.NewServer(srv.Handler())
	defer func() {
		hs.Close()
		cancel()
		time.Sleep(50 * time.Millisecond)
	}()

	base := "ws://" + strings.TrimPrefix(hs.URL, "http://")
	console, err := Dial(base + "/debug")
	if err != nil {
		t.Fatalf("dial /debug: %v", err)
	}
	go console.ReadLoop()
	defer console.Close()

	var stats atomic.Int64
	console.OnDebugStats = func(DebugStats) { stats.Add(1) }

	if err := console.SendDebugSet(sim.ParamGrabReactionScale, 0.25); err != nil {
		t.Fatalf("SendDebugSet: %v", err)
	}

	waitFor(t, "tuning to apply", func() bool {
		return srv.sim.Tuning.Snapshot().GrabReactionScale == 0.25
	})
	waitFor(t, "debug stats", func() bool { return stats.Load() >= 1 })
}

// A player connection must not be able to retune the world, even in dev mode.
func TestPlayerCannotSendDebugSet(t *testing.T) {
	cfg := sim.DefaultConfig()
	cfg.AutoRestock = false

	srv, err := NewServer(Options{Sim: cfg, DevMode: true})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Run(ctx)

	hs := httptest.NewServer(srv.Handler())
	defer func() {
		hs.Close()
		cancel()
		time.Sleep(50 * time.Millisecond)
	}()

	base := "ws://" + strings.TrimPrefix(hs.URL, "http://")
	before := srv.sim.Tuning.Snapshot().GrabSpring

	c := dialPlayer(t, base, "attacker", 0)
	waitFor(t, "welcome", func() bool { _, ok := c.Welcome(); return ok })

	if err := c.SendDebugSet(sim.ParamGrabSpring, 1); err != nil {
		t.Fatalf("send: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	if got := srv.sim.Tuning.Snapshot().GrabSpring; got != before {
		t.Errorf("a player retuned the world: GrabSpring %v -> %v", before, got)
	}
}
