package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"operator/pkg/controller"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

func main() {
	// 1. Define command line flags
	mode := flag.String("mode", "kubernetes", "Execution mode: 'kubernetes', 'local' (dry-run), or 'service' (simulated process)")
	kubeconfig := flag.String("kubeconfig", "", "Path to the kubeconfig file (optional)")
	namespace := flag.String("namespace", "operator-system", "Kubernetes namespace to watch for DbConfigSync CRs")

	// Local dry-run parameters
	localConfig := flag.String("config", "sync-config.json", "Local config JSON file (for local dry-run)")
	localEnvOut := flag.String("env", "app-shared-env.properties", "Output environment variables file (for local dry-run)")
	serviceName := flag.String("serviceName", "app-worker", "Name of the service (for service mode)")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle OS shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nShutdown signal received. Gracefully shutting down...")
		cancel()
	}()

	switch strings.ToLower(*mode) {
	case "service":
		runSimulatedService(ctx, *serviceName)

	case "local":
		// Run Local Simulation Mode
		reconciler := controller.NewLocalReconciler(*localConfig, *localEnvOut)
		reconciler.RunReconciliationLoop(ctx)

	case "kubernetes":
		// Run Kubernetes Operator Mode
		k8sClient, dynClient, err := buildK8sClients(*kubeconfig)
		if err != nil {
			fmt.Printf("ERROR: Failed to establish Kubernetes cluster client connection: %v\n", err)
			fmt.Println("If running locally outside the cluster, make sure your kubeconfig is valid or use '-mode local' for dry-run testing.")
			os.Exit(1)
		}

		reconciler := controller.NewK8sReconciler(k8sClient, dynClient, *namespace)
		reconciler.RunReconciliationLoop(ctx)

	default:
		fmt.Printf("ERROR: Invalid execution mode '%s'. Supported modes: 'kubernetes', 'local', 'service'\n", *mode)
		os.Exit(1)
	}
}

// runSimulatedService starts a loop logging injected environment variables.
func runSimulatedService(ctx context.Context, name string) {
	fmt.Printf("[%s] Mock microservice process started (PID: %d)\n", strings.ToUpper(name), os.Getpid())

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Printf("[%s] Shutting down simulated process (PID: %d)\n", strings.ToUpper(name), os.Getpid())
			return
		case <-ticker.C:
			var activeEnvs []string
			// Collect database/shared vars injected
			for _, env := range os.Environ() {
				parts := strings.SplitN(env, "=", 2)
				if len(parts) < 2 {
					continue
				}
				key := parts[0]
				val := parts[1]

				// Filters variables relevant to configuration syncing
				if strings.HasPrefix(key, "DB_") ||
					strings.HasPrefix(key, "REDIS_") ||
					strings.HasPrefix(key, "MONGO_") ||
					strings.HasPrefix(key, "DATABASE_") ||
					strings.HasPrefix(key, "CORS_") {
					// Obfuscate sensitive credentials
					if strings.Contains(key, "PASS") ||
						strings.Contains(key, "SECRET") ||
						strings.Contains(key, "URL") ||
						strings.Contains(key, "TOKEN") ||
						strings.Contains(key, "KEY") ||
						strings.Contains(key, "DSN") ||
						strings.Contains(key, "CREDENTIAL") {
						val = "********"
					}
					activeEnvs = append(activeEnvs, fmt.Sprintf("%s=%s", key, val))
				}
			}

			if len(activeEnvs) == 0 {
				fmt.Printf("[%s] Awaiting operator environment variable injection...\n", strings.ToUpper(name))
			} else {
				fmt.Printf("[%s] Running with environment config: %s\n", strings.ToUpper(name), strings.Join(activeEnvs, ", "))
			}
		}
	}
}

// buildK8sClients establishes connections to the Kubernetes cluster API.
func buildK8sClients(kubeconfigPath string) (kubernetes.Interface, dynamic.Interface, error) {
	var config *rest.Config
	var err error

	// 1. Try In-Cluster configuration (running inside Pod)
	config, err = rest.InClusterConfig()
	if err == nil {
		fmt.Println("[K8S] Using in-cluster credentials configuration.")
	} else {
		// 2. Try explicit command line flag
		if kubeconfigPath != "" {
			config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		} else {
			// 3. Try standard KUBECONFIG environment variable or default ~/.kube/config
			kubeenv := os.Getenv("KUBECONFIG")
			if kubeenv != "" {
				config, err = clientcmd.BuildConfigFromFlags("", kubeenv)
			} else if home := homedir.HomeDir(); home != "" {
				path := filepath.Join(home, ".kube", "config")
				config, err = clientcmd.BuildConfigFromFlags("", path)
			}
		}
	}

	if err != nil {
		return nil, nil, fmt.Errorf("failed to load Kubernetes config: %w", err)
	}

	// Create clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create client: %w", err)
	}

	// Create dynamic client (needed for Custom Resources)
	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	return clientset, dynClient, nil
}
