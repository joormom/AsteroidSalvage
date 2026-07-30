/*
 * registry.h — extern "C" surface over an EnTT registry.
 *
 * STATUS: SPIKE. This is not the migration; it is the evidence the migration is
 * buildable. See server/core/MIGRATION.md for the plan this exists to de-risk, and do not
 * grow this file into the real bridge — the real one is staged, and stage 1 replaces it.
 *
 * What it is here to prove, in order of how badly each would sink the plan:
 *
 *   1. server.exe stays standalone. Adding C++ to a cgo build normally pulls in
 *      libstdc++-6.dll, libgcc_s_seh-1.dll and libwinpthread-1.dll, none of which ship
 *      with Windows and none of which build_dist.py copies. See the LDFLAGS in core.go.
 *   2. A C++ exception cannot reach Go. Go has no idea what unwinding is; one escaping
 *      std::bad_alloc past the boundary is an unrecoverable crash with no stack.
 *   3. cgo cannot see templates, so EnTT's views have to be flattened to a bulk
 *      POD copy — the same shape physics/bridge.h already settled on for lagrange.
 *      This measures what that costs.
 *
 * The component set below is deliberately fake and minimal. Its only job is to be shaped
 * like the real thing: POD, no pointers, bulk in and bulk out.
 */

#ifndef ASCORE_REGISTRY_H
#define ASCORE_REGISTRY_H

#include <stdint.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct ascore_world ascore_world;

/* One entity's components, flattened. The real bridge's equivalent is ag_body_state. */
typedef struct {
    uint32_t entity;
    float px, py, pz;
    float health;
} ascore_body;

/* Returns NULL on allocation failure rather than throwing. */
ascore_world* ascore_create(void);
void          ascore_destroy(ascore_world* w);

/* THE INVALID-ENTITY SENTINELS OF THE TWO LIBRARIES ARE OPPOSITE. READ THIS.
 *
 *   lagrange:  LG_ENTITY_INVALID == 0.        0 means "nothing".
 *   EnTT:      the first entity created IS 0. entt::null is 0xFFFFFFFF.
 *
 * The Go server is built on the first convention throughout — `Held physics.EntityID //
 * 0 when empty-handed`, `if s.hostID == 0`, `if e == 0 { return }` — so a migration that
 * carries "zero means nothing" into EnTT would treat the very first entity the registry
 * ever hands out as absent. The bug is silent, it lands on whichever entity happens to be
 * created first, and it looks like a game rule misfiring rather than a type confusion.
 *
 * So this API does not invent its own sentinel. It reports EnTT's, and the Go side asserts
 * the two agree at test time. Stage 1 of the migration has to pick one convention for the
 * whole server and convert at the boundary; see MIGRATION.md. */
uint32_t ascore_null_entity(void);

/* Returns ascore_null_entity() on failure. */
uint32_t ascore_spawn(ascore_world* w, float px, float py, float pz, float health);
void     ascore_kill(ascore_world* w, uint32_t entity);
size_t   ascore_count(const ascore_world* w);

/* Bulk read of every entity carrying both components. Returns the number written.
 * This is the call that stands in for a C++ view, which cgo cannot iterate. */
size_t ascore_snapshot(const ascore_world* w, ascore_body* out, size_t max);

/* Bulk write: n parallel entries. Unknown entities are skipped, not an error. */
void ascore_apply_damage(ascore_world* w, const uint32_t* entities,
                         const float* amounts, size_t n);

/* Deliberately throws inside C++ and catches it at the boundary.
 * Returns 0 if the throw was contained, -1 if it was a std::exception, -2 if unknown.
 * A crash instead of a return value means the boundary is not exception-tight. */
int ascore_provoke_throw(ascore_world* w);

#ifdef __cplusplus
}
#endif

#endif /* ASCORE_REGISTRY_H */
