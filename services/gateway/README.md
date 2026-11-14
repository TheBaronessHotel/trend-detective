# gateway

Go. Orchestrates jobs: it runs preflight checks, writes job and task state to Redis, and enqueues each stage on NATS when the previous one reports `StageDone`. It is the only writer of job state. It also owns `catalog.yaml` and the Connect API the browser talks to.

See the Services and Job lifecycle sections of [ARCHITECTURE.md](../../ARCHITECTURE.md).
