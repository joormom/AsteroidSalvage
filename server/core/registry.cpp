// SPIKE — see registry.h.

#include "registry.h"

#include <entt/entt.hpp>

#include <cstring>
#include <new>
#include <stdexcept>

namespace {

struct Transform {
    float x, y, z;
};

struct Health {
    float value;
};

} // namespace

// The registry is wrapped rather than exposed because entt::registry is a C++ type with a
// non-trivial destructor; the Go side may only ever hold the opaque pointer.
struct ascore_world {
    entt::registry reg;
};

// Every entry point is noexcept and wraps its body, because an exception crossing into Go
// is not a recoverable error — Go's runtime does not participate in unwinding, so it is a
// process kill with no usable stack. There is exactly one way for that not to happen and
// it is doing it at every single door.
#define ASCORE_GUARD(body)                     \
    try {                                      \
        body                                   \
    } catch (...) {                            \
    }

extern "C" {

ascore_world* ascore_create(void) {
    // nothrow rather than a guard: this is the one place where failing to allocate is the
    // expected error rather than a bug, and NULL is already its return channel.
    return new (std::nothrow) ascore_world();
}

void ascore_destroy(ascore_world* w) {
    delete w;
}

uint32_t ascore_null_entity(void) {
    // Asked of EnTT rather than hardcoded, so that if its representation ever changes the
    // Go constant fails a test instead of the game misplacing an entity.
    return static_cast<uint32_t>(entt::entity{entt::null});
}

uint32_t ascore_spawn(ascore_world* w, float px, float py, float pz, float health) {
    if (w == nullptr) {
        return ascore_null_entity();
    }
    ASCORE_GUARD({
        const auto e = w->reg.create();
        w->reg.emplace<Transform>(e, px, py, pz);
        w->reg.emplace<Health>(e, health);
        return static_cast<uint32_t>(e);
    })
    return ascore_null_entity();
}

void ascore_kill(ascore_world* w, uint32_t entity) {
    if (w == nullptr) {
        return;
    }
    ASCORE_GUARD({
        const auto e = static_cast<entt::entity>(entity);
        if (w->reg.valid(e)) {
            w->reg.destroy(e);
        }
    })
}

size_t ascore_count(const ascore_world* w) {
    if (w == nullptr) {
        return 0;
    }
    // storage<Transform>() is const-correct and cheaper than building a view just to size
    // it. Every spawned entity carries a Transform, so this is the live count.
    if (const auto* s = w->reg.storage<Transform>()) {
        return s->size();
    }
    return 0;
}

size_t ascore_snapshot(const ascore_world* w, ascore_body* out, size_t max) {
    if (w == nullptr || out == nullptr || max == 0) {
        return 0;
    }
    size_t n = 0;
    ASCORE_GUARD({
        for (const auto [e, t, h] : w->reg.view<const Transform, const Health>().each()) {
            if (n == max) {
                break; // Caller sized the buffer; silently truncating is its problem to size.
            }
            out[n].entity = static_cast<uint32_t>(e);
            out[n].px = t.x;
            out[n].py = t.y;
            out[n].pz = t.z;
            out[n].health = h.value;
            ++n;
        }
    })
    return n;
}

void ascore_apply_damage(ascore_world* w, const uint32_t* entities,
                         const float* amounts, size_t n) {
    if (w == nullptr || entities == nullptr || amounts == nullptr) {
        return;
    }
    ASCORE_GUARD({
        for (size_t i = 0; i < n; ++i) {
            const auto e = static_cast<entt::entity>(entities[i]);
            // An entity killed earlier this tick is ordinary, not exceptional.
            if (auto* h = w->reg.try_get<Health>(e)) {
                h->value -= amounts[i];
            }
        }
    })
}

int ascore_provoke_throw(ascore_world* w) {
    if (w == nullptr) {
        return -2;
    }
    try {
        throw std::runtime_error("spike: deliberate throw");
    } catch (const std::exception&) {
        return -1;
    } catch (...) {
        return -2;
    }
}

} // extern "C"
