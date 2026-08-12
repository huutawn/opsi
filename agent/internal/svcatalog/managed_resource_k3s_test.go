package svcatalog

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/opsi-dev/opsi/agent/internal/cloudrelay"
	resourcev1 "github.com/opsi-dev/opsi/contracts/go/resourcev1"
)

func TestManagedResourceRealK3sNATS(t *testing.T) {
	if os.Getenv("OPSI_E2E_K3S_NATS") != "1" {
		t.Skip("set OPSI_E2E_K3S_NATS=1 with KUBECONFIG pointing to a disposable K3s cluster")
	}
	spec := managedSpec(t)
	reconciler := ManagedResourceReconciler{Timeout: 5 * time.Minute, PollInterval: time.Second}
	apply := func(token string, spec resourcev1.ManagedResourceSpec) cloudrelay.ManagedResourceResult {
		return reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: token, Spec: spec})
	}
	result := apply("lease-1", spec)
	if result.Status != "ready" || result.Evidence == nil || result.Evidence.Image != resourcev1.NATSImage {
		t.Fatalf("first reconcile=%+v", result)
	}
	assertManagedK3sObjects(t, spec, 1)
	assertNATSConnection(t, spec)
	if replay := apply("lease-2", spec); replay.Status != "ready" {
		t.Fatalf("replay=%+v", replay)
	}
	assertManagedK3sObjects(t, spec, 1)
	if restarted := (ManagedResourceReconciler{Timeout: 5 * time.Minute, PollInterval: time.Second}).Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "apply", LeaseToken: "lease-3", Spec: spec}); restarted.Status != "ready" {
		t.Fatalf("restart reconcile=%+v", restarted)
	}
	assertManagedK3sObjects(t, spec, 1)
	deleted := reconciler.Reconcile(context.Background(), cloudrelay.ManagedResourceLease{Action: "delete", LeaseToken: "lease-4", Spec: spec})
	if deleted.Status != "deleted" {
		t.Fatalf("delete=%+v", deleted)
	}
	assertManagedK3sObjects(t, spec, 0)
}

func assertManagedK3sObjects(t *testing.T, spec resourcev1.ManagedResourceSpec, want int) {
	t.Helper()
	namespace := managedResourceNamespace(spec)
	selector := "opsi.dev/managed-resource-id=" + managedLabel(spec.ResourceID)
	for _, kind := range []string{"deployment", "service"} {
		out := kubectl(t, "get", kind, "-n", namespace, "-l", selector, "-o", "name")
		count := 0
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if line != "" {
				count++
			}
		}
		if count != want {
			t.Fatalf("%s count=%d want=%d output=%q", kind, count, want, out)
		}
	}
	if want == 1 {
		image := kubectl(t, "get", "pod", "-n", namespace, "-l", selector, "-o", "jsonpath={.items[0].status.containerStatuses[0].imageID}")
		if !imageMatches(image, spec.Image) {
			t.Fatalf("running image=%q want=%q", image, spec.Image)
		}
	}
}

func assertNATSConnection(t *testing.T, spec resourcev1.ManagedResourceSpec) {
	t.Helper()
	namespace := managedResourceNamespace(spec)
	name := "nats-client-" + strings.ToLower(time.Now().UTC().Format("150405"))
	const busyboxImage = "docker.io/library/busybox@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662"
	out := kubectl(t, "run", name, "-n", namespace, "--restart=Never", "--rm", "-i", "--image="+busyboxImage, "--command", "--", "sh", "-ec", "nc -w 5 "+spec.Connection.Host+" 4222 | head -n 1")
	if !strings.Contains(out, "INFO") {
		t.Fatalf("NATS connection output=%q", out)
	}
}

func kubectl(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "kubectl", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
