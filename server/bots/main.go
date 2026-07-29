// Command bots drives headless clients against a running server.
//
//	go run ./bots -n 16 -seconds 60
//
// Sixteen bots is the target match size, and this is how that gets tested without
// sixteen humans. Bots hunt the nearest asteroid, haul it toward the mothership, and
// release — a crude approximation of play, but enough to exercise physics, the grab
// constraint, scoring and the full broadcast path under real concurrency.
package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"sync"
	"sync/atomic"
	"time"

	gnet "asteroidsalvage/net"
	"asteroidsalvage/physics"
	"asteroidsalvage/sim"
)

type stats struct {
	snapshots atomic.Int64
	events    atomic.Int64
	deposits  atomic.Int64
	grabs     atomic.Int64
	errors    atomic.Int64
	connected atomic.Int64
}

func main() {
	var (
		url     = flag.String("url", "ws://localhost:8080/ws", "server URL")
		n       = flag.Int("n", 16, "number of bots")
		seconds = flag.Int("seconds", 30, "how long to run")
		seed    = flag.Int64("seed", 42, "RNG seed")
	)
	flag.Parse()

	var st stats
	var wg sync.WaitGroup
	stop := make(chan struct{})

	log.Printf("starting %d bots against %s for %ds", *n, *url, *seconds)

	for i := 0; i < *n; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			runBot(id, *url, rand.New(rand.NewSource(*seed+int64(id))), &st, stop)
		}(i)

		// Stagger connections slightly; 16 simultaneous handshakes is not the thing
		// under test here.
		time.Sleep(25 * time.Millisecond)
	}

	deadline := time.After(time.Duration(*seconds) * time.Second)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	start := time.Now()
	var lastSnaps int64

loop:
	for {
		select {
		case <-deadline:
			break loop
		case <-ticker.C:
			snaps := st.snapshots.Load()
			rate := float64(snaps-lastSnaps) / 5.0
			lastSnaps = snaps
			log.Printf("t=%3.0fs  connected %d/%d  snapshots %d (%.0f/s)  grabs %d  deposits %d  errors %d",
				time.Since(start).Seconds(), st.connected.Load(), *n,
				snaps, rate, st.grabs.Load(), st.deposits.Load(), st.errors.Load())
		}
	}

	close(stop)
	wg.Wait()

	elapsed := time.Since(start).Seconds()
	snaps := st.snapshots.Load()

	fmt.Println()
	fmt.Println("=== load test summary ===")
	fmt.Printf("bots            : %d\n", *n)
	fmt.Printf("duration        : %.1fs\n", elapsed)
	fmt.Printf("snapshots       : %d (%.1f per bot per second)\n",
		snaps, float64(snaps)/float64(*n)/elapsed)
	fmt.Printf("events          : %d\n", st.events.Load())
	fmt.Printf("grabs           : %d\n", st.grabs.Load())
	fmt.Printf("deposits        : %d\n", st.deposits.Load())
	fmt.Printf("errors          : %d\n", st.errors.Load())

	// At 15 Hz each bot should see ~15 snapshots a second. Well below that means the
	// server is not keeping up or the broadcast path is dropping clients.
	perBotPerSec := float64(snaps) / float64(*n) / elapsed
	if st.errors.Load() > 0 || perBotPerSec < 10 {
		fmt.Println("\nRESULT: FAIL")
		os.Exit(1)
	}
	fmt.Println("\nRESULT: PASS")
}

func runBot(id int, url string, rng *rand.Rand, st *stats, stop <-chan struct{}) {
	c, err := gnet.Dial(url)
	if err != nil {
		log.Printf("bot %d: dial: %v", id, err)
		st.errors.Add(1)
		return
	}
	defer c.Close()

	var (
		mu         sync.Mutex
		shipPos    physics.Vec3
		shipRot    physics.Quat
		mothership physics.Vec3
		target     physics.EntityID
		targetPos  physics.Vec3
		holding    bool
		haveShip   bool
	)

	c.OnSnapshot = func(s gnet.Snapshot) {
		st.snapshots.Add(1)

		w, ok := c.Welcome()
		if !ok {
			return
		}

		mu.Lock()
		defer mu.Unlock()

		var bestDist float32 = 1e30
		var best physics.EntityID
		var bestPos physics.Vec3
		holding = false

		for _, b := range s.Bodies {
			switch {
			case b.Entity == w.Ship:
				shipPos, shipRot, haveShip = b.Pos, b.Rot, true
			case b.Kind == sim.KindMothership:
				mothership = b.Pos
			case b.Kind == sim.KindAsteroid || b.Kind == sim.KindSalvage:
				if b.Flags&1 != 0 { // held by someone
					continue
				}
				if d := b.Pos.DistTo(shipPos); d < bestDist {
					bestDist, best, bestPos = d, b.Entity, b.Pos
				}
			}
		}

		// Anything flagged held that is near us is almost certainly ours.
		for _, b := range s.Bodies {
			if b.Flags&1 != 0 && b.Pos.DistTo(shipPos) < 25 {
				holding = true
				break
			}
		}

		target, targetPos = best, bestPos
	}

	c.OnEvent = func(e sim.Event) {
		st.events.Add(1)
		switch e.Type {
		case sim.EventGrabbed:
			st.grabs.Add(1)
		case sim.EventDeposited:
			st.deposits.Add(1)
		}
	}

	c.OnDisconnect = func(error) { st.connected.Add(-1) }

	go c.ReadLoop()

	if err := c.SendHello(fmt.Sprintf("bot-%02d", id), 0xFF); err != nil {
		st.errors.Add(1)
		return
	}
	st.connected.Add(1)

	ticker := time.NewTicker(time.Second / 30)
	defer ticker.Stop()

	var seq uint32
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			seq++

			mu.Lock()
			sp, sr, mp := shipPos, shipRot, mothership
			tp, tgt, held, ready := targetPos, target, holding, haveShip
			mu.Unlock()

			if !ready {
				continue
			}

			// Steer toward the mothership when carrying, otherwise toward the nearest
			// free rock.
			goal := tp
			wantGrab := true
			if held {
				goal = mp
				// Let go once home; the server banks it on contact with the volume.
				if sp.DistTo(mp) < 40 {
					wantGrab = false
				}
			} else if tgt == 0 {
				// Nothing in sight — wander so the bot does not sit still.
				goal = physics.Vec3{
					X: sp.X + float32(rng.NormFloat64()*80),
					Y: sp.Y + float32(rng.NormFloat64()*80),
					Z: sp.Z + float32(rng.NormFloat64()*20),
				}
			}

			in := steer(sp, goal, sr, seq)
			in.Grab = wantGrab
			_ = c.SendInput(in)
		}
	}
}

// steer points the ship at a goal and thrusts toward it.
//
// Controls are body-relative, so the world-space direction to the goal has to be rotated
// into the ship's own frame first. An earlier version skipped that and steered in world
// terms; the bots simply spun on the spot and reached nothing in 45 seconds, which made
// the load test exercise the broadcast path but never the grab constraint or collisions.
func steer(from, to physics.Vec3, rot physics.Quat, seq uint32) sim.Input {
	d := to.Sub(from)
	dist := d.Len()
	if dist < 1e-3 {
		return sim.Input{Seq: seq}
	}

	// World direction -> ship-local. Local axes: +Y forward, +X right, +Z up.
	local := rot.Conjugate().Rotate(d.Scale(1 / dist))

	clamp := func(v float32) float32 {
		if v < -1 {
			return -1
		}
		if v > 1 {
			return 1
		}
		return v
	}

	// Gain of 2.5 turns hard when badly misaligned and eases off near the target,
	// instead of oscillating around it.
	in := sim.Input{
		Seq:   seq,
		Yaw:   clamp(-local.X * 2.5),
		Pitch: clamp(local.Z * 2.5),
	}

	// Only burn once roughly pointing the right way, or the bot accelerates away from
	// wherever it is trying to go.
	if local.Y > 0.5 {
		in.ThrustFwd = 1
	} else if local.Y < -0.2 {
		in.Brake = true
	}

	return in
}
