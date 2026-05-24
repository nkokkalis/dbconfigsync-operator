# DbConfigSync Operator

[![License: BSD-3-Clause](https://img.shields.io/badge/License-BSD%203--Clause-blue.svg)](https://opensource.org/licenses/BSD-3-Clause)
[![Go Report Card](https://goreportcard.com/badge/github.com/nicolas/dbconfigsync-operator)](https://goreportcard.com/report/github.com/nicolas/dbconfigsync-operator)

DbConfigSync Operator is a production-ready, high-performance Kubernetes Custom Operator written in Go. It dynamically queries configuration variables from PostgreSQL, MySQL, Redis, and MongoDB, applies real-time value transformations, and synchronizes them directly into Kubernetes ConfigMaps or Secrets.

It integrates out-of-the-box with Stakater Reloader for auto-restarting client pods on configuration updates and Emberstack Reflector for automatic replication across multiple Kubernetes namespaces.

---

## Key Features

- **Multi-Database Support**: Connects concurrently to PostgreSQL, MySQL, Redis, and MongoDB.
- **Dynamic Column Mapping**: Scans arbitrary database tables natively. No strict key/value schema required.
- **Explicit Key Renaming (keyMapping)**: Map database column names to custom environment variable names.
- **Real-Time Transformations**:
  - **Go Templates**: Construct connection strings, endpoints, or composite keys (e.g. DATABASE_URL).
  - **Joins**: Merge explicit keys or filter keys matching a regex pattern (e.g., cluster endpoints).
- **Ecosystem Native**:
  - **Emberstack Reflector**: Annotates generated resources for multi-namespace mirroring.
  - **Stakater Reloader**: Automatically triggers rolling updates in dependent deployments.
- **Local Dry-Run Simulator**: Runs locally as a standalone OS process—generating .env files and managing simulated microservices—without requiring a Kubernetes cluster.

---

## Architecture

```
                    +------------------------+
                    |   Databases            |
                    | (Postgres / MySQL /    |
                    |  Redis / MongoDB)      |
                    +-----------+------------+
                                |
                                | Query / Fetch
                                v
                    +------------------------+
                    |  DbConfigSync Operator |
                    +-----------+------------+
                                |
                                | Reconcile / Transform
                                v
                    +------------------------+
                    | Kubernetes ConfigMap / |
                    |       Secret           |
                    +-----------+------------+
                                |
        +-----------------------+-----------------------+
        | (Emberstack Reflector)                        | (Stakater Reloader)
        v                                               v
+------------------+                            +------------------+
| App Namespace A  |                            | Pod Rolling      |
| App Namespace B  |                            | Restart Trigger  |
+------------------+                            +------------------+
```

---

## Getting Started

### Prerequisites

- Go 1.20+
- Kubernetes 1.20+ / kind / minikube
- Helm 3 (optional, for chart installation)

### Installation via Helm (Recommended)

1. Add the Helm repository (once published):
   ```bash
   helm repo add dbconfigsync https://nicolas.github.io/dbconfigsync-operator
   helm repo update
   ```

2. Install the operator using Helm:
   ```bash
   helm install dbconfigsync-operator dbconfigsync/dbconfigsync-operator \
     --namespace operator-system \
     --create-namespace
   ```

3. Default values.yaml parameters can be overridden during installation:
   ```bash
   helm install dbconfigsync-operator dbconfigsync/dbconfigsync-operator \
     --namespace operator-system \
     --set operator.watchNamespace="operator-system"
   ```

---

## CRD Specification & Examples

Define a DbConfigSync Custom Resource to orchestrate synchronization. Below are examples demonstrating standard and advanced table-mapping scenarios.

### 1. Arbitrary Database Table & Explicit Key Mapping

When querying tables not specifically structured for configuration storage, use the dynamic scanning capability and the keyMapping block to rename keys.

```yaml
apiVersion: config.operator.io/v1
kind: DbConfigSync
metadata:
  name: billing-config-sync
  namespace: operator-system
spec:
  targetConfigMap: "billing-shared-env"
  refreshInterval: 10

  databases:
    # Query postgres system_metadata table and map columns to ENV names
    - type: postgresql
      connectionUri: "postgres://postgres:postgres@postgres-service.databases.svc.cluster.local:5432/app_db?sslmode=disable"
      query: "SELECT installation_id, license_key, api_version FROM system_metadata LIMIT 1"
      keyMapping:
        INSTALLATION_ID: "BILLING_INSTALLATION_ID"
        LICENSE_KEY: "BILLING_LICENSE_KEY"
        API_VERSION: "BILLING_API_VERSION"
```

*Note: Database columns are converted to uppercase keys during scanning. The keyMapping block matches these uppercase column names to map them to your desired Environment Variable.*

### 2. Standard Key-Value Tables & Value Transformations

```yaml
apiVersion: config.operator.io/v1
kind: DbConfigSync
metadata:
  name: app-config-sync
  namespace: operator-system
spec:
  targetConfigMap: "app-shared-env"
  refreshInterval: 10
  
  # Namespace mirroring configuration
  reflection:
    allowed: true
    allowedNamespaces: "app-dev,app-prod"
    autoEnabled: true
    autoNamespaces: "app-dev,app-prod"

  databases:
    # 1. PostgreSQL KV Database Source
    - type: postgresql
      connectionUri: "postgres://postgres:postgres@postgres-service.databases.svc.cluster.local:5432/app_db?sslmode=disable"
      query: "SELECT key, val FROM app_settings WHERE is_active = 1"

    # 2. Redis Hash Database Source
    - type: redis
      connectionUri: "redis://redis-service.databases.svc.cluster.local:6379/0"
      query: "HGETALL settings:app"

  transforms:
    # Go Template transformation to build connection URI
    - name: "DATABASE_URL"
      type: "template"
      template: "postgres://{{.DB_USER}}:{{.DB_PASS}}@{{.DB_HOST}}:{{.DB_PORT}}/{{.DB_NAME}}?sslmode=disable"
      
    # Concat explicit keys
    - name: "CORS_ALLOWED_ORIGINS"
      type: "join"
      separator: ","
      sourceKeys:
        - "CORS_WEB"
        - "CORS_MOBILE"
        
    # Regex join (extracts keys matching REDIS_NODE_\d+ alphabetically sorted)
    - name: "REDIS_CLUSTER_NODES"
      type: "join"
      separator: ";"
      sourcePattern: "^REDIS_NODE_\\d+$"
```

---

## Value Transformations Spec

The operator supports two types of real-time configuration processors under transforms:

### Template (type: "template")
Interpolates Go-style template directives using the fetched database variables.
- **name**: The target environment variable name.
- **template**: The formatting template. All merged variables are accessible as fields (e.g. {{.VARIABLE_NAME}}).

### Join (type: "join")
Concatenates multiple database values into a single string.
- **separator**: String separator (e.g., ,, ;, or spaces).
- **sourceKeys**: Array of keys to fetch in a specific sequence.
- **sourcePattern**: Regular expression pattern. Matches all keys satisfying the pattern, sorts them alphabetically, and joins their values.

---

## Helm Chart Parameters

The following parameters are configurable in values.yaml or via the --set flag:

| Parameter | Description | Default |
|-----------|-------------|---------|
| `replicaCount` | Number of operator manager pod replicas | `1` |
| `image.repository` | Docker image repository | `ghcr.io/nicolas/dbconfigsync-operator` |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |
| `image.tag` | Docker image tag override | `""` (defaults to chart `appVersion`) |
| `serviceAccount.create` | Spec whether to create a ServiceAccount | `true` |
| `serviceAccount.name` | ServiceAccount name override | `""` |
| `rbac.create` | Setup ClusterRole and Bindings | `true` |
| `operator.watchNamespace` | Target namespace to watch for CRs | `"operator-system"` (blank for current namespace) |
| `resources` | Pod resource limits/requests | `cpu: 200m`, `memory: 256Mi` |

---

## Local Dry-Run Mode (Simulator)

For local development without a running Kubernetes cluster, you can run the operator in local mode. It polls database sources, generates a local .env configuration file, and mimics rolling updates by managing spawned child processes.

1. Create a `sync-config.json` in your local directory (standard JSON structure corresponding to the CRD spec).
2. Execute the simulator:
   ```bash
   go run main.go -mode dry-run -config sync-config.json -env .env
   ```
3. Watch the terminal logs. When databases change, the operator updates .env and sequentially restarts registered child processes.

---

## License

Distributed under the BSD 3-Clause License. See `LICENSE` for details.
