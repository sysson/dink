package plugins

import (
	"errors"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var Resource = schema.GroupVersionResource{Group: "dink.io", Version: "v1alpha1", Resource: "plugins"}

const Kind = "Plugin"

type Spec struct {
	Types   []Type     `json:"types"`
	Service ServiceRef `json:"service"`
	Timeout string     `json:"timeout,omitempty"`
	// Insecure calls the plugin over plaintext HTTP instead of mutual TLS.
	Insecure bool `json:"insecure,omitempty"`
}

type ServiceRef struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}

type Status struct {
	Ready              bool   `json:"ready"`
	Version            string `json:"version,omitempty"`
	Message            string `json:"message,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
}

// Object returns the Plugin resource for spec.
func (s Spec) Object(namespace, name string) (*unstructured.Unstructured, error) {
	spec, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&s)
	if err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": Resource.GroupVersion().String(),
		"kind":       Kind,
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"spec":       spec,
	}}, nil
}

// Endpoint is always a Service in the registration's own namespace, so a tenant
// cannot point dink at another tenant's plugin or an arbitrary host.
func (s Spec) Endpoint(namespace string) string {
	scheme := "https"
	if s.Insecure {
		scheme = "http"
	}
	return fmt.Sprintf("%s://%s.%s.svc:%d", scheme, s.Service.Name, namespace, s.Service.Port)
}

func (s Spec) timeout() (time.Duration, error) {
	if s.Timeout == "" {
		return defaultTimeout, nil
	}
	d, err := time.ParseDuration(s.Timeout)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid spec.timeout %q", s.Timeout)
	}
	return d, nil
}

func SpecFromObject(u *unstructured.Unstructured) (Spec, error) {
	var s Spec
	rawSpec, _, _ := unstructured.NestedMap(u.Object, "spec")
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rawSpec, &s); err != nil {
		return Spec{}, fmt.Errorf("decoding spec: %w", err)
	}
	if s.Service.Name == "" || s.Service.Port < 1 || s.Service.Port > 65535 {
		return Spec{}, errors.New("spec.service needs a name and a port between 1 and 65535")
	}
	return s, nil
}

func StatusFromObject(u *unstructured.Unstructured) Status {
	var s Status
	raw, _, _ := unstructured.NestedMap(u.Object, "status")
	_ = runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &s)
	return s
}
