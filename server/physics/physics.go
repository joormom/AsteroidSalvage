// Package physics wraps the lagrange C physics library for the server simulation.
//
// The C side (bridge.c) exists because every lagrange function is `static inline`,
// which cgo cannot call directly. It is also a batching boundary: lagrange's entity
// lookup is a linear scan, so this package moves the whole world per call rather than
// one entity per call. Callers should use ApplyForces/Step/Snapshot in that order,
// once per tick, and avoid the per-entity accessors in the hot path.
//
// Game state — what a body *is*, what it is worth, who is holding it — lives in
// package sim, not here. This package knows only masses and forces.
package physics

/*
#cgo CFLAGS: -I${SRCDIR}/../../vendor/lagrange/include
#cgo CFLAGS: -I${SRCDIR}/../../vendor/lagrange/libspatial/include
#cgo CFLAGS: -std=c11 -O2
#cgo LDFLAGS: -lm

#include "bridge.h"
*/
import "C"

import (
	"errors"
	"unsafe"
)

// EntityID is a lagrange entity handle. The zero value is invalid.
type EntityID uint64

// Vec3 is a right-handed, Z-up vector in metres (see shared/protocol.md).
type Vec3 struct{ X, Y, Z float32 }

// Quat is a rotation quaternion.
type Quat struct{ X, Y, Z, W float32 }

// BodyState is one body's physics state at a point in time.
type BodyState struct {
	Entity   EntityID
	Pos      Vec3
	Rot      Quat
	Vel      Vec3
	AngVel   Vec3
	Mass     float32
	Sleeping bool
}

// ForceCmd applies a force and torque to one entity for one tick.
// Multiple commands for the same entity accumulate, which is what lets a ship receive
// engine thrust and a grab reaction force in the same tick.
type ForceCmd struct {
	Entity EntityID
	Force  Vec3
	Torque Vec3
}

// Collision is a contact observed during the previous Step, captured *before* contact
// resolution — so RelSpeed is the true impact speed, which is what salvage damage is
// computed from.
type Collision struct {
	A, B     EntityID
	RelSpeed float32
}

// World owns a lagrange simulation. Not safe for concurrent use; the server drives it
// from a single tick goroutine.
type World struct {
	w *C.ag_world

	// Reused across ticks so a 30 Hz loop does not allocate.
	snapBuf  []C.ag_body_state
	collBuf  []C.ag_collision
	forceBuf []C.ag_force_cmd
	outBuf   []BodyState
	outColl  []Collision

	maxEntities int
}

const maxCollisionsPerTick = 4096 // must match AG_COLLISION_CAP in bridge.c

// NewWorld creates a simulation stepping at a fixed timeStep (seconds).
// maxEntities is preallocated and never grows — lagrange returns an invalid entity
// once storage is full.
func NewWorld(timeStep float32, maxEntities int) (*World, error) {
	if timeStep <= 0 {
		return nil, errors.New("physics: timeStep must be positive")
	}
	if maxEntities <= 0 {
		return nil, errors.New("physics: maxEntities must be positive")
	}

	w := C.ag_world_create(C.float(timeStep), C.size_t(maxEntities))
	if w == nil {
		return nil, errors.New("physics: ag_world_create failed (out of memory)")
	}

	return &World{
		w:           w,
		snapBuf:     make([]C.ag_body_state, maxEntities),
		collBuf:     make([]C.ag_collision, maxCollisionsPerTick),
		outBuf:      make([]BodyState, 0, maxEntities),
		outColl:     make([]Collision, 0, 256),
		maxEntities: maxEntities,
	}, nil
}

// Close releases the underlying world. Safe to call more than once.
func (w *World) Close() {
	if w.w != nil {
		C.ag_world_free(w.w)
		w.w = nil
	}
}

// SpawnSphere adds a sphere body. mass <= 0 creates a static (immovable) body.
// Returns 0 if storage is full.
func (w *World) SpawnSphere(mass, radius float32, pos Vec3) EntityID {
	return EntityID(C.ag_spawn_sphere(w.w,
		C.float(mass), C.float(radius),
		C.float(pos.X), C.float(pos.Y), C.float(pos.Z)))
}

// Shape identifies a collider's geometry. Used with PairSupported.
type Shape int

const (
	ShapeSphere   Shape = C.AG_SHAPE_SPHERE
	ShapeBox      Shape = C.AG_SHAPE_BOX
	ShapeCapsule  Shape = C.AG_SHAPE_CAPSULE
	ShapeCylinder Shape = C.AG_SHAPE_CYLINDER
	ShapePlane    Shape = C.AG_SHAPE_PLANE
)

func (s Shape) String() string {
	switch s {
	case ShapeSphere:
		return "sphere"
	case ShapeBox:
		return "box"
	case ShapeCapsule:
		return "capsule"
	case ShapeCylinder:
		return "cylinder"
	case ShapePlane:
		return "plane"
	}
	return "unknown"
}

// PairSupported reports whether two shapes can collide with each other.
//
// Not every pair is implemented: lagrange has no routine for capsule/box,
// cylinder/cylinder, cylinder/box, plane/capsule or plane/plane, and an unsupported pair
// passes through in silence rather than erroring. Check this before introducing a shape
// that has to be solid against something specific.
//
// Every dynamic body in this game is a sphere, and sphere collides with all five, so the
// gaps only bite if a non-sphere is ever given a mass.
func PairSupported(a, b Shape) bool {
	return bool(C.ag_collider_pair_supported(C.int(a), C.int(b)))
}

// SpawnBox adds a box body given its half-extents. mass <= 0 creates a static body.
//
// Boxes collide properly against spheres and planes, and honour their rotation — set it
// with SetRotation. Box against box is the one gap: lagrange's routine for that pair is an
// AABB overlap test that ignores both rotations, so it is only correct for axis-aligned
// boxes. It costs nothing today because two boxes are always two static bodies, which the
// resolver skips before it looks at shapes; giving a box a mass is what would expose it.
func (w *World) SpawnBox(mass float32, half Vec3, pos Vec3) EntityID {
	return EntityID(C.ag_spawn_box(w.w,
		C.float(mass),
		C.float(half.X), C.float(half.Y), C.float(half.Z),
		C.float(pos.X), C.float(pos.Y), C.float(pos.Z)))
}

// Axis names the body-local axis a capsule or cylinder runs along.
type Axis int

const (
	// AxisY is lagrange's own convention and the cheaper choice: its inertia tensor is
	// built around Y too, so shape and spin resistance agree with nothing to fix up.
	AxisY Axis = C.AG_AXIS_Y

	// AxisZ is for a shape whose long axis has to be the game's up while the body itself
	// stays unrotated. The client draws every body at its snapshot rotation
	// (client/render.py:341), so orienting a collider by rotating the body also rotates
	// the model — a saucer or ring station laid out in its local XY plane would visibly
	// tip on its side. Inertia is swizzled to match.
	AxisZ Axis = C.AG_AXIS_Z
)

func (a Axis) String() string {
	if a == AxisZ {
		return "Z"
	}
	return "Y"
}

// SpawnCapsule adds a capsule: a cylinder with hemispherical caps. halfHeight is the half
// length of the straight section, so the total extent along the axis is
// 2*(halfHeight+radius).
//
// axis picks which body-local axis it runs along; SetRotation still orients the body on top
// of that. Prefer AxisY where the choice is free — see the Axis constants.
//
// This is the right shape for anything long and thin that a ship should slide along rather
// than catch on — a girder, a strut, a wreck's spine. A sphere cluster approximates it at
// several times the cost and with a bumpy surface.
func (w *World) SpawnCapsule(mass, radius, halfHeight float32, axis Axis, pos Vec3) EntityID {
	return EntityID(C.ag_spawn_capsule(w.w,
		C.float(mass), C.float(radius), C.float(halfHeight), C.int(axis),
		C.float(pos.X), C.float(pos.Y), C.float(pos.Z)))
}

// SpawnCylinder adds a flat-capped cylinder along the given body-local axis.
//
// A wide, shallow cylinder is the shape of a saucer or a disc — which is what makes it the
// right collider for this game's station hulls, with AxisZ so the disc lies in the body's XY
// plane the way the models do.
//
// Only sphere/cylinder is implemented, so a cylinder is for static scenery that ships and
// rocks bounce off. Prefer a capsule where the flat caps do not matter: its contact is
// smooth everywhere, which is kinder to the solver.
func (w *World) SpawnCylinder(mass, radius, halfHeight float32, axis Axis, pos Vec3) EntityID {
	return EntityID(C.ag_spawn_cylinder(w.w,
		C.float(mass), C.float(radius), C.float(halfHeight), C.int(axis),
		C.float(pos.X), C.float(pos.Y), C.float(pos.Z)))
}

// SpawnPlane adds an infinite static half-space. normal need not be unit length;
// distance offsets it from the origin along that normal, and the solid side is the one the
// normal points away from.
//
// Useful as an arena wall — a hard boundary that cannot be flown around, unlike a large
// sphere.
func (w *World) SpawnPlane(normal Vec3, distance float32) EntityID {
	return EntityID(C.ag_spawn_plane(w.w,
		C.float(normal.X), C.float(normal.Y), C.float(normal.Z), C.float(distance)))
}

// SetRotation orients a body. Cold path.
//
// Spheres do not care, which is why nothing called this before non-sphere colliders
// existed. Boxes, capsules and cylinders all do.
func (w *World) SetRotation(e EntityID, q Quat) {
	C.ag_set_rotation(w.w, C.uint64_t(e),
		C.float(q.X), C.float(q.Y), C.float(q.Z), C.float(q.W))
}

// Despawn removes an entity.
func (w *World) Despawn(e EntityID) {
	C.ag_despawn(w.w, C.uint64_t(e))
}

// Count returns the number of live bodies.
func (w *World) Count() int {
	return int(C.ag_body_count(w.w))
}

// ApplyForces queues forces and torques for the coming Step. One cgo call regardless
// of how many commands are supplied.
func (w *World) ApplyForces(cmds []ForceCmd) {
	if len(cmds) == 0 {
		return
	}

	if cap(w.forceBuf) < len(cmds) {
		w.forceBuf = make([]C.ag_force_cmd, len(cmds))
	}
	w.forceBuf = w.forceBuf[:len(cmds)]

	for i, c := range cmds {
		w.forceBuf[i] = C.ag_force_cmd{
			entity: C.uint64_t(c.Entity),
			fx:     C.float(c.Force.X), fy: C.float(c.Force.Y), fz: C.float(c.Force.Z),
			tx: C.float(c.Torque.X), ty: C.float(c.Torque.Y), tz: C.float(c.Torque.Z),
		}
	}

	C.ag_apply_forces(w.w,
		(*C.ag_force_cmd)(unsafe.Pointer(&w.forceBuf[0])),
		C.size_t(len(w.forceBuf)))
}

// Step advances the simulation one fixed timestep.
func (w *World) Step() {
	C.ag_step(w.w)
}

// Snapshot returns every body's state. The returned slice is reused between calls —
// copy it if you need to retain it past the next Snapshot.
func (w *World) Snapshot() []BodyState {
	n := int(C.ag_snapshot(w.w,
		(*C.ag_body_state)(unsafe.Pointer(&w.snapBuf[0])),
		C.size_t(len(w.snapBuf))))

	w.outBuf = w.outBuf[:0]
	for i := 0; i < n; i++ {
		s := &w.snapBuf[i]
		w.outBuf = append(w.outBuf, BodyState{
			Entity:   EntityID(s.entity),
			Pos:      Vec3{float32(s.px), float32(s.py), float32(s.pz)},
			Rot:      Quat{float32(s.qx), float32(s.qy), float32(s.qz), float32(s.qw)},
			Vel:      Vec3{float32(s.vx), float32(s.vy), float32(s.vz)},
			AngVel:   Vec3{float32(s.wx), float32(s.wy), float32(s.wz)},
			Mass:     float32(s.mass),
			Sleeping: s.sleeping != 0,
		})
	}
	return w.outBuf
}

// DrainCollisions returns contacts observed during the last Step and clears them.
// The returned slice is reused between calls.
func (w *World) DrainCollisions() []Collision {
	n := int(C.ag_drain_collisions(w.w,
		(*C.ag_collision)(unsafe.Pointer(&w.collBuf[0])),
		C.size_t(len(w.collBuf))))

	w.outColl = w.outColl[:0]
	for i := 0; i < n; i++ {
		c := &w.collBuf[i]
		w.outColl = append(w.outColl, Collision{
			A:        EntityID(c.entity_a),
			B:        EntityID(c.entity_b),
			RelSpeed: float32(c.rel_speed),
		})
	}
	return w.outColl
}

// GetBody reads one body. This is a linear scan inside lagrange — cold path only.
func (w *World) GetBody(e EntityID) (BodyState, bool) {
	var s C.ag_body_state
	if !bool(C.ag_get_body(w.w, C.uint64_t(e), &s)) {
		return BodyState{}, false
	}
	return BodyState{
		Entity:   EntityID(s.entity),
		Pos:      Vec3{float32(s.px), float32(s.py), float32(s.pz)},
		Rot:      Quat{float32(s.qx), float32(s.qy), float32(s.qz), float32(s.qw)},
		Vel:      Vec3{float32(s.vx), float32(s.vy), float32(s.vz)},
		AngVel:   Vec3{float32(s.wx), float32(s.wy), float32(s.wz)},
		Mass:     float32(s.mass),
		Sleeping: s.sleeping != 0,
	}, true
}

// SetPosition teleports a body. Cold path.
func (w *World) SetPosition(e EntityID, p Vec3) {
	C.ag_set_position(w.w, C.uint64_t(e), C.float(p.X), C.float(p.Y), C.float(p.Z))
}

// SetVelocity overrides a body's linear velocity and wakes it. Cold path.
func (w *World) SetVelocity(e EntityID, v Vec3) {
	C.ag_set_velocity(w.w, C.uint64_t(e), C.float(v.X), C.float(v.Y), C.float(v.Z))
}

// SetDamping overrides per-body damping. Ships want more than asteroids so they stop
// when the player releases thrust.
func (w *World) SetDamping(e EntityID, linear, angular float32) {
	C.ag_set_damping(w.w, C.uint64_t(e), C.float(linear), C.float(angular))
}

// SetSleepAllowed controls whether a body may sleep. Ships and held objects must not.
func (w *World) SetSleepAllowed(e EntityID, allowed bool) {
	C.ag_set_sleep_allowed(w.w, C.uint64_t(e), C.bool(allowed))
}

// SetMaterial sets bounciness (0..1) and friction. Ships and rocks want real bounce so
// collisions read as impacts rather than mush.
func (w *World) SetMaterial(e EntityID, restitution, friction float32) {
	C.ag_set_material(w.w, C.uint64_t(e), C.float(restitution), C.float(friction))
}

// LastStepMs is the wall-clock duration of the most recent Step, for DebugStats.
func (w *World) LastStepMs() float64 {
	return float64(C.ag_last_step_ms(w.w))
}
