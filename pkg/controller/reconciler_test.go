package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

func TestOperatorAnnotationsChanged(t *testing.T) {
	tests := []struct {
		name     string
		existing map[string]string
		desired  map[string]string
		want     bool
	}{
		{
			name:     "both empty",
			existing: map[string]string{},
			desired:  map[string]string{},
			want:     false,
		},
		{
			name:     "desired key absent from existing",
			existing: map[string]string{},
			desired:  map[string]string{"reflector.v1.k8s.emberstack.com/reflection-allowed": "true"},
			want:     true,
		},
		{
			name:     "desired key present with wrong value",
			existing: map[string]string{"reflector.v1.k8s.emberstack.com/reflection-allowed": "false"},
			desired:  map[string]string{"reflector.v1.k8s.emberstack.com/reflection-allowed": "true"},
			want:     true,
		},
		{
			name:     "stale operator key no longer desired",
			existing: map[string]string{"reflector.v1.k8s.emberstack.com/reflection-allowed": "true"},
			desired:  map[string]string{},
			want:     true,
		},
		{
			name:     "third-party key not in desired does not trigger change",
			existing: map[string]string{"app.example.com/owner": "team-a"},
			desired:  map[string]string{},
			want:     false,
		},
		{
			name: "all desired keys already correct",
			existing: map[string]string{
				"reflector.v1.k8s.emberstack.com/reflection-allowed":      "true",
				"reflector.v1.k8s.emberstack.com/reflection-auto-enabled": "true",
			},
			desired: map[string]string{
				"reflector.v1.k8s.emberstack.com/reflection-allowed":      "true",
				"reflector.v1.k8s.emberstack.com/reflection-auto-enabled": "true",
			},
			want: false,
		},
		{
			name: "third-party key plus correct operator key — no change",
			existing: map[string]string{
				"app.example.com/owner":                                   "team-a",
				"reflector.v1.k8s.emberstack.com/reflection-allowed":     "true",
			},
			desired: map[string]string{
				"reflector.v1.k8s.emberstack.com/reflection-allowed": "true",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := operatorAnnotationsChanged(tt.existing, tt.desired)
			if got != tt.want {
				t.Errorf("operatorAnnotationsChanged() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMergeOperatorAnnotations(t *testing.T) {
	tests := []struct {
		name     string
		existing map[string]string
		desired  map[string]string
		want     map[string]string
	}{
		{
			name:     "adds desired operator keys to empty map",
			existing: map[string]string{},
			desired:  map[string]string{"reflector.v1.k8s.emberstack.com/reflection-allowed": "true"},
			want:     map[string]string{"reflector.v1.k8s.emberstack.com/reflection-allowed": "true"},
		},
		{
			name: "preserves third-party keys and drops stale operator keys",
			existing: map[string]string{
				"app.example.com/owner":                               "team-a",
				"reflector.v1.k8s.emberstack.com/reflection-allowed": "true",
			},
			desired: map[string]string{},
			want:    map[string]string{"app.example.com/owner": "team-a"},
		},
		{
			name: "removes operator key no longer in desired",
			existing: map[string]string{
				"reflector.v1.k8s.emberstack.com/reflection-allowed":      "true",
				"reflector.v1.k8s.emberstack.com/reflection-auto-enabled": "true",
			},
			desired: map[string]string{
				"reflector.v1.k8s.emberstack.com/reflection-allowed": "true",
			},
			want: map[string]string{
				"reflector.v1.k8s.emberstack.com/reflection-allowed": "true",
			},
		},
		{
			name: "merges third-party and desired operator keys",
			existing: map[string]string{
				"app.example.com/version": "v2",
			},
			desired: map[string]string{
				"reflector.v1.k8s.emberstack.com/reflection-allowed": "true",
			},
			want: map[string]string{
				"app.example.com/version":                             "v2",
				"reflector.v1.k8s.emberstack.com/reflection-allowed": "true",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeOperatorAnnotations(tt.existing, tt.desired)
			if len(got) != len(tt.want) {
				t.Fatalf("mergeOperatorAnnotations() len=%d want=%d; got=%v want=%v", len(got), len(tt.want), got, tt.want)
			}
			for k, wantV := range tt.want {
				if gotV, ok := got[k]; !ok || gotV != wantV {
					t.Errorf("key %q: got %q, want %q", k, gotV, wantV)
				}
			}
		})
	}
}

func TestNewK8sReconciler(t *testing.T) {
	kube := kubefake.NewSimpleClientset()

	t.Run("empty namespace defaults to default", func(t *testing.T) {
		r := NewK8sReconciler(kube, nil, "")
		if r.CRDNamespace != "default" {
			t.Errorf("expected namespace=default, got %q", r.CRDNamespace)
		}
	})

	t.Run("explicit namespace is preserved", func(t *testing.T) {
		r := NewK8sReconciler(kube, nil, "production")
		if r.CRDNamespace != "production" {
			t.Errorf("expected namespace=production, got %q", r.CRDNamespace)
		}
	})

	t.Run("versions map is initialized", func(t *testing.T) {
		r := NewK8sReconciler(kube, nil, "test")
		if r.Versions == nil {
			t.Error("Versions map must not be nil")
		}
	})
}

func TestSyncConfigMap(t *testing.T) {
	ctx := context.Background()
	ns := "default"
	data := map[string]string{"KEY": "value", "PORT": "8080"}

	t.Run("creates configmap when not found", func(t *testing.T) {
		kube := kubefake.NewSimpleClientset()
		r := NewK8sReconciler(kube, nil, ns)

		changed, err := r.syncConfigMap(ctx, "my-config", ns, data, ReflectionSpec{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true for new ConfigMap")
		}

		cm, err := kube.CoreV1().ConfigMaps(ns).Get(ctx, "my-config", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("ConfigMap not found after create: %v", err)
		}
		if cm.Data["KEY"] != "value" {
			t.Errorf("KEY: got %q, want %q", cm.Data["KEY"], "value")
		}
	})

	t.Run("updates configmap when data changed", func(t *testing.T) {
		existing := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "my-config", Namespace: ns},
			Data:       map[string]string{"KEY": "old-value"},
		}
		kube := kubefake.NewSimpleClientset(existing)
		r := NewK8sReconciler(kube, nil, ns)

		changed, err := r.syncConfigMap(ctx, "my-config", ns, data, ReflectionSpec{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true when data differs")
		}
	})

	t.Run("no update when data and annotations unchanged", func(t *testing.T) {
		existing := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "my-config", Namespace: ns},
			Data:       data,
		}
		kube := kubefake.NewSimpleClientset(existing)
		r := NewK8sReconciler(kube, nil, ns)

		changed, err := r.syncConfigMap(ctx, "my-config", ns, data, ReflectionSpec{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if changed {
			t.Error("expected changed=false when nothing differs")
		}
	})

	t.Run("creates configmap with reflector annotations", func(t *testing.T) {
		kube := kubefake.NewSimpleClientset()
		r := NewK8sReconciler(kube, nil, ns)
		reflection := ReflectionSpec{
			Allowed:           true,
			AllowedNamespaces: "ns-a,ns-b",
			AutoEnabled:       true,
			AutoNamespaces:    "ns-a",
		}

		_, err := r.syncConfigMap(ctx, "reflected-config", ns, data, reflection)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		cm, err := kube.CoreV1().ConfigMaps(ns).Get(ctx, "reflected-config", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("ConfigMap not found: %v", err)
		}
		checks := map[string]string{
			"reflector.v1.k8s.emberstack.com/reflection-allowed":            "true",
			"reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces": "ns-a,ns-b",
			"reflector.v1.k8s.emberstack.com/reflection-auto-enabled":       "true",
			"reflector.v1.k8s.emberstack.com/reflection-auto-namespaces":    "ns-a",
		}
		for k, want := range checks {
			if got := cm.Annotations[k]; got != want {
				t.Errorf("annotation %q: got %q, want %q", k, got, want)
			}
		}
	})

	t.Run("updates annotations when reflection toggled on", func(t *testing.T) {
		existing := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "my-config", Namespace: ns},
			Data:       data,
		}
		kube := kubefake.NewSimpleClientset(existing)
		r := NewK8sReconciler(kube, nil, ns)
		reflection := ReflectionSpec{Allowed: true}

		changed, err := r.syncConfigMap(ctx, "my-config", ns, data, reflection)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true when annotations differ")
		}
	})
}

func TestSyncSecret(t *testing.T) {
	ctx := context.Background()
	ns := "default"
	data := map[string]string{"DB_PASS": "s3cr3t"}

	t.Run("creates secret when not found", func(t *testing.T) {
		kube := kubefake.NewSimpleClientset()
		r := NewK8sReconciler(kube, nil, ns)

		changed, err := r.syncSecret(ctx, "my-secret", ns, data, ReflectionSpec{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true for new Secret")
		}

		sec, err := kube.CoreV1().Secrets(ns).Get(ctx, "my-secret", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("Secret not found after create: %v", err)
		}
		if string(sec.Data["DB_PASS"]) != "s3cr3t" {
			t.Errorf("DB_PASS: got %q, want %q", string(sec.Data["DB_PASS"]), "s3cr3t")
		}
		if sec.Type != corev1.SecretTypeOpaque {
			t.Errorf("expected Opaque type, got %v", sec.Type)
		}
	})

	t.Run("updates secret when data changed", func(t *testing.T) {
		existing := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: ns},
			Data:       map[string][]byte{"DB_PASS": []byte("old-pass")},
		}
		kube := kubefake.NewSimpleClientset(existing)
		r := NewK8sReconciler(kube, nil, ns)

		changed, err := r.syncSecret(ctx, "my-secret", ns, data, ReflectionSpec{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true when data differs")
		}
	})

	t.Run("no update when data and annotations unchanged", func(t *testing.T) {
		existing := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: ns},
			Data:       map[string][]byte{"DB_PASS": []byte("s3cr3t")},
		}
		kube := kubefake.NewSimpleClientset(existing)
		r := NewK8sReconciler(kube, nil, ns)

		changed, err := r.syncSecret(ctx, "my-secret", ns, data, ReflectionSpec{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if changed {
			t.Error("expected changed=false when nothing differs")
		}
	})

	t.Run("creates secret with reflector annotations", func(t *testing.T) {
		kube := kubefake.NewSimpleClientset()
		r := NewK8sReconciler(kube, nil, ns)
		reflection := ReflectionSpec{Allowed: true, AllowedNamespaces: "prod"}

		_, err := r.syncSecret(ctx, "reflected-secret", ns, data, reflection)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		sec, err := kube.CoreV1().Secrets(ns).Get(ctx, "reflected-secret", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("Secret not found: %v", err)
		}
		if sec.Annotations["reflector.v1.k8s.emberstack.com/reflection-allowed"] != "true" {
			t.Error("missing reflection-allowed annotation on Secret")
		}
		if sec.Annotations["reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces"] != "prod" {
			t.Error("missing reflection-allowed-namespaces annotation on Secret")
		}
	})
}
