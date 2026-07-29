package sim

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Tuning holds every value the ImGui dev console can change at runtime.
//
// These are the numbers that decide whether the game feels good, so they are
// deliberately data rather than constants: the console pushes DebugSet messages and the
// next tick picks them up, with no rebuild and no restart. The IDs are fixed wire
// values — see the parameter table in shared/protocol.md, which is authoritative.
type Tuning struct {
	mu sync.RWMutex

	// Grab constraint. The held object is pulled toward a hold point in front of the
	// ship by a spring-damper, never parented to it — that is what makes hauling feel
	// physical instead of glued.
	GrabSpring        float32 // N/m toward the hold point
	GrabDamping       float32 // damping per sqrt(kg); critical is 2*sqrt(GrabSpring)
	GrabMaxForce      float32 // N; exceeding this drops the object
	GrabHoldDistance  float32 // m in front of the ship
	GrabReactionScale float32 // how much of the reaction force the ship feels
	GrabRange         float32 // m; how far the tractor beam reaches to acquire

	// Ship handling.
	ShipThrust         float32 // N per unit of input
	ShipTorque         float32 // N·m per unit of input
	ShipLinearDamping  float32
	ShipAngularDamping float32

	// Salvage fragility.
	SalvageDamageThreshold float32 // m/s below which impacts are harmless
	SalvageDamageScale     float32 // integrity lost per m/s over the threshold
}

// Wire parameter IDs. Must match shared/protocol.md.
const (
	ParamGrabSpring        uint16 = 0x0001
	ParamGrabDamping       uint16 = 0x0002
	ParamGrabMaxForce      uint16 = 0x0003
	ParamGrabHoldDistance  uint16 = 0x0004
	ParamGrabReactionScale uint16 = 0x0005
	ParamGrabRange         uint16 = 0x0006

	ParamShipThrust         uint16 = 0x0010
	ParamShipTorque         uint16 = 0x0011
	ParamShipLinearDamping  uint16 = 0x0012
	ParamShipAngularDamping uint16 = 0x0013

	ParamSalvageDamageThreshold uint16 = 0x0020
	ParamSalvageDamageScale     uint16 = 0x0021
)

// paramDef describes one tunable for the console and the config file.
type paramDef struct {
	ID       uint16
	Name     string
	Min, Max float32
	get      func(*Tuning) float32
	set      func(*Tuning, float32)
}

// paramDefs is the single registry driving DebugSet handling, feel.toml load/save, and
// the console's slider list. Adding a tunable means adding one entry here.
var paramDefs = []paramDef{
	{ParamGrabSpring, "grab.spring", 0, 2000,
		func(t *Tuning) float32 { return t.GrabSpring },
		func(t *Tuning, v float32) { t.GrabSpring = v }},
	{ParamGrabDamping, "grab.damping", 0, 200,
		func(t *Tuning) float32 { return t.GrabDamping },
		func(t *Tuning, v float32) { t.GrabDamping = v }},
	{ParamGrabMaxForce, "grab.max_force", 0, 20000,
		func(t *Tuning) float32 { return t.GrabMaxForce },
		func(t *Tuning, v float32) { t.GrabMaxForce = v }},
	{ParamGrabHoldDistance, "grab.hold_distance", 1, 30,
		func(t *Tuning) float32 { return t.GrabHoldDistance },
		func(t *Tuning, v float32) { t.GrabHoldDistance = v }},
	{ParamGrabReactionScale, "grab.reaction_scale", 0, 2,
		func(t *Tuning) float32 { return t.GrabReactionScale },
		func(t *Tuning, v float32) { t.GrabReactionScale = v }},
	{ParamGrabRange, "grab.range", 5, 200,
		func(t *Tuning) float32 { return t.GrabRange },
		func(t *Tuning, v float32) { t.GrabRange = v }},

	{ParamShipThrust, "ship.thrust", 0, 5000,
		func(t *Tuning) float32 { return t.ShipThrust },
		func(t *Tuning, v float32) { t.ShipThrust = v }},
	// Torque and angular damping need far wider ranges than they first had. The ship's
	// moment of inertia is 0.4*m*r^2 = 192 kg*m^2, so with damping capped at 10 the
	// rotational time constant I/c was 19-35 seconds: the ship wound up slowly and then
	// would not stop turning. Handling is governed by that time constant, not by the
	// raw numbers, so the ranges have to reach values that produce a sane one.
	{ParamShipTorque, "ship.torque", 0, 6000,
		func(t *Tuning) float32 { return t.ShipTorque },
		func(t *Tuning, v float32) { t.ShipTorque = v }},
	{ParamShipLinearDamping, "ship.linear_damping", 0, 5,
		func(t *Tuning) float32 { return t.ShipLinearDamping },
		func(t *Tuning, v float32) { t.ShipLinearDamping = v }},
	{ParamShipAngularDamping, "ship.angular_damping", 0, 3000,
		func(t *Tuning) float32 { return t.ShipAngularDamping },
		func(t *Tuning, v float32) { t.ShipAngularDamping = v }},

	{ParamSalvageDamageThreshold, "salvage.damage_threshold", 0, 50,
		func(t *Tuning) float32 { return t.SalvageDamageThreshold },
		func(t *Tuning, v float32) { t.SalvageDamageThreshold = v }},
	{ParamSalvageDamageScale, "salvage.damage_scale", 0, 1,
		func(t *Tuning) float32 { return t.SalvageDamageScale },
		func(t *Tuning, v float32) { t.SalvageDamageScale = v }},
}

var paramByID = func() map[uint16]paramDef {
	m := make(map[uint16]paramDef, len(paramDefs))
	for _, p := range paramDefs {
		m[p.ID] = p
	}
	return m
}()

var paramByName = func() map[string]paramDef {
	m := make(map[string]paramDef, len(paramDefs))
	for _, p := range paramDefs {
		m[p.Name] = p
	}
	return m
}()

// DefaultTuning is the starting point for tuning, not a claim that it feels right.
// The whole reason the dev console exists is that these numbers must be found by
// playing, not by reasoning.
func DefaultTuning() *Tuning {
	return &Tuning{
		GrabSpring:        220,
		GrabDamping:       28,
		GrabMaxForce:      1800,
		GrabHoldDistance:  6,
		GrabReactionScale: 1.0,
		// Short on purpose: you fly up to a rock and take it, rather than vacuuming the
		// field from a standoff. Note the interaction with GrabHoldDistance — cargo is
		// held at GrabHoldDistance + both radii, so for the biggest rocks (5.5 m) the
		// hold point is already ~13.5 m out and the beam barely reaches further than it
		// holds. Raising one usually means raising the other.
		GrabRange: 15,

		ShipThrust: 1800,
		// Tuned as a pair, via the two things a player actually feels:
		//
		//   top turn rate  w_max = torque / angular_damping   -> 1.55 rad/s (~89 deg/s)
		//   responsiveness tau   = inertia / angular_damping  -> 0.21 s
		//
		// Inertia is 0.4*m*r^2 = 192 for the 120 kg, 2 m ship. Pick the feel first and
		// solve for the constants; picking the constants directly is how this ended up
		// with a 35-second time constant that read as "too sensitive" because the ship
		// never stopped turning.
		ShipTorque:         1400,
		ShipLinearDamping:  0.35,
		ShipAngularDamping: 900,

		SalvageDamageThreshold: 12,
		SalvageDamageScale:     0.04,
	}
}

// Snapshot returns a lock-free copy for use during a tick.
func (t *Tuning) Snapshot() Tuning {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return Tuning{
		GrabSpring:             t.GrabSpring,
		GrabDamping:            t.GrabDamping,
		GrabMaxForce:           t.GrabMaxForce,
		GrabHoldDistance:       t.GrabHoldDistance,
		GrabReactionScale:      t.GrabReactionScale,
		GrabRange:              t.GrabRange,
		ShipThrust:             t.ShipThrust,
		ShipTorque:             t.ShipTorque,
		ShipLinearDamping:      t.ShipLinearDamping,
		ShipAngularDamping:     t.ShipAngularDamping,
		SalvageDamageThreshold: t.SalvageDamageThreshold,
		SalvageDamageScale:     t.SalvageDamageScale,
	}
}

// SetByID applies a DebugSet. Unknown IDs are ignored and out-of-range values are
// clamped — this is fed by the network, so it must not trust its input.
func (t *Tuning) SetByID(id uint16, v float32) bool {
	p, ok := paramByID[id]
	if !ok {
		return false
	}
	if v < p.Min {
		v = p.Min
	}
	if v > p.Max {
		v = p.Max
	}
	t.mu.Lock()
	p.set(t, v)
	t.mu.Unlock()
	return true
}

// Params returns every tunable's current value, for the console's slider list.
func (t *Tuning) Params() []ParamValue {
	t.mu.RLock()
	defer t.mu.RUnlock()

	out := make([]ParamValue, 0, len(paramDefs))
	for _, p := range paramDefs {
		out = append(out, ParamValue{
			ID: p.ID, Name: p.Name, Value: p.get(t), Min: p.Min, Max: p.Max,
		})
	}
	return out
}

// ParamValue is one tunable, as reported to the console.
type ParamValue struct {
	ID       uint16
	Name     string
	Value    float32
	Min, Max float32
}

// Save writes the tuning to a flat `key = value` file so a good feel is reproducible
// rather than rediscovered. Deliberately hand-rolled: the format is flat, and this
// keeps the server dependency-free apart from the websocket layer.
func (t *Tuning) Save(path string) error {
	vals := t.Params()
	sort.Slice(vals, func(i, j int) bool { return vals[i].Name < vals[j].Name })

	var b strings.Builder
	b.WriteString("# Tuned feel values for Asteroid Salvage.\n")
	b.WriteString("# Written by the dev console (tools/devconsole). Safe to edit by hand.\n")
	b.WriteString("# Parameter meanings and ranges: shared/protocol.md\n\n")
	for _, v := range vals {
		fmt.Fprintf(&b, "%s = %g\n", v.Name, v.Value)
	}

	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// Load reads a file written by Save. A missing file is not an error — the defaults
// stand — so a fresh clone runs without any config present.
func (t *Tuning) Load(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		key, valStr, ok := strings.Cut(s, "=")
		if !ok {
			return fmt.Errorf("%s:%d: expected `key = value`, got %q", path, line, s)
		}
		key = strings.TrimSpace(key)
		valStr = strings.TrimSpace(valStr)

		p, ok := paramByName[key]
		if !ok {
			// Forward compatibility: an unknown key is likely a parameter added by a
			// newer build. Skip rather than fail.
			continue
		}
		v, err := strconv.ParseFloat(valStr, 32)
		if err != nil {
			return fmt.Errorf("%s:%d: %q is not a number", path, line, valStr)
		}
		t.SetByID(p.ID, float32(v))
	}
	return sc.Err()
}
