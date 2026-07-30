# Moving the simulation core to C++

**Status: proposed. Nothing in `server/sim` has moved. Needs sign-off before stage 1.**

The idea is to let the three vendored libraries live in one language instead of three
bridges: `lagrange` (bodies), `art_of_flight` (control) and `entt` (entities and
components) become a single C++20 simulation core, with Go keeping the netcode, the
tournament rules and the protocol.

`server/core` is currently a **spike**, not that core. It exists to prove the boundary is
buildable and to find the traps early. It is deleted at the start of stage 1.

---

## What the spike established

Measured on this machine (GCC 16.1, MinGW-W64 x86_64-ucrt-posix-seh, EnTT v4.0.0):

| Question | Answer |
|---|---|
| Does cgo compile and call EnTT? | Yes. `entt::registry`, `emplace`, `view<...>().each()` all work behind `extern "C"`. |
| Which C++ standard? | **C++20.** EnTT v4 dropped C++17 — it uses `std::bit_ceil`, `std::popcount` and concepts. Only v3.x is C++17. |
| Does `server.exe` stay standalone? | Yes, but only with the right link flags — see below. Verified: `KERNEL32.dll` plus Windows' own `api-ms-win-crt-*` stubs, nothing else. |
| Can a C++ exception reach Go? | Not if every entry point guards. Go does not participate in unwinding, so an escaping exception is a process kill with no usable stack. Guard pattern is in `registry.cpp`. |
| Cost of the boundary shape | cgo cannot see templates, so a view becomes a flat POD array and one crossing per tick — the same shape `physics/bridge.h` already uses for lagrange. |
| Build cost | ~12 s cold for the one C++ translation unit. EnTT is template-heavy; keep the TU count low. |
| Size cost | ~1.3 MB for EnTT plus a statically linked libstdc++. `vendor/entt` is 3.3 MB on disk. |

### The link flags, and why they are not optional

A cgo build containing any C++ links by default against `libstdc++-6.dll`,
`libgcc_s_seh-1.dll` and `libwinpthread-1.dll`. Windows ships none of them and
`build_dist.py` copies none of them, so the binary starts on the machine that built it and
fails on a player's. The third is the awkward one: this toolchain's libstdc++ is the
*posix* threading build, so linking it statically promotes `libwinpthread` from its
dependency to ours, and neither `-static-libstdc++` nor `-Wl,-Bstatic` is enough — gcc
appends libstdc++ *after* our flags and the linker re-resolves pthread against the import
library. Its members have to be pulled in unconditionally instead.

cgo's LDFLAGS allowlist rejects `--whole-archive`, so this is a build prerequisite:

```
CGO_LDFLAGS_ALLOW='-Wl,--(no-)?whole-archive'
```

`build_dist.py` sets it. For local work, `go env -w CGO_LDFLAGS_ALLOW='-Wl,--(no-)?whole-archive'`.
Forgetting it fails the build loudly rather than producing an unshippable binary.

`TestBinaryStaysStandalone` enforces all of this, and has teeth — removing any flag fails
it with the offending DLL named. Note it deliberately inspects **this package's own test
binary**, not `go build .` on the server: nothing in `main` imports `core` yet, so building
the server would compile no C++ and the check would pass while proving nothing. Switch it
to the server binary once stage 1 lands.

### The trap worth knowing before anything moves

**lagrange and EnTT have opposite invalid-entity conventions.**

- lagrange: `LG_ENTITY_INVALID == 0`. Zero means nothing.
- EnTT: the first entity a registry creates **is** `0`. Its null is `0xFFFFFFFF`.

The Go server assumes lagrange's convention throughout — `Held physics.EntityID // 0 when
empty-handed`, `if s.hostID == 0`, early-outs on `e == 0`. Carrying that into EnTT unchanged
would make the first entity ever created read as absent: silent, landing on whichever
entity happens to be created first, and looking like a game rule misfiring rather than a
type confusion. `TestNullEntityMatchesEnTT` and `TestZeroIsAValidEntity` pin both halves.

**Proposed resolution:** keep `0 == invalid` for the whole server, because that is what
every existing line of Go already means, and make the C++ side burn entity 0 at registry
creation — create one entity, immediately destroy it. EnTT encodes a version in the handle's
high bits, so once destroyed, no recycled handle can ever be numerically zero again. One
line, no lookup table, and it makes Go's assumption true instead of merely hoped for. It
needs a test asserting no live handle is ever 0, because it depends on EnTT's bit layout.

The alternative — a monotonic external `uint64` ID with a map both ways — is more explicit
and survives a registry reset, at the cost of a hash lookup per crossing. Worth reaching for
only if stable-across-reset IDs are ever needed.

---

## Staging

**Vertical slices, not horizontal layers.** The tempting first step is "move the components
into EnTT and leave the rules in Go", and it is a trap: every rule would then cross the
boundary for every field access, which is strictly worse than today until the very last
stage lands. Each stage below instead moves one subsystem's **data and its rules together**,
so the boundary count goes down at every step and the tree is shippable throughout.

The Go-side API shape (`AddPlayer`, `SetInput`, `Step`, `GetBody`) does not change in any
stage. That is deliberate: `server/sim`'s test suite is the main asset being risked here, so
each stage migrates the implementation underneath tests that stay put. Any stage that wants
to change the test surface is a stage that has gone wrong.

### Stage 1 — Foundation

Fold `server/physics` and `server/flight` into `server/core`: one registry, whose entities
carry a lagrange body and (for ships) an art_of_flight rate controller as components. Delete
the spike.

This stage writes almost no new logic — both bridges already exist and already batch, so it
is mostly a move plus the entity-sentinel decision above. `core.World` replaces
`physics.World`; `sim` keeps every rule and reads one snapshot per tick, as it does now.

Done when: `go test ./...` passes untouched, `TestBinaryStaysStandalone` builds the real
server, and the tick's cgo crossing count is no higher than today.

### Stage 2 — Projectiles

Bolts and missiles: spawn, integrate, expire, hit-test, missile steering. The most
self-contained subsystem, the highest entity count, no tournament state, and the one place
ECS iteration actually pays for itself. Covers `projectiles.go`, the bolt half of
`weapons.go`, and `steerMissile` in `items.go`.

This is the stage that tests the premise. Measure tick time before and after.

### Stage 3 — Asteroids and salvage

Tiers, integrity, impact damage, shatter-into-fragments. Moves the collision→damage pass
next to the contacts that produce it, which is where the per-contact work already is.

### Stage 4 — Grab constraint

The spring-damper wants the ship and the held body in the same frame at the same instant;
it is currently three lookups and a reaction force applied back across the boundary.

### Stage 5 — Stop here, deliberately

Match flow, lobby, upgrades, shop, credits, stats, chat, KOTH and hoard scoring **stay in
Go, permanently.** They are state machines and rules, not per-entity hot loops: they gain
nothing from an ECS, they are the most-tested code in the repo, and they talk directly to
netcode. Migrating them would be motion without benefit, and it is how a migration like this
ends up with no end.

The goal is a C++ core for the per-entity simulation, not a C++ server.

---

## Risks

- **Build time.** ~12 s per C++ TU. Keep `core` to one or two translation units; revisit with
  a precompiled header if it grows.
- **Debuggability gets worse.** A C++ crash gives a far poorer stack than a Go panic.
  Mitigation: keep the bridge thin and assert hard at the boundary.
- **No Go pointers in C memory.** Every component must be POD. `*Object` and `*Player` carry
  maps and slices today and cannot be stored in a registry as they stand.
- **Exception discipline** has to hold at every entry point, forever, not just at first.
- **The env var** must be present in every build path — packaging, CI, and each developer's
  shell.

## Is it worth it?

Stated plainly, because the plan should carry its own counter-argument.

**For:** one language for the simulation, all three libraries composing directly instead of
through three separate bulk bridges, less marshalling, and an ECS in the two or three places
that genuinely iterate a lot of entities.

**Against:** the Go simulation works and is well covered by tests. Entity counts are in the
hundreds, and the existing bridges already amortise cgo by moving the whole world per call —
so the performance case is weak at this game's current scale. The real argument is
architectural coherence, which is a judgment call rather than a measurement.

**Recommendation:** commit to stages 1 and 2 only. Stage 2 is where the premise becomes
measurable — take a tick-time measurement there and decide stages 3 and 4 on the number,
not on the plan.
