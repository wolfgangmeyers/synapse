# SQLite connection lifecycle safety

## User scenarios

- A user terminates an embedded agent after it has conversed. Once termination completes, its session is cleaned up and the same name can be launched with a fresh session, including when the database connection was replaced during the router's lifetime.
- A replacement database connection retains foreign-key enforcement, full synchronous durability, and the configured busy timeout. It must not silently accept orphaned child records.
- An existing orphan session without independently verified termination remains untouched and blocks relaunch; the router must not erase user evidence merely to free a name.

## Verification

The embedded relaunch Playwright suite exercises the real router, proxy, browser, SQLite, and deterministic Claude fixture. A store regression forces a connection replacement and verifies its settings and cascade behavior.
