#!/bin/bash
set -e

# Visual formatting helper
print_step() {
    echo -e "\n\033[1;36m[STEP] $1...\033[0m"
}

print_success() {
    echo -e "\n\033[1;32m[SUCCESS] $1\033[0m"
}

print_step "1. Checking Kind and Docker status"
if ! command -v kind &> /dev/null; then
    echo "ERROR: Kind is not installed in WSL. Please install kind first."
    exit 1
fi

if ! docker ps &> /dev/null; then
    echo "ERROR: Docker daemon is not running or not accessible in WSL."
    echo "Please activate WSL integration in Docker Desktop settings."
    exit 1
fi

print_step "2. Creating Kind Kubernetes cluster 'dbconfig-test'"
if kind get clusters | grep -q "^dbconfig-test$"; then
    echo "Cluster 'dbconfig-test' already exists. Re-using existing cluster."
    kubectl cluster-info --context kind-dbconfig-test
else
    kind create cluster --name dbconfig-test --image kindest/node:v1.27.3
fi

# Set context
kubectl config use-context kind-dbconfig-test

print_step "3. Installing Reloader (Stakater) and Reflector (Emberstack) controllers"
# Deploy Reloader (watches ConfigMaps/Secrets and restarts Pods)
echo "Deploying Stakater Reloader..."
kubectl apply -f https://raw.githubusercontent.com/stakater/Reloader/master/deployments/kubernetes/reloader.yaml

# Deploy Reflector (replicates ConfigMaps/Secrets across namespaces)
echo "Deploying Emberstack Reflector..."
kubectl apply -f https://github.com/emberstack/kubernetes-reflector/releases/latest/download/reflector.yaml

print_step "4. Setting up namespaces and deploying test databases"
# Create operator system namespace
kubectl create namespace operator-system 2>/dev/null || true
# Deploy databases (Postgres and Redis)
kubectl apply -f deploy/test-databases.yaml

print_step "5. Waiting for databases to be ready"
echo "Waiting for Postgres deployment..."
kubectl rollout status deployment/postgres -n databases --timeout=120s
echo "Waiting for Redis deployment..."
kubectl rollout status deployment/redis -n databases --timeout=120s

print_step "6. Seeding databases with configurations"
echo "Seeding Postgres table 'app_settings'..."
# Wait for postgres to accept connections
until kubectl exec -n databases deployment/postgres -- pg_isready -U postgres; do
    echo "Postgres is starting, retrying in 2s..."
    sleep 2
done

# Create table and insert variables
kubectl exec -n databases deployment/postgres -i -- psql -U postgres -d app_db <<EOF
CREATE TABLE IF NOT EXISTS app_settings (
    key VARCHAR(50) PRIMARY KEY,
    val VARCHAR(255) NOT NULL,
    is_active INT DEFAULT 1
);
TRUNCATE TABLE app_settings;
INSERT INTO app_settings (key, val, is_active) VALUES 
('DB_USER', 'db_prod_user', 1),
('DB_PASS', 'securedpwd456', 1),
('DB_HOST', 'postgres-service.databases.svc.cluster.local', 1),
('DB_PORT', '5432', 1),
('DB_NAME', 'app_db', 1),
('CORS_WEB', 'https://dev.example.com', 1),
('CORS_MOBILE', 'app://mobile.example.com', 1);

CREATE TABLE IF NOT EXISTS system_metadata (
    installation_id VARCHAR(50) PRIMARY KEY,
    license_key VARCHAR(255) NOT NULL,
    extra_info TEXT
);
TRUNCATE TABLE system_metadata;
INSERT INTO system_metadata (installation_id, license_key, extra_info) VALUES 
('inst-98765-xyz', 'lic-abcde-12345-99', '{"database": {"username": "prod_user", "max_connections": 100}, "roles": ["admin", "developer"]}');
EOF

echo "Seeding Redis hash 'settings:app'..."
until kubectl exec -n databases deployment/redis -- redis-cli ping | grep -q "PONG"; do
    echo "Redis is starting, retrying in 2s..."
    sleep 2
done

kubectl exec -n databases deployment/redis -- redis-cli HSET settings:app \
    REDIS_NODE_1 "redis-east-1.svc.cluster.local" \
    REDIS_NODE_2 "redis-east-2.svc.cluster.local" \
    REDIS_NODE_3 "redis-east-3.svc.cluster.local"

print_success "Databases seeded successfully!"

print_step "7. Building Operator Docker image locally"
# Compile check and docker build
go mod tidy
docker build -t dbconfigsync-operator:latest .

print_step "8. Loading Operator image into Kind cluster"
kind load docker-image dbconfigsync-operator:latest --name dbconfig-test

print_step "9. Deploying Operator and target test deployments"
# Apply CRD definition
kubectl apply -f deploy/crds/config.operator.io_dbconfigsyncs.yaml
# Apply RBAC configuration
kubectl apply -f deploy/rbac.yaml
# Deploy the operator manager
kubectl apply -f deploy/deployment.yaml
kubectl rollout restart deployment/dbconfigsync-operator -n operator-system
# Deploy test client apps in app-dev and app-prod namespaces
kubectl apply -f deploy/test-apps.yaml

# Wait for operator to roll out
echo "Waiting for Operator Deployment..."
kubectl rollout status deployment/dbconfigsync-operator -n operator-system --timeout=120s

print_step "10. Applying DbConfigSync Custom Resource"
kubectl apply -f deploy/sample-cr.yaml

print_success "Deployment completed successfully!"

print_step "11. Verifying configuration synchronization and transformations"
echo "Waiting 15 seconds for reconciliation and propagation..."
sleep 15

# Fetch the generated ConfigMap
cm_json=$(kubectl get configmap app-shared-env -n operator-system -o json)
if [ -z "$cm_json" ]; then
    echo "ERROR: Target ConfigMap 'app-shared-env' not found in namespace 'operator-system'."
    exit 1
fi

get_val() {
    echo "$1" | python3 -c "import sys, json; print(json.load(sys.stdin).get('data', {}).get('$2', ''))"
}

db_user=$(get_val "$cm_json" "DB_USER")
sys_install_id=$(get_val "$cm_json" "SYS_INSTALL_ID")
sys_license_key=$(get_val "$cm_json" "SYS_LICENSE_KEY")
db_pass_b64=$(get_val "$cm_json" "DB_PASS_B64")
extra_db_user=$(get_val "$cm_json" "EXTRA_DB_USER")
extra_first_role=$(get_val "$cm_json" "EXTRA_FIRST_ROLE")

echo -e "\n--- Verification Assertions ---"
echo "DB_USER          = $db_user (Expected: db_prod_user)"
echo "SYS_INSTALL_ID   = $sys_install_id (Expected: inst-98765-xyz)"
echo "SYS_LICENSE_KEY  = $sys_license_key (Expected: lic-abcde-12345-99)"
echo "DB_PASS_B64      = $db_pass_b64 (Expected: c2VjdXJlZHB3ZDQ1Ng==)"
echo "EXTRA_DB_USER    = $extra_db_user (Expected: prod_user)"
echo "EXTRA_FIRST_ROLE = $extra_first_role (Expected: admin)"

if [ "$sys_install_id" != "inst-98765-xyz" ] || \
   [ "$sys_license_key" != "lic-abcde-12345-99" ] || \
   [ "$extra_db_user" != "prod_user" ] || \
   [ "$extra_first_role" != "admin" ] || \
   [ "$db_pass_b64" != "c2VjdXJlZHB3ZDQ1Ng==" ]; then
    echo "ERROR: Verification failed: One or more configuration values do not match expected values!"
    exit 1
fi

echo -e "\nVerifying namespace reflection..."
dev_cm=$(kubectl get configmap app-shared-env -n app-dev -o json)
prod_cm=$(kubectl get configmap app-shared-env -n app-prod -o json)
if [ -z "$dev_cm" ] || [ -z "$prod_cm" ]; then
    echo "ERROR: Verification failed: Mirror ConfigMap was not replicated by Emberstack Reflector!"
    exit 1
fi
echo "Mirror ConfigMaps successfully replicated!"

print_success "All configurations successfully synchronized and verified!"

print_step "12. Cleaning up and deleting Kind cluster"
kind delete cluster --name dbconfig-test
print_success "Kind cluster 'dbconfig-test' deleted successfully!"
