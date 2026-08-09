# Durable goal execution (operators)

Buckley can drive goals through a durable workflow backend. A worker
process is disposable: workflow history, the run ledger, and evidence
survive a crash, and a restarted worker resumes without repeating a
completed model call or tool effect. The design is canonical in the
Hyphae space (`hypha recall "durable execution"`); this page is the
operator surface.

## Run a goal durably

```bash
# One-process mode: worker and scheduler in one command.
buckley goal run --durable-backend dapr <run-id>

# Bounded fan-out of claim-independent tasks.
buckley goal run --durable-backend dapr --max-parallel 4 <run-id>

# Hold parked tasks for a durable approval instead of stopping.
buckley goal run --durable-backend dapr --approval-wait 8h <run-id>
```

Set `execution.durable_backend: dapr` in the configuration to make it
the default. Local mode stays the default otherwise.

## Approvals

A parked task with `--approval-wait` set holds on a durable event.
Resolve it from any machine that reaches the same ledger and sidecar:

```bash
buckley goal approve <run-id>                 # approve; the task resumes
buckley goal approve --deny --reason "not yet" <run-id>
buckley goal approve --task task-002-... <run-id>
```

The wait and its resolution are ledger events; `buckley goal audit`
shows both.

## Standalone worker

Run the activity host as its own process. It serves any goal on the
ledger and resolves each run on first use:

```bash
buckley goal worker --endpoint localhost:50001
```

Interrupting the worker is safe. In-flight state is durable and the
next worker resumes it.

## Sidecar or emulator

The backend speaks the Dapr workflow gRPC protocol. Two ways to provide
it:

1. A Dapr sidecar (`dapr run`), for real deployments.
2. The durabletask emulator, for local work and CI. It needs no Dapr
   install:

```bash
go run github.com/dapr/durabletask-go --port 4501 --db /tmp/taskhub.db
BUCKLEY_DAPR_TEST_ENDPOINT=localhost:4501 go test ./pkg/durability/dapr/
```

Endpoint resolution order: `--endpoint` flag, then
`execution.dapr_grpc_endpoint`, then `DAPR_GRPC_ENDPOINT`, then
`DAPR_GRPC_PORT` on localhost, then `localhost:50001`.

## Distributed deployments

PostgreSQL is the supported workflow state store. Give the sidecar an
actor-enabled Postgres component:

```yaml
apiVersion: dapr.io/v1alpha1
kind: Component
metadata:
  name: workflowstate
spec:
  type: state.postgresql
  version: v1
  metadata:
    - name: connectionString
      secretKeyRef:
        name: workflow-postgres
        key: connectionString
    - name: actorStateStore
      value: "true"
```

Deploy `buckley goal worker` as its own Deployment with a Dapr sidecar
annotation. Keep one Dapr state database separate from Buckley's
`ledger.db`; the run ledger stays the audit source of truth.

## Evidence retention

Step evidence (model requests and responses, tool requests and
results) is pinned for the run's lifetime, so replay never loses its
inputs to a retention sweep. Pruning a run releases its pins; the next
sweep reclaims the space.

## Verify a run

```bash
buckley goal report <run-id>    # morning report
buckley goal audit <run-id>     # full decision and step trail
buckley goal replay <run-id>    # replay-readiness verification
```
