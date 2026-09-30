package translator

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	managedByLabel = "app.kubernetes.io/managed-by"
	managedByDink  = "dink"

	// namespaceIsolationPolicy denies ingress that no other policy allows, which
	// is what makes the per-network policies an isolation boundary.
	namespaceIsolationPolicy = "dink-default-deny"
	networkPolicyPrefix      = "dink-network-"
	publishedPolicyPrefix    = "dink-published-"
)

// dinkPodLabels marks a Pod as Dink-managed so the isolation policies can
// select it without catching an operator's own workloads.
func dinkPodLabels(name string) map[string]string {
	return map[string]string{"app": name, managedByLabel: managedByDink}
}

// ensureNamespaceIsolation denies ingress to Dink Pods by default. Policies are
// additive, so this is what the per-network allow rules are carved out of.
func (d *Docker) ensureNamespaceIsolation(ctx context.Context, namespace string) error {
	policies := d.k8s.NetworkingV1().NetworkPolicies(namespace)
	if _, err := policies.Get(ctx, namespaceIsolationPolicy, metav1.GetOptions{}); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return kubeError(err)
	}
	policy := &networkingv1.NetworkPolicy{
		Name:      namespaceIsolationPolicy,
		Namespace: namespace,
		Labels:    map[string]string{managedByLabel: managedByDink},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{managedByLabel: managedByDink}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		},
	}
	if _, err := policies.Create(ctx, policy, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return kubeError(err)
	}
	return nil
}

// networkHasPolicy reports whether a Docker network can be expressed as a
// NetworkPolicy. `none` needs no allow rule and `host` Pods bypass policy.
func networkHasPolicy(name string) bool {
	return name != "none" && name != "host"
}

// ensureNetworkPolicy lets the members of a Docker network reach each other.
// Membership is a Pod label, so the policy never changes as containers connect
// and disconnect.
func (d *Docker) ensureNetworkPolicy(ctx context.Context, obj *unstructured.Unstructured) error {
	if !networkHasPolicy(networkFromObject(obj).Name) {
		return nil
	}
	namespace, objectName := obj.GetNamespace(), obj.GetName()
	membership := map[string]string{networkLabelPrefix + objectName: "true"}
	policy := &networkingv1.NetworkPolicy{
		Name:      networkPolicyPrefix + objectName,
		Namespace: namespace,
		Labels:    map[string]string{managedByLabel: managedByDink},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: networkResource.GroupVersion().String(),
			Kind:       "DockerNetwork",
			Name:       objectName,
			UID:        obj.GetUID(),
		}},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: membership},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					PodSelector: &metav1.LabelSelector{MatchLabels: membership},
				}},
			}},
		},
	}
	policies := d.k8s.NetworkingV1().NetworkPolicies(namespace)
	if _, err := policies.Create(ctx, policy, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return kubeError(err)
	}
	return nil
}

// applyPublishedPortsPolicy opens a workload's published ports to any source.
// Traffic through a NodePort or LoadBalancer usually arrives translated to a
// node address, so no Pod selector can match it.
func (d *Docker) applyPublishedPortsPolicy(ctx context.Context, namespace, name string, owner metav1.OwnerReference, ports []corev1.ContainerPort) error {
	policies := d.k8s.NetworkingV1().NetworkPolicies(namespace)
	policyName := publishedPolicyPrefix + name
	if len(ports) == 0 {
		err := policies.Delete(ctx, policyName, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return kubeError(err)
		}
		return nil
	}
	policy := &networkingv1.NetworkPolicy{
		Name:            policyName,
		Namespace:       namespace,
		Labels:          map[string]string{managedByLabel: managedByDink},
		OwnerReferences: []metav1.OwnerReference{owner},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			// An ingress rule with no peers allows every source.
			Ingress: []networkingv1.NetworkPolicyIngressRule{{Ports: networkPolicyPorts(ports)}},
		},
	}
	existing, err := policies.Get(ctx, policyName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := policies.Create(ctx, policy, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return kubeError(err)
		}
		return nil
	}
	if err != nil {
		return kubeError(err)
	}
	policy.ResourceVersion = existing.ResourceVersion
	if _, err := policies.Update(ctx, policy, metav1.UpdateOptions{}); err != nil {
		return kubeError(err)
	}
	return nil
}

func networkPolicyPorts(ports []corev1.ContainerPort) []networkingv1.NetworkPolicyPort {
	result := make([]networkingv1.NetworkPolicyPort, 0, len(ports))
	for _, port := range ports {
		protocol := port.Protocol
		if protocol == "" {
			protocol = corev1.ProtocolTCP
		}
		value := intstr.FromInt32(port.ContainerPort)
		result = append(result, networkingv1.NetworkPolicyPort{Protocol: &protocol, Port: &value})
	}
	return result
}

func deploymentOwnerReference(name string, uid types.UID) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: name, UID: uid, Controller: new(true)}
}
