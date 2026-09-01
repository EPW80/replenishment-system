# Architecture decision records

One file per decision, numbered in the order taken. A record is written once and then
amended only by a later record that supersedes it — the point is the trail, not the
current state.

Format: Context (what forced a decision), Decision, Consequences (including what this
costs), and Status.

| ADR | Decision | Status |
| --- | --- | --- |
| [0001](0001-go-service-not-woocommerce-subscriptions.md) | Build a Go service rather than configure WooCommerce Subscriptions | Accepted |
| [0002](0002-river-for-durable-queue.md) | Use `river` for the durable queue, behind a `Queue` interface | Accepted, provisional |
| [0003](0003-goose-migrations-sqlc-pgx.md) | `goose` for migrations, `sqlc` + `pgx` for data access | Accepted, provisional |
| [0004](0004-anchor-relative-date-math.md) | Compute `next_run_date` anchor-relative, never incrementally | Accepted |
| [0005](0005-compliance-boundary-enforcement.md) | Enforce the §2 compliance boundary with a build-failing guard | Accepted |
| [0006](0006-read-models-scoped-to-available-data.md) | Read models report counts, not revenue or acquisition source — the schema doesn't have that data yet | Accepted |
| [0007](0007-postmark-http-client-and-outbox-dispatch.md) | Postmark over hand-rolled `net/http`; an outbox dispatcher off `schedule_events`, not synchronous sends inside transitions | Accepted |

**"Provisional"** marks a decision taken without access to PartnerOS. The spec (§4)
directs reuse of whatever PartnerOS settled on rather than introducing a second
library. That codebase was not reachable when these were written, so each provisional
choice sits behind a narrow interface and is expected to be revisited against
PartnerOS's actual stack.
