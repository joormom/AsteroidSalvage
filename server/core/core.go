// Package core is a SPIKE, not the simulation core it is named after.
//
// It exists to answer three questions about putting EnTT behind cgo before any of package
// sim moves, because all three are cheaper to answer now than to discover halfway through
// a migration. See MIGRATION.md for the plan, and registry.h for what each question is.
//
// Delete this package at the start of stage 1. Nothing outside its own test should import
// it, and nothing does.
package core

/*
#cgo CXXFLAGS: -std=c++20 -O2 -I${SRCDIR}/../../vendor/entt/single_include

// These flags are the whole reason this spike was worth running.
//
// A cgo build containing any C++ links by default against libstdc++-6.dll,
// libgcc_s_seh-1.dll and libwinpthread-1.dll — MinGW DLLs that Windows does not ship and
// build_dist.py does not copy. server.exe would run on the machine that built it and fail
// to start on a player's, with no error a player could act on.
//
// All three have to be dealt with, and the third is the one that surprises. This toolchain
// is x86_64-ucrt-**posix**-seh, so its libstdc++ implements std::thread and std::mutex on
// winpthreads; linking libstdc++ statically promotes libwinpthread from libstdc++'s
// dependency to ours, and -static-libstdc++ alone still leaves the binary importing it.
// Nor is -Wl,-Bstatic enough, because gcc appends libstdc++ *after* these flags and the
// linker re-resolves pthread against the import library once -Bdynamic is restored. The
// archive's members have to be pulled in unconditionally, ahead of that.
//
// REQUIRES an environment variable. cgo's LDFLAGS allowlist rejects --whole-archive, so
// every build and test of this package needs:
//
//	CGO_LDFLAGS_ALLOW='-Wl,--(no-)?whole-archive'
//
// build_dist.py sets it. For a local `go build` or `go test` set it in your shell, or once
// and for all with `go env -w CGO_LDFLAGS_ALLOW='-Wl,--(no-)?whole-archive'`. Forgetting it
// fails the build loudly with "invalid flag in #cgo LDFLAGS" — which is the right failure,
// since the alternative would be a quietly unshippable binary.
//
// TestBinaryStaysStandalone keeps all of this true rather than merely intended, and it has
// teeth: dropping any of these flags fails it with the offending DLL named.
#cgo LDFLAGS: -static-libstdc++ -static-libgcc
#cgo LDFLAGS: -Wl,--whole-archive -l:libwinpthread.a -Wl,--no-whole-archive

#include "registry.h"
*/
import "C"

import (
	"errors"
	"unsafe"
)

// NullEntity is EnTT's "no entity" value.
//
// It is 0xFFFFFFFF, and the first entity a registry hands out is 0 — the opposite of
// lagrange, where 0 is the invalid one and which the whole Go server currently assumes.
// TestNullEntityMatchesEnTT asserts this constant against what EnTT actually reports, and
// registry.h explains why getting it wrong would be so quiet.
const NullEntity uint32 = 0xFFFFFFFF

// Body is one entity's flattened components.
type Body struct {
	Entity  uint32
	X, Y, Z float32
	Health  float32
}

// World owns an EnTT registry. Not safe for concurrent use.
type World struct {
	w *C.ascore_world

	// Reused across calls so a 30 Hz loop does not allocate, exactly as physics.World
	// does. The spike keeps this habit because measuring the boundary without it would
	// measure Go's allocator instead.
	snapBuf []C.ascore_body
	out     []Body

	entBuf []C.uint32_t
	amtBuf []C.float
}

// NewWorld creates a registry sized for maxEntities snapshot slots.
func NewWorld(maxEntities int) (*World, error) {
	if maxEntities <= 0 {
		return nil, errors.New("core: maxEntities must be positive")
	}
	w := C.ascore_create()
	if w == nil {
		return nil, errors.New("core: ascore_create failed (out of memory)")
	}
	return &World{
		w:       w,
		snapBuf: make([]C.ascore_body, maxEntities),
		out:     make([]Body, 0, maxEntities),
	}, nil
}

// Close releases the registry. Safe to call more than once.
func (w *World) Close() {
	if w.w != nil {
		C.ascore_destroy(w.w)
		w.w = nil
	}
}

// Spawn adds an entity. Returns NullEntity on failure — not 0, which is a perfectly good
// entity here.
func (w *World) Spawn(x, y, z, health float32) uint32 {
	return uint32(C.ascore_spawn(w.w,
		C.float(x), C.float(y), C.float(z), C.float(health)))
}

// nullEntityFromCpp is what EnTT itself reports as its null value, for the test that pins
// NullEntity against it.
func nullEntityFromCpp() uint32 {
	return uint32(C.ascore_null_entity())
}

// Kill removes an entity. Unknown entities are ignored.
func (w *World) Kill(e uint32) {
	C.ascore_kill(w.w, C.uint32_t(e))
}

// Count returns the number of live entities.
func (w *World) Count() int {
	return int(C.ascore_count(w.w))
}

// Snapshot bulk-copies every entity out of the registry. The returned slice is reused
// between calls — copy it to retain it.
//
// This is the shape the boundary forces: cgo cannot iterate a C++ view, so a view becomes
// a flat array and one crossing per tick instead of one per entity.
func (w *World) Snapshot() []Body {
	if len(w.snapBuf) == 0 {
		return nil
	}
	n := int(C.ascore_snapshot(w.w,
		(*C.ascore_body)(unsafe.Pointer(&w.snapBuf[0])),
		C.size_t(len(w.snapBuf))))

	w.out = w.out[:0]
	for i := 0; i < n; i++ {
		b := &w.snapBuf[i]
		w.out = append(w.out, Body{
			Entity: uint32(b.entity),
			X:      float32(b.px),
			Y:      float32(b.py),
			Z:      float32(b.pz),
			Health: float32(b.health),
		})
	}
	return w.out
}

// Damage is one queued health change.
type Damage struct {
	Entity uint32
	Amount float32
}

// ApplyDamage writes a batch back into the registry in one crossing.
func (w *World) ApplyDamage(ds []Damage) {
	if len(ds) == 0 {
		return
	}
	if cap(w.entBuf) < len(ds) {
		w.entBuf = make([]C.uint32_t, len(ds))
		w.amtBuf = make([]C.float, len(ds))
	}
	w.entBuf, w.amtBuf = w.entBuf[:len(ds)], w.amtBuf[:len(ds)]
	for i, d := range ds {
		w.entBuf[i] = C.uint32_t(d.Entity)
		w.amtBuf[i] = C.float(d.Amount)
	}

	C.ascore_apply_damage(w.w,
		(*C.uint32_t)(unsafe.Pointer(&w.entBuf[0])),
		(*C.float)(unsafe.Pointer(&w.amtBuf[0])),
		C.size_t(len(ds)))
}

// ProvokeThrow asks the C++ side to throw and catch. It returns the boundary's verdict:
// -1 means a std::exception was contained, which is the passing answer. If an exception
// could escape, this call takes the process down instead of returning at all.
func (w *World) ProvokeThrow() int {
	return int(C.ascore_provoke_throw(w.w))
}
