package command

import (
	"errors"
	"fmt"

	pkgcerts "github.com/sysson/dink/pkg/certs"
	"github.com/sysson/dink/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// globalOptions holds the flags shared by every dinkle subcommand.
type globalOptions struct {
	kubeConfig string
	certsDir   string
}

func (g *globalOptions) kubeClient() (kubernetes.Interface, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if g.kubeConfig != "" {
		rules.ExplicitPath = g.kubeConfig
	}
	restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, &clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}
	return kubernetes.NewForConfig(restConfig)
}

// loadAuthority reloads the CA cached by a previous `dinkle bootstrap` run.
// The ServiceName/Namespace only matter for IssueServer, so placeholders are
// fine here: tenant and client certificates never consult them.
func loadAuthority(g *globalOptions) (*pkgcerts.Authority, error) {
	ca, err := pkgcerts.LoadAuthorityDir(g.certsDir, pkgcerts.Options{
		ServiceName: defaultService,
		Namespace:   types.DefaultSystemNamespace,
	})
	if err != nil {
		if errors.Is(err, pkgcerts.ErrNoAuthority) {
			return nil, fmt.Errorf("no CA cached in %s; run `dinkle bootstrap` first: %w", g.certsDir, err)
		}
		return nil, err
	}
	return ca, nil
}

func clientSecretName(clientName string) string {
	return "dink-client-" + clientName
}
