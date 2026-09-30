package translator

import (
	"context"
	"fmt"
	"sort"
	"strings"

	swarmtypes "github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/v2/daemon/server/swarmbackend"
	"github.com/sysson/dink/core/identity"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Swarm configs map to Kubernetes ConfigMaps in the tenant namespace.

func (s *Swarm) GetConfigs(ctx context.Context, options swarmbackend.ConfigListOptions) ([]swarmtypes.Config, error) {
	namespace, err := s.namespace(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateSwarmObjectFilters(options.Filters); err != nil {
		return nil, InvalidArgument(err)
	}
	configs, err := s.k8s.CoreV1().ConfigMaps(namespace).List(ctx, metav1.ListOptions{LabelSelector: swarmConfigSelector})
	if err != nil {
		return nil, kubeError(err)
	}
	result := make([]swarmtypes.Config, 0, len(configs.Items))
	for index := range configs.Items {
		config, err := swarmConfig(&configs.Items[index])
		if err != nil {
			return nil, err
		}
		if !matchesSwarmObjectFilters(options.Filters, config.ID, config.Spec.Name, config.Spec.Labels) {
			continue
		}
		result = append(result, config)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Spec.Name < result[j].Spec.Name })
	return result, nil
}

func (s *Swarm) GetConfig(ctx context.Context, idOrName string) (swarmtypes.Config, error) {
	object, err := s.findConfig(ctx, idOrName)
	if err != nil {
		return swarmtypes.Config{}, err
	}
	return swarmConfig(object)
}

func (s *Swarm) CreateConfig(ctx context.Context, spec swarmtypes.ConfigSpec) (string, error) {
	namespace, err := s.namespace(ctx)
	if err != nil {
		return "", err
	}
	if err := validateSwarmName("config", spec.Name); err != nil {
		return "", err
	}
	if spec.Templating != nil {
		return "", Unsupported(fmt.Errorf("config templating is not supported: Kubernetes ConfigMaps are mounted verbatim"))
	}
	if len(spec.Data) == 0 {
		return "", InvalidArgument(fmt.Errorf("config data is required"))
	}
	annotations, err := swarmSpecAnnotations(swarmtypes.ConfigSpec{Annotations: spec.Annotations})
	if err != nil {
		return "", err
	}
	created, err := s.k8s.CoreV1().ConfigMaps(namespace).Create(ctx, &corev1.ConfigMap{
		Name:        spec.Name,
		Namespace:   namespace,
		Labels:      map[string]string{swarmKindLabel: swarmConfigKind},
		Annotations: annotations,
		BinaryData:  map[string][]byte{swarmPayloadKey: spec.Data},
	}, metav1.CreateOptions{})
	if err != nil {
		return "", kubeError(err)
	}
	return identity.DockerIDFromUID(created.UID), nil
}

func (s *Swarm) UpdateConfig(ctx context.Context, idOrName string, version uint64, spec swarmtypes.ConfigSpec) error {
	object, err := s.findConfig(ctx, idOrName)
	if err != nil {
		return err
	}
	if len(spec.Data) > 0 {
		return InvalidArgument(fmt.Errorf("only config labels can be updated"))
	}
	if spec.Name != "" && spec.Name != object.Name {
		return InvalidArgument(fmt.Errorf("renaming a config is not supported"))
	}
	annotations, err := swarmSpecAnnotations(swarmtypes.ConfigSpec{Name: object.Name, Labels: spec.Labels})
	if err != nil {
		return err
	}
	object.Annotations = annotations
	object.ResourceVersion = resourceVersionFor(version)
	if _, err := s.k8s.CoreV1().ConfigMaps(object.Namespace).Update(ctx, object, metav1.UpdateOptions{}); err != nil {
		return kubeError(err)
	}
	return nil
}

func (s *Swarm) RemoveConfig(ctx context.Context, idOrName string) error {
	object, err := s.findConfig(ctx, idOrName)
	if err != nil {
		return err
	}
	err = s.k8s.CoreV1().ConfigMaps(object.Namespace).Delete(ctx, object.Name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return kubeError(err)
	}
	return nil
}

func (s *Swarm) findConfig(ctx context.Context, idOrName string) (*corev1.ConfigMap, error) {
	namespace, err := s.namespace(ctx)
	if err != nil {
		return nil, err
	}
	if idOrName == "" {
		return nil, InvalidArgument(fmt.Errorf("config ID or name is required"))
	}
	if config, err := s.k8s.CoreV1().ConfigMaps(namespace).Get(ctx, idOrName, metav1.GetOptions{}); err == nil {
		if config.Labels[swarmKindLabel] == swarmConfigKind {
			return config, nil
		}
	} else if !apierrors.IsNotFound(err) {
		return nil, kubeError(err)
	}
	configs, err := s.k8s.CoreV1().ConfigMaps(namespace).List(ctx, metav1.ListOptions{LabelSelector: swarmConfigSelector})
	if err != nil {
		return nil, kubeError(err)
	}
	var match *corev1.ConfigMap
	for index := range configs.Items {
		if !strings.HasPrefix(identity.DockerIDFromUID(configs.Items[index].UID), idOrName) {
			continue
		}
		if match != nil {
			return nil, Conflict(fmt.Errorf("config %s is ambiguous", idOrName))
		}
		match = &configs.Items[index]
	}
	if match == nil {
		return nil, NotFound(fmt.Errorf("config %s not found", idOrName))
	}
	return match, nil
}

func swarmConfig(object *corev1.ConfigMap) (swarmtypes.Config, error) {
	var spec swarmtypes.ConfigSpec
	if err := decodeSwarmSpec(object.Annotations, &spec); err != nil {
		return swarmtypes.Config{}, fmt.Errorf("decoding config spec for %s: %w", object.Name, err)
	}
	spec.Name = object.Name
	if spec.Labels == nil {
		spec.Labels = map[string]string{}
	}
	// Configs are not secret, so Docker returns their payload on inspect.
	if data, ok := object.BinaryData[swarmPayloadKey]; ok {
		spec.Data = data
	} else if text, ok := object.Data[swarmPayloadKey]; ok {
		spec.Data = []byte(text)
	}
	return swarmtypes.Config{
		ID:   identity.DockerIDFromUID(object.UID),
		Meta: swarmMeta(object),
		Spec: spec,
	}, nil
}
