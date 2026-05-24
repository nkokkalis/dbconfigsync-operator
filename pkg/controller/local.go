package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"time"

	"operator/pkg/dbclient"
)

// LocalReconciler simulates the operator locally using standard OS child processes.
type LocalReconciler struct {
	ConfigFile     string
	OutEnvFile     string
	ActiveConfigs  map[string]string
	ActiveVersion  int
	Processes      map[string]*exec.Cmd
	ProcessMutex   sync.Mutex
}

// NewLocalReconciler creates a reconciler for local process simulation.
func NewLocalReconciler(configFile, outEnvFile string) *LocalReconciler {
	return &LocalReconciler{
		ConfigFile:    configFile,
		OutEnvFile:    outEnvFile,
		ActiveConfigs: make(map[string]string),
		Processes:     make(map[string]*exec.Cmd),
	}
}

// RunReconciliationLoop runs the local filesystem/process reconciler.
func (r *LocalReconciler) RunReconciliationLoop(ctx context.Context) {
	EmitLog("operator", "info", "Starting Local Dry-Run operator loop...")
	EmitLog("operator", "info", "Watching local configuration file: %s", r.ConfigFile)
	EmitLog("operator", "info", "Output environment variables file: %s", r.OutEnvFile)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Ensure config file template exists
	r.ensureConfigFileTemplate()

	for {
		select {
		case <-ctx.Done():
			EmitLog("operator", "info", "Local reconciler shutting down. Terminating child processes...")
			r.terminateAllProcesses()
			return
		case <-ticker.C:
			r.reconcileLocal(ctx)
		}
	}
}

func (r *LocalReconciler) reconcileLocal(ctx context.Context) {
	// Read and parse config file
	data, err := os.ReadFile(r.ConfigFile)
	if err != nil {
		EmitLog("operator", "error", "Failed to read local config file '%s': %v", r.ConfigFile, err)
		return
	}

	var spec DbConfigSyncSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		EmitLog("operator", "error", "Failed to parse local config json: %v", err)
		return
	}

	dbStatuses := make(map[string]string)
	aggregatedConfigs := make(map[string]string)
	hasError := false
	var errMsgs []string

	// 1. Fetch values from each database
	for idx, dbSpec := range spec.Databases {
		dbId := fmt.Sprintf("%s-%d", dbSpec.Type, idx)
		EmitLog("operator", "info", "Querying %s database locally...", dbSpec.Type)

		uri := dbSpec.ConnectionUri
		if uri == "" {
			dbStatuses[dbId] = "ConfigError"
			errMsgs = append(errMsgs, fmt.Sprintf("Empty connection URI for db source %s", dbId))
			EmitLog("operator", "error", "Local DB %s connection URI is empty", dbId)
			hasError = true
			continue
		}

		dbCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		configs, err := dbclient.FetchConfig(dbCtx, dbSpec.Type, uri, dbSpec.Query)
		cancel()

		if err != nil {
			dbStatuses[dbId] = "Disconnected"
			errMsgs = append(errMsgs, fmt.Sprintf("%s query failed: %v", dbSpec.Type, err))
			EmitLog(dbSpec.Type, "error", "Failed to query database config: %v", err)
			hasError = true
			continue
		}

		dbStatuses[dbId] = "Connected"
		EmitLog(dbSpec.Type, "success", "Successfully retrieved %d keys from local DB", len(configs))
		
		for k, v := range configs {
			targetKey := k
			if dbSpec.KeyMapping != nil {
				if mappedKey, ok := dbSpec.KeyMapping[k]; ok && mappedKey != "" {
					targetKey = mappedKey
				}
			}
			aggregatedConfigs[targetKey] = v
		}
	}

	// In local dry-run, we report error status to logs and return
	if hasError {
		EmitLog("operator", "error", "Reconciliation failed: %s", strings.Join(errMsgs, "; "))
		return
	}

	// 2. Run value transforms
	EmitLog("operator", "info", "Executing value transformations locally...")
	finalConfigs, err := ProcessTransforms(aggregatedConfigs, spec.Transforms)
	if err != nil {
		EmitLog("operator", "error", "Transformations failed: %v", err)
		return
	}

	// 3. Compare configuration and update local output env file
	configChanged := !reflect.DeepEqual(r.ActiveConfigs, finalConfigs)
	if configChanged || r.ActiveVersion == 0 {
		r.ActiveVersion++
		r.ActiveConfigs = finalConfigs

		// Write environment variables to local file (e.g. .env format)
		if err := r.writeEnvFile(finalConfigs); err != nil {
			EmitLog("operator", "error", "Failed to write local env file: %v", err)
		} else {
			EmitLog("operator", "success", "Updated environment variables file: %s", r.OutEnvFile)
		}

		EmitLog("operator", "success", "Configuration version updated to v%d. Spawning rolling restart of local services...", r.ActiveVersion)
		
		// Simulate rolling restart of microservices
		r.restartLocalServices(spec.TargetConfigMap) // local simulation uses targetConfigMap string for service name identification
	}
}

func (r *LocalReconciler) writeEnvFile(configs map[string]string) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# EnvSync Auto-Generated Environment Variables (Version v%d)\n", r.ActiveVersion)
	fmt.Fprintf(&sb, "# Generated at: %s\n\n", time.Now().Format(time.RFC3339))

	for k, v := range configs {
		escapedV := strings.ReplaceAll(v, "\r", "\\r")
		escapedV = strings.ReplaceAll(escapedV, "\n", "\\n")
		fmt.Fprintf(&sb, "%s=%s\n", k, escapedV)
	}

	return os.WriteFile(r.OutEnvFile, []byte(sb.String()), 0600)
}

func (r *LocalReconciler) restartLocalServices(serviceListStr string) {
	// Parse services from the target ConfigMap specification (comma-separated string in local mode)
	services := strings.Split(serviceListStr, ",")
	if len(services) == 1 && services[0] == "" {
		services = []string{"app-worker", "app-notifier"}
	}

	// Snapshot ActiveConfigs before spawning the goroutine to avoid a data race:
	// reconcileLocal can reassign r.ActiveConfigs on the next tick while the goroutine
	// is still iterating it.
	configSnapshot := make(map[string]string, len(r.ActiveConfigs))
	for k, v := range r.ActiveConfigs {
		configSnapshot[k] = v
	}

	// Rolling restart logic (sequential restart with small delay)
	go func() {
		for _, service := range services {
			sName := strings.TrimSpace(service)
			if sName == "" {
				continue
			}

			EmitLog("operator", "warn", "Initiating rolling restart for local service '%s'...", sName)

			// Kill existing process — lock only around the map READ
			r.ProcessMutex.Lock()
			cmd, exists := r.Processes[sName]
			r.ProcessMutex.Unlock()

			if exists && cmd.Process != nil {
				EmitLog("operator", "info", "Stopping active PID %d for service '%s'...", cmd.Process.Pid, sName)
				_ = cmd.Process.Kill()
				_, _ = cmd.Process.Wait()
			}

			// Prepare command to launch simulated process
			// We re-execute ourselves with a special flag: main.go -mode service -serviceName <name>
			execPath, err := os.Executable()
			if err != nil || strings.Contains(execPath, "go-build") {
				// Fallback to running go run if executing via go run
				execPath = "go"
			}

			var args []string
			if execPath == "go" {
				args = []string{"run", "main.go", "-mode", "service", "-serviceName", sName}
			} else {
				args = []string{"-mode", "service", "-serviceName", sName}
			}

			cmd = exec.Command(execPath, args...)

			// Inject the snapshotted environment variables into the process env
			env := os.Environ()
			for k, v := range configSnapshot {
				env = append(env, fmt.Sprintf("%s=%s", k, v))
			}
			cmd.Env = env

			// Pipe logs of child process to operator log output
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr

			err = cmd.Start()
			if err != nil {
				EmitLog("operator", "error", "Failed to start local service '%s': %v", sName, err)
				continue
			}

			// Lock only around the map WRITE
			r.ProcessMutex.Lock()
			r.Processes[sName] = cmd
			r.ProcessMutex.Unlock()

			EmitLog("operator", "success", "Service '%s' successfully restarted with new env configs (PID: %d)", sName, cmd.Process.Pid)

			// Small delay to simulate rolling update
			time.Sleep(2 * time.Second)
		}
		EmitLog("operator", "success", "All local services successfully reconciled. Status: HEALTHY.")
	}()
}

func (r *LocalReconciler) terminateAllProcesses() {
	r.ProcessMutex.Lock()
	defer r.ProcessMutex.Unlock()

	for name, cmd := range r.Processes {
		if cmd.Process != nil {
			EmitLog("local", "info", "Killing process %s (PID: %d)...", name, cmd.Process.Pid)
			_ = cmd.Process.Kill()
		}
	}
}

func (r *LocalReconciler) ensureConfigFileTemplate() {
	if _, err := os.Stat(r.ConfigFile); os.IsNotExist(err) {
		// Write a sample sync-config.json
		sampleSpec := map[string]interface{}{
			"targetConfigMap": "app-worker,app-notifier",
			"databases": []map[string]interface{}{
				{
					"type":          "postgresql",
					"connectionUri": "postgres://postgres:postgres@localhost:5432/app_db?sslmode=disable",
					"query":         "SELECT config_key, config_value FROM config_settings WHERE active = true",
				},
				{
					"type":          "redis",
					"connectionUri": "redis://localhost:6379/0",
					"query":         "HGETALL settings:app",
				},
			},
			"transforms": []map[string]interface{}{
				{
					"name":     "DATABASE_URL",
					"type":     "template",
					"template": "postgres://{{.DB_USER}}:{{.DB_PASS}}@{{.DB_HOST}}:{{.DB_PORT}}/{{.DB_NAME}}?sslmode=disable",
				},
			},
		}

		bytes, _ := json.MarshalIndent(sampleSpec, "", "  ")
		_ = os.WriteFile(r.ConfigFile, bytes, 0644)
	}
}
