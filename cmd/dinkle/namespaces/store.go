package namespaces

import (
	"github.com/sysson/dink/cmd/dinkle/store"
	"k8s.io/client-go/kubernetes"
)

// NewSecretStore returns a pki.Store scoped to namespace, using dinkle's own
// Secret labels and field manager so every Secret it writes is recognizable
// as one dinkle owns. caSecretName is only consulted by SaveCA/LoadCA.
func NewSecretStore(client kubernetes.Interface, namespace, caSecretName string) *store.SecretStore {
	s := store.NewSecretStore(client, namespace, caSecretName)
	s.Labels = map[string]string{LabelManagedBy: ManagedByValue}
	s.LeafLabelKey = LabelLeaf
	s.FieldManager = CertFieldManager
	return s
}
