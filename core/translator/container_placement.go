package translator

import (
	"github.com/sysson/dink/core/config"
	corev1 "k8s.io/api/core/v1"
)

// applyNodePlacement restricts a Pod to nodes that carry no tenant label or the
// tenant's own label, and tolerates the matching taint so operators can keep
// unrelated workloads off a dedicated node.
func applyNodePlacement(spec *corev1.PodSpec, placement config.NodePlacement, namespace string) {
	if !placement.IsEnabled() || namespace == "" {
		return
	}
	// Terms are OR'd: an unclaimed node, or one claimed by this tenant.
	selector := &corev1.NodeSelector{
		NodeSelectorTerms: []corev1.NodeSelectorTerm{
			{MatchExpressions: []corev1.NodeSelectorRequirement{{
				Key:      placement.LabelKey,
				Operator: corev1.NodeSelectorOpDoesNotExist,
			}}},
			{MatchExpressions: []corev1.NodeSelectorRequirement{{
				Key:      placement.LabelKey,
				Operator: corev1.NodeSelectorOpIn,
				Values:   []string{namespace},
			}}},
		},
	}
	if spec.Affinity == nil {
		spec.Affinity = &corev1.Affinity{}
	}
	if spec.Affinity.NodeAffinity == nil {
		spec.Affinity.NodeAffinity = &corev1.NodeAffinity{}
	}
	spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = selector
	if !placement.TolerationsEnabled() {
		return
	}
	spec.Tolerations = append(spec.Tolerations, corev1.Toleration{
		Key:      placement.LabelKey,
		Operator: corev1.TolerationOpEqual,
		Value:    namespace,
		Effect:   corev1.TaintEffectNoSchedule,
	})
}
