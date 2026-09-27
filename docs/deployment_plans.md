Create a clean baseline commit from the large current working-tree revamp.
Rename active previous-owner identifiers.
Run a full-history secret scan and dependency/license review.
Strengthen CI:- build
- unit tests
- PostGIS and Kafka integration tests
- race detector
- vet/staticcheck/govulncheck
- Docker builds

Add Google Cloud infrastructure and Workload Identity Federation.
Make the services Cloud Run-compatible.
Deploy immutable commit-SHA images to staging.
Run migrations, smoke tests, health checks, and WebSocket/gRPC checks.
Promote the same image digests to production with approval and rollback support.