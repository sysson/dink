package translator

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	volumetypes "github.com/moby/moby/api/types/volume"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"

	"github.com/sysson/dink/core/identity"
	"github.com/sysson/dink/core/k8s"
	"github.com/sysson/dink/core/plugins"
	"github.com/sysson/dink/sdk/plugin"
	"github.com/sysson/dink/sdk/volumes"
)

type fakeDriver struct {
	mu      sync.Mutex
	claim   volumes.Claim
	created []volumes.Request
	removed []string
	failRm  bool
}

func (f *fakeDriver) Create(_ context.Context, req *volumes.Request) (volumes.Claim, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if req.Options["tier"] == "bogus" {
		return volumes.Claim{}, errors.New("unknown tier")
	}
	f.created = append(f.created, *req)
	return f.claim, nil
}

func (f *fakeDriver) Remove(_ context.Context, req *volumes.Request) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failRm {
		return errors.New("backend busy")
	}
	f.removed = append(f.removed, req.Name)
	return nil
}

func newVolumePluginDocker(t *testing.T, driver *fakeDriver) (context.Context, *Docker, *kubernetesfake.Clientset) {
	t.Helper()
	h, err := plugin.Handler(plugin.Info{Name: "fast"}, volumes.Plugin(driver))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	reg, err := plugins.New("dink-system", []plugins.Static{{Name: "fast", Types: []plugins.Type{plugins.TypeVolumes}, Endpoint: srv.URL}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := kubernetesfake.NewClientset()
	ctx := identity.NewContext(t.Context(), identity.Identity{Namespace: "tenant", CommonName: "alice"})
	return ctx, &Docker{k8s: &k8s.KubeClient{Interface: client}, plugins: reg}, client
}

func TestPluginVolumeLifecycle(t *testing.T) {
	driver := &fakeDriver{claim: volumes.Claim{
		StorageClassName: "fast-ssd",
		Size:             "5Gi",
		AccessModes:      []string{"ReadWriteMany"},
		Annotations:      map[string]string{"example.com/tier": "gold"},
	}}
	ctx, docker, client := newVolumePluginDocker(t, driver)

	created, err := docker.CreateVolume(ctx, volumetypes.CreateRequest{Name: "db", Driver: "fast", DriverOpts: map[string]string{"tier": "gold"}})
	if err != nil {
		t.Fatal(err)
	}
	if created.Driver != "fast" || created.Options["tier"] != "gold" {
		t.Fatalf("created volume = %+v", created)
	}
	if len(driver.created) != 1 || driver.created[0].Namespace != "tenant" || driver.created[0].IdentityName != "alice" {
		t.Fatalf("driver saw %+v", driver.created)
	}
	pvc, err := client.CoreV1().PersistentVolumeClaims("tenant").Get(ctx, "db", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if *pvc.Spec.StorageClassName != "fast-ssd" || pvc.Spec.AccessModes[0] != corev1.ReadWriteMany ||
		pvc.Spec.Resources.Requests.Storage().String() != "5Gi" || pvc.Annotations["example.com/tier"] != "gold" {
		t.Fatalf("unexpected claim: %+v", pvc)
	}

	if _, err := docker.CreateVolume(ctx, volumetypes.CreateRequest{Name: "db", Driver: "fast"}); err != nil || len(driver.created) != 1 {
		t.Fatalf("re-creating a volume must not provision again: %v, %d calls", err, len(driver.created))
	}
	if _, err := docker.CreateVolume(ctx, volumetypes.CreateRequest{Name: "db"}); !IsKind(err, KindConflict) {
		t.Fatalf("expected a driver mismatch to conflict, got %v", err)
	}

	if err := docker.RemoveVolume(ctx, "db", false); err != nil {
		t.Fatal(err)
	}
	if len(driver.removed) != 1 || driver.removed[0] != "db" {
		t.Fatalf("driver removals = %v", driver.removed)
	}
	if _, err := client.CoreV1().PersistentVolumeClaims("tenant").Get(ctx, "db", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("claim should be deleted")
	}
}

func TestPluginVolumeRejections(t *testing.T) {
	driver := &fakeDriver{claim: volumes.Claim{Annotations: map[string]string{"dink.io/docker-volume-name": "spoofed"}}}
	ctx, docker, _ := newVolumePluginDocker(t, driver)

	if _, err := docker.CreateVolume(ctx, volumetypes.CreateRequest{Name: "a", Driver: "fast"}); !IsKind(err, KindInvalidArgument) {
		t.Fatalf("expected reserved annotation to be rejected, got %v", err)
	}
	driver.claim = volumes.Claim{AccessModes: []string{"Everything"}}
	if _, err := docker.CreateVolume(ctx, volumetypes.CreateRequest{Name: "a", Driver: "fast"}); !IsKind(err, KindInvalidArgument) {
		t.Fatalf("expected invalid access mode to be rejected, got %v", err)
	}
	driver.claim = volumes.Claim{}
	if _, err := docker.CreateVolume(ctx, volumetypes.CreateRequest{Name: "a", Driver: "fast", DriverOpts: map[string]string{"tier": "bogus"}}); !IsKind(err, KindInvalidArgument) {
		t.Fatalf("expected the driver's rejection to be an invalid argument, got %v", err)
	}
	if _, err := docker.CreateVolume(ctx, volumetypes.CreateRequest{Name: "a", Driver: "missing"}); !IsKind(err, KindUnsupported) {
		t.Fatalf("expected an unknown driver to be unsupported, got %v", err)
	}
}

func TestPluginVolumeRemoveFailure(t *testing.T) {
	driver := &fakeDriver{failRm: true}
	ctx, docker, _ := newVolumePluginDocker(t, driver)
	if _, err := docker.CreateVolume(ctx, volumetypes.CreateRequest{Name: "db", Driver: "fast"}); err != nil {
		t.Fatal(err)
	}
	if err := docker.RemoveVolume(ctx, "db", false); err == nil {
		t.Fatal("a driver that cannot release the volume must keep it")
	}
	if err := docker.RemoveVolume(ctx, "db", true); err != nil {
		t.Fatalf("force must delete the claim anyway: %v", err)
	}
}

func TestContainerMountUsesVolumeDriver(t *testing.T) {
	driver := &fakeDriver{claim: volumes.Claim{Size: "2Gi"}}
	ctx, docker, _ := newVolumePluginDocker(t, driver)

	_, _, _, err := docker.containerVolumes(ctx, &container.Config{}, &container.HostConfig{
		Mounts: []mount.Mount{{
			Type:   mount.TypeVolume,
			Source: "data",
			Target: "/data",
			VolumeOptions: &mount.VolumeOptions{DriverConfig: &mount.Driver{
				Name:    "fast",
				Options: map[string]string{"tier": "silver"},
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(driver.created) != 1 || driver.created[0].Name != "data" || driver.created[0].Options["tier"] != "silver" {
		t.Fatalf("driver saw %+v", driver.created)
	}

	_, _, _, err = docker.containerVolumes(ctx, &container.Config{}, &container.HostConfig{VolumeDriver: "fast", Binds: []string{"logs:/logs"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(driver.created) != 2 || driver.created[1].Name != "logs" {
		t.Fatalf("--volume-driver should apply to binds, driver saw %+v", driver.created)
	}
}
