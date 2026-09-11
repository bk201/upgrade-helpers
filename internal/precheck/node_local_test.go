package precheck

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestLocalNetworkConfig(t *testing.T) {
	tests := []struct {
		name       string
		custom     bool
		legacy     bool
		config     string
		wantStatus Status
	}{
		{name: "current schema", custom: true, config: "install:\n  management_interface:\n    interfaces: [eth0]\n", wantStatus: Pass},
		{name: "legacy custom file", legacy: true, config: "install:\n  managementinterface: eth0\n", wantStatus: Fail},
		{name: "old network schema", custom: true, config: "install:\n  networks: [eth0]\n", wantStatus: Fail},
		{name: "missing management interface", custom: true, config: "install: {}\n", wantStatus: Fail},
		{name: "invalid YAML", custom: true, config: "install: [", wantStatus: Error},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			oem := filepath.Join(root, "oem")
			if err := os.MkdirAll(oem, 0o755); err != nil {
				t.Fatal(err)
			}
			if test.custom {
				if err := os.WriteFile(filepath.Join(oem, "90_custom.yaml"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if test.legacy {
				if err := os.WriteFile(filepath.Join(oem, "99_custom.yaml"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(oem, "harvester.config"), []byte(test.config), 0o600); err != nil {
				t.Fatal(err)
			}
			got := localNetworkConfig("node-1", root)
			if got.Status != test.wantStatus {
				t.Fatalf("got %+v, want status %s", got, test.wantStatus)
			}
		})
	}
}

func TestLocalCertificates(t *testing.T) {
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		notAfter   time.Time
		wantStatus Status
	}{
		{name: "valid", notAfter: now.Add(11 * 24 * time.Hour), wantStatus: Pass},
		{name: "warning", notAfter: now.Add(9 * 24 * time.Hour), wantStatus: Warning},
		{name: "expired", notAfter: now.Add(-time.Hour), wantStatus: Fail},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			directory := filepath.Join(root, "var/lib/rancher/rke2/server/tls")
			if err := os.MkdirAll(directory, 0o755); err != nil {
				t.Fatal(err)
			}
			writeCertificate(t, filepath.Join(directory, "server.crt"), now.Add(-time.Hour), test.notAfter)
			got := localCertificates("node-1", root, now)
			if got.Status != test.wantStatus {
				t.Fatalf("got %+v, want status %s", got, test.wantStatus)
			}
		})
	}
}

func writeCertificate(t *testing.T, path string, notBefore, notAfter time.Time) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"}, NotBefore: notBefore, NotAfter: notAfter, KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAggregateNodeResults(t *testing.T) {
	tests := []struct {
		name string
		in   []NodeResult
		want Status
	}{
		{name: "pass", in: []NodeResult{{Node: "a", Status: Pass}, {Node: "b", Status: Pass}}, want: Pass},
		{name: "warning", in: []NodeResult{{Node: "a", Status: Pass}, {Node: "b", Status: Warning}}, want: Warning},
		{name: "failure", in: []NodeResult{{Node: "a", Status: Fail}, {Node: "b", Status: Warning}}, want: Fail},
		{name: "error wins", in: []NodeResult{{Node: "a", Status: Fail}, {Node: "b", Status: Error}}, want: Error},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := aggregateNodeResults(test.in); got.Status != test.want {
				t.Fatalf("got %s, want %s", got.Status, test.want)
			}
		})
	}
}

func TestValidatorDaemonSetSecurityAndScope(t *testing.T) {
	ds := validatorDaemonSet("test", map[string]string{"run": "one"}, map[string]string{"role": "control-plane"}, "image@sha256:deadbeef", "certificates", []validatorMount{{"tls", "/tls", "/host/tls"}})
	pod := ds.Spec.Template.Spec
	if pod.NodeSelector["role"] != "control-plane" || len(pod.InitContainers) != 1 || len(pod.Volumes) != 1 {
		t.Fatalf("unexpected validator pod: %+v", pod)
	}
	container := pod.InitContainers[0]
	if container.SecurityContext.Privileged == nil || *container.SecurityContext.Privileged || !container.VolumeMounts[0].ReadOnly {
		t.Fatalf("validator is not read-only and unprivileged: %+v", container.SecurityContext)
	}
	if container.ImagePullPolicy != "IfNotPresent" {
		t.Fatalf("got pull policy %s", container.ImagePullPolicy)
	}
}

func TestNodeValidatorTimeoutCleansUp(t *testing.T) {
	core := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}})
	env := NewEnvironment(core, nil, nil)
	env.ValidatorImage = "image@sha256:deadbeef"
	env.Timeout = 10 * time.Millisecond
	result := runNodeValidator(context.Background(), env, "network-config", nil, []validatorMount{{"oem", "/oem", "/host/oem"}})
	if result.Status != Error {
		t.Fatalf("got %+v, want timeout error", result)
	}
	daemonSets, err := core.AppsV1().DaemonSets(validatorNamespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(daemonSets.Items) != 0 {
		t.Fatalf("validator DaemonSet was not cleaned up: %+v", daemonSets.Items)
	}
}
