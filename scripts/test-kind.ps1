# DbConfigSync Operator Kind Bootstrap & Verification Script (Windows PowerShell)
$ErrorActionPreference = "Stop"

function Write-Step ($msg) {
    Write-Host "`n[STEP] $msg..." -ForegroundColor Cyan
}

function Write-Success ($msg) {
    Write-Host "`n[SUCCESS] $msg" -ForegroundColor Green
}

Write-Step "1. Checking Docker and Kind status on Windows"
try {
    $dockerVer = docker --version
    $kindVer = kind --version
    Write-Host "Using $dockerVer"
    Write-Host "Using $kindVer"
} catch {
    Write-Error "Docker or Kind is not running or not in your Windows system PATH. Please make sure Docker Desktop is active."
}

Write-Step "2. Building Docker image locally on Windows using multi-stage Dockerfile"
docker build -t dbconfigsync-operator:latest .
if ($LASTEXITCODE -ne 0) {
    Write-Error "Docker image build failed."
}

Write-Step "4. Creating Kind Kubernetes cluster 'dbconfig-test'"
$clusters = kind get clusters
if ($clusters -contains "dbconfig-test") {
    Write-Host "Cluster 'dbconfig-test' already exists. Re-using cluster."
} else {
    kind create cluster --name dbconfig-test --image kindest/node:v1.27.3
}

# Set current context
kubectl config use-context kind-dbconfig-test

Write-Step "5. Loading image into Kind cluster"
kind load docker-image dbconfigsync-operator:latest --name dbconfig-test

Write-Step "6. Installing Reloader (Stakater) and Reflector (Emberstack) controllers"
Write-Host "Deploying Stakater Reloader..."
kubectl apply -f https://raw.githubusercontent.com/stakater/Reloader/master/deployments/kubernetes/reloader.yaml

Write-Host "Deploying Emberstack Reflector..."
kubectl apply -f https://github.com/emberstack/kubernetes-reflector/releases/latest/download/reflector.yaml

Write-Step "7. Creating namespaces and deploying test databases"
kubectl create namespace operator-system --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f deploy/test-databases.yaml

Write-Step "8. Waiting for databases to roll out"
Write-Host "Waiting for Postgres deployment..."
kubectl rollout status deployment/postgres -n databases --timeout=120s
Write-Host "Waiting for Redis deployment..."
kubectl rollout status deployment/redis -n databases --timeout=120s

Write-Step "9. Seeding Postgres database config tables"
# Wait for postgres connection acceptance
$pgReady = $false
while (-not $pgReady) {
    $null = kubectl exec -n databases deployment/postgres -- pg_isready -U postgres 2>&1
    if ($LASTEXITCODE -eq 0) {
        $pgReady = $true
    } else {
        Write-Host "Postgres starting, waiting 2s..."
        Start-Sleep -Seconds 2
    }
}

# Run SQL seeding commands
kubectl exec -n databases deployment/postgres -- psql -U postgres -d app_db -c "CREATE TABLE IF NOT EXISTS app_settings (key VARCHAR(50) PRIMARY KEY, val VARCHAR(255) NOT NULL, is_active INT DEFAULT 1);"
kubectl exec -n databases deployment/postgres -- psql -U postgres -d app_db -c "TRUNCATE TABLE app_settings;"
kubectl exec -n databases deployment/postgres -- psql -U postgres -d app_db -c "INSERT INTO app_settings (key, val, is_active) VALUES ('DB_USER', 'db_prod_user', 1), ('DB_PASS', 'securedpwd456', 1), ('DB_HOST', 'postgres-service.databases.svc.cluster.local', 1), ('DB_PORT', '5432', 1), ('DB_NAME', 'app_db', 1), ('CORS_WEB', 'https://dev.example.com', 1), ('CORS_MOBILE', 'app://mobile.example.com', 1);"

# Seed system_metadata table for testing key mapping
kubectl exec -n databases deployment/postgres -- psql -U postgres -d app_db -c "CREATE TABLE IF NOT EXISTS system_metadata (installation_id VARCHAR(50) PRIMARY KEY, license_key VARCHAR(255) NOT NULL, extra_info TEXT);"
kubectl exec -n databases deployment/postgres -- psql -U postgres -d app_db -c "TRUNCATE TABLE system_metadata;"
kubectl exec -n databases deployment/postgres -- psql -U postgres -d app_db -c "INSERT INTO system_metadata (installation_id, license_key, extra_info) VALUES ('inst-98765-xyz', 'lic-abcde-12345-99', '{\`"database\`": {\`"username\`": \`"prod_user\`", \`"max_connections\`": 100}, \`"roles\`": [\`"admin\`", \`"developer\`"]}'::json);"

Write-Step "10. Seeding Redis database config hashes"
# Check redis ready
$redisReady = $false
while (-not $redisReady) {
    $ping = ""
    try {
        $ping = kubectl exec -n databases deployment/redis -- redis-cli ping 2>&1
    } catch {}
    if ($LASTEXITCODE -eq 0 -and $ping -like "*PONG*") {
        $redisReady = $true
    } else {
        Write-Host "Redis starting, waiting 2s..."
        Start-Sleep -Seconds 2
    }
}

kubectl exec -n databases deployment/redis -- redis-cli HSET settings:app REDIS_NODE_1 "redis-east-1.svc.cluster.local" REDIS_NODE_2 "redis-east-2.svc.cluster.local" REDIS_NODE_3 "redis-east-3.svc.cluster.local"

Write-Success "Test databases seeded with raw configurations!"

Write-Step "11. Deploying Operator and target test deployments"
kubectl apply -f deploy/crds/config.operator.io_dbconfigsyncs.yaml
kubectl apply -f deploy/rbac.yaml
kubectl apply -f deploy/deployment.yaml
kubectl rollout restart deployment/dbconfigsync-operator -n operator-system
kubectl apply -f deploy/test-apps.yaml

# Wait for operator to roll out
Write-Host "Waiting for Operator Deployment..."
kubectl rollout status deployment/dbconfigsync-operator -n operator-system --timeout=120s

Write-Step "12. Applying DbConfigSync Custom Resource"
kubectl apply -f deploy/sample-cr.yaml

Write-Success "All resources successfully applied to Kind cluster!"

Write-Step "13. Verifying configuration synchronization and transformations"
Write-Host "Waiting 15 seconds for reconciliation and propagation..."
Start-Sleep -Seconds 15

# Fetch the generated ConfigMap
$cmJson = kubectl get configmap app-shared-env -n operator-system -o json
if (-not $cmJson) {
    Write-Error "Target ConfigMap 'app-shared-env' not found in namespace 'operator-system'."
}
$cm = $cmJson | ConvertFrom-Json
$data = $cm.data

Write-Host "`n--- Verification Assertions ---"
Write-Host "DB_USER          = $($data.DB_USER) (Expected: db_prod_user)"
Write-Host "SYS_INSTALL_ID   = $($data.SYS_INSTALL_ID) (Expected: inst-98765-xyz)"
Write-Host "SYS_LICENSE_KEY  = $($data.SYS_LICENSE_KEY) (Expected: lic-abcde-12345-99)"
Write-Host "DB_PASS_B64      = $($data.DB_PASS_B64) (Expected: c2VjdXJlZHB3ZDQ1Ng==)"
Write-Host "EXTRA_DB_USER    = $($data.EXTRA_DB_USER) (Expected: prod_user)"
Write-Host "EXTRA_FIRST_ROLE = $($data.EXTRA_FIRST_ROLE) (Expected: admin)"

# Verify expected values
if ($data.SYS_INSTALL_ID -ne "inst-98765-xyz" -or 
    $data.SYS_LICENSE_KEY -ne "lic-abcde-12345-99" -or 
    $data.EXTRA_DB_USER -ne "prod_user" -or 
    $data.EXTRA_FIRST_ROLE -ne "admin" -or
    $data.DB_PASS_B64 -ne "c2VjdXJlZHB3ZDQ1Ng==") {
    Write-Error "Verification failed: One or more configuration values do not match expected values!"
}

# Verify Reflector mirror ConfigMaps in app-dev and app-prod namespaces
Write-Host "`nVerifying namespace reflection..."
$cmDevJson = kubectl get configmap app-shared-env -n app-dev -o json
$cmProdJson = kubectl get configmap app-shared-env -n app-prod -o json
if (-not $cmDevJson -or -not $cmProdJson) {
    Write-Error "Verification failed: Mirror ConfigMap 'app-shared-env' was not replicated to app-dev or app-prod namespace by Emberstack Reflector!"
}
Write-Host "Mirror ConfigMaps successfully replicated!"

Write-Success "All configurations successfully synchronized and verified!"

Write-Step "14. Cleaning up and deleting Kind cluster"
kind delete cluster --name dbconfig-test
Write-Success "Kind cluster 'dbconfig-test' deleted successfully!"
