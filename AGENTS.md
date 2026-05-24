# AGENTS.md

This file provides system context, guidelines, and commands for AI coding assistants working on the **DbConfigSync Operator** repository.

---

## Project Context & Architecture

DbConfigSync Operator fetches configurations from relational and non-relational databases and syncs them to Kubernetes ConfigMaps/Secrets or local `.env` files.

### Key Components

- **`main.go`**: Command-line entry point. Directs execution to Operator mode, Local Dry-Run mode, or simulated service mode.
- **`pkg/dbclient/dbclient.go`**: Dynamic database client connector. Supports PostgreSQL, MySQL, Redis, and MongoDB.
- **`pkg/controller/reconciler.go`**: The Kubernetes operator reconciler. Handles periodic loops, fetches database configs, applies key mappings, executes transformations, updates ConfigMaps/Secrets, and handles custom resource status updates.
- **`pkg/controller/local.go`**: Local simulator reconciler. Simulates Kubernetes operator logic on the local OS by modifying `.env` files and spawning/killing processes sequentially to simulate rolling updates.
- **`pkg/controller/transforms.go`**: Implements template-based variable formatting and list/pattern-based key joins.
- **`pkg/dashboard/server.go`**: Hosts the web control console (dashboard) and streams live logging events via Server-Sent Events (SSE).

---

## Coding Standards & Critical Guidelines

- **Go Version**: Keep compatibility with Go 1.20+.
- **Kubernetes client-go Status Panic**: When updating custom resource status fields through the dynamic client (`DynamicClient.Resource(...).UpdateStatus`), Kubernetes internal converters will panic if they encounter standard Go `int` values or typed maps like `map[string]string`. Always explicitly cast integers to `int64` and maps to `map[string]interface{}`.
- **Key Normalization**: The database scanner upper-cases scanned column names by default (e.g. `installation_id` becomes `INSTALLATION_ID`). Explicit `keyMapping` definitions in the CRD spec should match the upper-cased key names.
- **Reflector Annotations**: The prefix for Emberstack Reflector must be `reflector.v1.k8s.emberstack.com/` (with `.v1.k8s.`). The older version `reflector.emberstack.com/` is deprecated and will not trigger replication.
- **Helm Template Spacing**: Avoid using trailing hyphens (`-}}`) next to the document separator (`---`) in YAML templates (e.g., RBAC), as it strips newlines and breaks manifest parsing.

---

## Common Development Commands

### Compilation and Local Run
- Run the operator locally (dry-run mode):
  ```bash
  go run main.go -mode dry-run -config sync-config.json -env .env
  ```
- Build static Linux binary (via WSL on Windows):
  ```bash
  wsl CGO_ENABLED=0 GOOS=linux go build -ldflags='-w -s' -o envsync main.go
  ```

### Kubernetes cluster testing (Kind)
- Build operator image and run full bootstrap integration tests:
  ```powershell
  # Windows PowerShell
  .\scripts\test-kind.ps1
  ```
  ```bash
  # WSL / Linux Bash
  ./scripts/test-kind.sh
  ```

### Helm Chart Operations
- Lint the Helm chart structure:
  ```bash
  helm lint charts/dbconfigsync-operator
  ```

---

## Project Structure Reference

```
operator/
├── .github/
│   └── workflows/
│       └── ci.yaml           # GitHub Actions CI workflow
├── charts/
│   └── dbconfigsync-operator/ # Helm Chart files (Chart.yaml, values.yaml)
├── deploy/
│   ├── crds/                 # Custom Resource Definitions
│   ├── deployment.yaml       # Operator Deployment manifest
│   ├── rbac.yaml             # Operator Role and bindings
│   ├── sample-cr.yaml        # Sample DbConfigSync Custom Resource
│   ├── test-apps.yaml        # Simulates consumer workloads
│   └── test-databases.yaml   # Databases deployed for Kind testing
├── pkg/
│   ├── controller/           # Reconcilers, transforms, and types
│   ├── dashboard/            # SSE logging broker and web console server
│   └── dbclient/             # Database connection and querying engines
├── scripts/                  # Cluster bootstrapping scripts
├── LICENSE                   # BSD 3-Clause License
├── README.md                 # Project User Guide
└── main.go                   # Application entrypoint
```
