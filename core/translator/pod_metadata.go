package translator

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	podLabelPrefix      = "dink.io/pod-label/"
	podAnnotationPrefix = "dink.io/pod-annotation/"
)

// Later label sets override earlier ones, but every requested key is validated.
func podMetadata(labelSets ...map[string]string) (metav1.ObjectMeta, error) {
	meta := metav1.ObjectMeta{Labels: map[string]string{}, Annotations: map[string]string{}}
	for _, labels := range labelSets {
		for _, source := range slices.Sorted(maps.Keys(labels)) {
			var target map[string]string
			var key string
			switch {
			case strings.HasPrefix(source, podLabelPrefix):
				key = strings.TrimPrefix(source, podLabelPrefix)
				target = meta.Labels
			case strings.HasPrefix(source, podAnnotationPrefix):
				key = strings.TrimPrefix(source, podAnnotationPrefix)
				target = meta.Annotations
			default:
				continue
			}
			if problems := validation.IsQualifiedName(key); len(problems) > 0 {
				return metav1.ObjectMeta{}, InvalidArgument(fmt.Errorf("invalid Pod metadata key in Docker label %q: %s", source, strings.Join(problems, "; ")))
			}
			if reservedPodMetadataKey(key) {
				return metav1.ObjectMeta{}, InvalidArgument(fmt.Errorf("Docker label %q targets reserved Pod metadata key %q", source, key))
			}
			if strings.HasPrefix(source, podLabelPrefix) {
				if problems := validation.IsValidLabelValue(labels[source]); len(problems) > 0 {
					return metav1.ObjectMeta{}, InvalidArgument(fmt.Errorf("invalid Pod label value for %q: %s", source, strings.Join(problems, "; ")))
				}
			}
			target[key] = labels[source]
		}
	}
	if err := validatePodAnnotations(meta.Annotations); err != nil {
		return metav1.ObjectMeta{}, err
	}
	return meta, nil
}

func reservedPodMetadataKey(key string) bool {
	domain, _, qualified := strings.Cut(key, "/")
	return key == "app" || key == managedByLabel ||
		(qualified && (domain == "dink.io" || strings.HasSuffix(domain, ".dink.io")))
}

func validatePodAnnotations(annotations map[string]string) error {
	if err := apivalidation.ValidateAnnotationsSize(annotations); err != nil {
		return InvalidArgument(fmt.Errorf("invalid Pod annotations: %w", err))
	}
	return nil
}
