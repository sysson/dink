package translator

import (
	"context"
	"encoding/json"
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

// Swarm secrets map to Kubernetes Secrets in the tenant namespace. This is
// unrelated to Dink's secrets plugin, which resolves se:// environment values.

func (s *Swarm) GetSecrets(ctx context.Context, options swarmbackend.SecretListOptions) ([]swarmtypes.Secret, error) {
	namespace, err := s.namespace(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateSwarmObjectFilters(options.Filters); err != nil {
		return nil, InvalidArgument(err)
	}
	secrets, err := s.k8s.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{LabelSelector: swarmSecretSelector})
	if err != nil {
		return nil, kubeError(err)
	}
	result := make([]swarmtypes.Secret, 0, len(secrets.Items))
	for index := range secrets.Items {
		secret, err := swarmSecret(&secrets.Items[index])
		if err != nil {
			return nil, err
		}
		if !matchesSwarmObjectFilters(options.Filters, secret.ID, secret.Spec.Name, secret.Spec.Labels) {
			continue
		}
		result = append(result, secret)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Spec.Name < result[j].Spec.Name })
	return result, nil
}

func (s *Swarm) GetSecret(ctx context.Context, idOrName string) (swarmtypes.Secret, error) {
	object, err := s.findSecret(ctx, idOrName)
	if err != nil {
		return swarmtypes.Secret{}, err
	}
	return swarmSecret(object)
}

func (s *Swarm) CreateSecret(ctx context.Context, spec swarmtypes.SecretSpec) (string, error) {
	namespace, err := s.namespace(ctx)
	if err != nil {
		return "", err
	}
	if err := validateSwarmName("secret", spec.Name); err != nil {
		return "", err
	}
	if spec.Driver != nil {
		return "", Unsupported(fmt.Errorf("external secret drivers are not supported by the Swarm endpoints"))
	}
	if spec.Templating != nil {
		return "", Unsupported(fmt.Errorf("secret templating is not supported: Kubernetes Secrets are mounted verbatim"))
	}
	if len(spec.Data) == 0 {
		return "", InvalidArgument(fmt.Errorf("secret data is required"))
	}
	annotations, err := swarmSpecAnnotations(swarmtypes.SecretSpec{Annotations: spec.Annotations})
	if err != nil {
		return "", err
	}
	created, err := s.k8s.CoreV1().Secrets(namespace).Create(ctx, &corev1.Secret{
		Name:        spec.Name,
		Namespace:   namespace,
		Labels:      map[string]string{swarmKindLabel: swarmSecretKind},
		Annotations: annotations,
		Data:        map[string][]byte{swarmPayloadKey: spec.Data},
	}, metav1.CreateOptions{})
	if err != nil {
		return "", kubeError(err)
	}
	return identity.DockerIDFromUID(created.UID), nil
}

func (s *Swarm) UpdateSecret(ctx context.Context, idOrName string, version uint64, spec swarmtypes.SecretSpec) error {
	object, err := s.findSecret(ctx, idOrName)
	if err != nil {
		return err
	}
	if len(spec.Data) > 0 {
		return InvalidArgument(fmt.Errorf("only secret labels can be updated"))
	}
	if spec.Name != "" && spec.Name != object.Name {
		return InvalidArgument(fmt.Errorf("renaming a secret is not supported"))
	}
	annotations, err := swarmSpecAnnotations(swarmtypes.SecretSpec{Name: object.Name, Labels: spec.Labels})
	if err != nil {
		return err
	}
	object.Annotations = annotations
	object.ResourceVersion = resourceVersionFor(version)
	if _, err := s.k8s.CoreV1().Secrets(object.Namespace).Update(ctx, object, metav1.UpdateOptions{}); err != nil {
		return kubeError(err)
	}
	return nil
}

func (s *Swarm) RemoveSecret(ctx context.Context, idOrName string) error {
	object, err := s.findSecret(ctx, idOrName)
	if err != nil {
		return err
	}
	err = s.k8s.CoreV1().Secrets(object.Namespace).Delete(ctx, object.Name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return kubeError(err)
	}
	return nil
}

func (s *Swarm) findSecret(ctx context.Context, idOrName string) (*corev1.Secret, error) {
	namespace, err := s.namespace(ctx)
	if err != nil {
		return nil, err
	}
	if idOrName == "" {
		return nil, InvalidArgument(fmt.Errorf("secret ID or name is required"))
	}
	if secret, err := s.k8s.CoreV1().Secrets(namespace).Get(ctx, idOrName, metav1.GetOptions{}); err == nil {
		if secret.Labels[swarmKindLabel] == swarmSecretKind {
			return secret, nil
		}
	} else if !apierrors.IsNotFound(err) {
		return nil, kubeError(err)
	}
	secrets, err := s.k8s.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{LabelSelector: swarmSecretSelector})
	if err != nil {
		return nil, kubeError(err)
	}
	var match *corev1.Secret
	for index := range secrets.Items {
		if !strings.HasPrefix(identity.DockerIDFromUID(secrets.Items[index].UID), idOrName) {
			continue
		}
		if match != nil {
			return nil, Conflict(fmt.Errorf("secret %s is ambiguous", idOrName))
		}
		match = &secrets.Items[index]
	}
	if match == nil {
		return nil, NotFound(fmt.Errorf("secret %s not found", idOrName))
	}
	return match, nil
}

// swarmSecret never returns the payload, matching Docker, which only exposes
// secret values to the tasks that mount them.
func swarmSecret(object *corev1.Secret) (swarmtypes.Secret, error) {
	var spec swarmtypes.SecretSpec
	if err := decodeSwarmSpec(object.Annotations, &spec); err != nil {
		return swarmtypes.Secret{}, fmt.Errorf("decoding secret spec for %s: %w", object.Name, err)
	}
	spec.Name = object.Name
	spec.Data = nil
	if spec.Labels == nil {
		spec.Labels = map[string]string{}
	}
	return swarmtypes.Secret{
		ID:   identity.DockerIDFromUID(object.UID),
		Meta: swarmMeta(object),
		Spec: spec,
	}, nil
}

func swarmSpecAnnotations(spec any) (map[string]string, error) {
	encoded, err := json.Marshal(spec)
	if err != nil {
		return nil, InvalidArgument(fmt.Errorf("encoding spec: %w", err))
	}
	return map[string]string{swarmSpecAnnotation: string(encoded)}, nil
}

func decodeSwarmSpec(annotations map[string]string, target any) error {
	encoded := annotations[swarmSpecAnnotation]
	if encoded == "" {
		return nil
	}
	return json.Unmarshal([]byte(encoded), target)
}

func validateSwarmObjectFilters(filters swarmFilters) error {
	return filters.Validate(map[string]bool{"id": true, "label": true, "name": true, "names": true})
}

func matchesSwarmObjectFilters(filters swarmFilters, id, name string, labels map[string]string) bool {
	if filters.Len() == 0 {
		return true
	}
	if !filters.FuzzyMatch("id", id) {
		return false
	}
	if !filters.Match("name", name) {
		return false
	}
	if !filters.ExactMatch("names", name) {
		return false
	}
	return filters.MatchKVList("label", labels)
}
