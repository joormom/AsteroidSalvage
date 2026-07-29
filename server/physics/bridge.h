/*
 * bridge.h — exported C API over lagrange for cgo.
 *
 * WHY THIS FILE EXISTS
 * --------------------
 * Every function in lagrange is `static inline` (see lagrange.h: "All implementations
 * are in the header files above as static inline functions"). cgo cannot reliably call
 * static inline functions, so this bridge exposes real, externally-linked wrappers.
 *
 * It is also a performance boundary. lagrange's `lg_storage_find` is a linear scan, so
 * every `lg_get_position` / `lg_apply_force` is O(n). This API is therefore *bulk*:
 * one cgo call moves the whole world, rather than one call per entity per tick.
 *
 * Game state (kind, value, integrity, owner) deliberately lives in Go, not here. This
 * side stays pure physics.
 */

#ifndef AG_BRIDGE_H
#define AG_BRIDGE_H

#include <stdint.h>
#include <stddef.h>
#include <stdbool.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct ag_world ag_world;

/* Body kinds are opaque to the bridge; Go assigns meaning. Stored here only so the
 * snapshot can carry it back without a second lookup. */
typedef struct {
    uint64_t entity;
    float px, py, pz;       /* position */
    float qx, qy, qz, qw;   /* rotation quaternion */
    float vx, vy, vz;       /* linear velocity */
    float wx, wy, wz;       /* angular velocity */
    float mass;
    uint8_t sleeping;
    uint8_t _pad[3];
} ag_body_state;

typedef struct {
    uint64_t entity;
    float fx, fy, fz;       /* force,  N */
    float tx, ty, tz;       /* torque, N·m */
} ag_force_cmd;

/* Recorded pre-resolution, so rel_speed is the true impact speed.
 * lagrange fires its collision callback before contact resolution (sim.h:767). */
typedef struct {
    uint64_t entity_a;
    uint64_t entity_b;
    float rel_speed;        /* |v_a - v_b| at moment of contact, m/s */
    float _pad;
} ag_collision;

/* --- lifecycle --- */

/* time_step is the fixed sim step (1/30 for this game). max_entities sizes the
 * preallocated storage; lagrange never grows it. */
ag_world* ag_world_create(float time_step, size_t max_entities);
void      ag_world_free(ag_world* w);

/* --- entities --- */

/* Returns 0 (LG_ENTITY_INVALID) if storage is full. mass <= 0 creates a static body.
 * Inertia is derived from the collider so torque behaves correctly. */
uint64_t ag_spawn_sphere(ag_world* w, float mass, float radius,
                         float px, float py, float pz);
uint64_t ag_spawn_box(ag_world* w, float mass, float hx, float hy, float hz,
                      float px, float py, float pz);
void     ag_despawn(ag_world* w, uint64_t entity);
size_t   ag_body_count(const ag_world* w);

/* --- per-tick bulk operations (the hot path) --- */

/* Queue forces/torques for this tick. Applied to the underlying bodies immediately;
 * lagrange clears accumulated forces at the end of each step.
 * Multiple commands naming the same entity accumulate. */
void ag_apply_forces(ag_world* w, const ag_force_cmd* cmds, size_t n);

/* Advance one fixed step. Collisions observed during the step are buffered. */
void ag_step(ag_world* w);

/* Fill `out` with up to `max` body states. Returns the number written.
 * Iterates storage directly — no per-entity lookup. */
size_t ag_snapshot(ag_world* w, ag_body_state* out, size_t max);

/* Drain collisions buffered during the last ag_step. Returns number written. */
size_t ag_drain_collisions(ag_world* w, ag_collision* out, size_t max);

/* --- individual access (cold path: spawning, teleports, tests) --- */

bool ag_get_body(ag_world* w, uint64_t entity, ag_body_state* out);
void ag_set_position(ag_world* w, uint64_t entity, float x, float y, float z);
void ag_set_velocity(ag_world* w, uint64_t entity, float x, float y, float z);
void ag_set_damping(ag_world* w, uint64_t entity, float linear, float angular);
void ag_set_sleep_allowed(ag_world* w, uint64_t entity, bool allowed);

/* Bounciness (0 = dead stop, 1 = perfectly elastic) and surface friction.
 * lagrange defaults to 0.3 restitution, which reads as a mushy shove rather than a
 * bounce when a ship hits a rock. */
void ag_set_material(ag_world* w, uint64_t entity, float restitution, float friction);

/* --- diagnostics --- */

/* Wall-clock milliseconds spent inside the most recent ag_step. */
double ag_last_step_ms(const ag_world* w);

#ifdef __cplusplus
}
#endif

#endif /* AG_BRIDGE_H */
