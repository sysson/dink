package translator

import (
	"context"
	"fmt"
	"strings"

	networktypes "github.com/moby/moby/api/types/network"
	"github.com/sysson/dink/core/identity"
	"github.com/sysson/syskit/httpx"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func (d *Docker) CreateNetwork(ctx context.Context, request networktypes.CreateRequest) (networktypes.CreateResponse, error) {
	if strings.TrimSpace(request.Name) == "" {
		return networktypes.CreateResponse{}, httpx.BadRequest(fmt.Errorf("network name is required"))
	}
	if (request.Driver != "" && request.Driver != "bridge") || (request.Scope != "" && request.Scope != "local") ||
		(request.EnableIPv4 != nil && !*request.EnableIPv4) || (request.EnableIPv6 != nil && *request.EnableIPv6) ||
		request.Internal || request.Attachable || request.Ingress || request.ConfigOnly || request.ConfigFrom != nil || len(request.Options) > 0 ||
		(request.IPAM != nil && (request.IPAM.Driver != "" && request.IPAM.Driver != "default" || len(request.IPAM.Options) > 0 || len(request.IPAM.Config) > 0)) {
		return networktypes.CreateResponse{}, httpx.BadRequest(fmt.Errorf("network configuration is not supported by the Kubernetes backend"))
	}
	namespace, err := networkNamespace(ctx)
	if err != nil {
		return networktypes.CreateResponse{}, err
	}
	list, err := d.networkList(ctx)
	if err != nil {
		return networktypes.CreateResponse{}, err
	}
	for index := range list.Items {
		if networkFromObject(&list.Items[index]).Name == request.Name {
			return networktypes.CreateResponse{}, httpx.Conflict(fmt.Errorf("network with name %s already exists", request.Name))
		}
	}
	labels := make(map[string]any, len(request.Labels))
	for key, value := range request.Labels {
		labels[key] = value
	}
	obj, err := d.k8s.Dynamic.Resource(networkResource).Namespace(namespace).Create(ctx, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "dink.io/v1alpha1",
		"kind":       "DockerNetwork",
		"metadata":   map[string]any{"name": networkObjectName(request.Name)},
		"spec":       map[string]any{"name": request.Name, "labels": labels},
	}}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return networktypes.CreateResponse{}, httpx.Conflict(fmt.Errorf("network with name %s already exists", request.Name))
	}
	if err != nil {
		return networktypes.CreateResponse{}, err
	}
	return networktypes.CreateResponse{ID: identity.DockerIDFromUID(obj.GetUID()), Warning: ""}, nil
}

func (s *Swarm) CreateNetwork(context.Context, networktypes.CreateRequest) (string, error) {
	return "", ErrNotImplemented
}
