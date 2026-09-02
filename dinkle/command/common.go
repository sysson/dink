package command

import (
	"context"
	"errors"
	"fmt"
	"sync"

	pkgcerts "github.com/sysson/dink/pkg/certs"
	"github.com/sysson/dink/pkg/k8s"
	"github.com/sysson/dink/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// Options holds the flags shared by every dinkle subcommand.
type Options struct {
	kubeConfig string
	certsDir   string
	once       sync.Once
	kubernetes.Interface
}

func (o *Options) kubeClient(ctx context.Context) (kubernetes.Interface, error) {
	o.once.Do(func() {
		var err error
		o.Interface, err = k8s.New(ctx, o.kubeConfig)
		if err != nil {
			return
		}
	})
	if o.Interface == nil {
		return nil, fmt.Errorf("failed to create kube client")
	}
	return o.Interface, nil
}

// loadAuthority reloads the CA cached by a previous `dinkle bootstrap` run.
// The ServiceName/Namespace only matter for IssueServer, so placeholders are
// fine here: tenant and client certificates never consult them.
func loadAuthority(dir string) (*pkgcerts.Authority, error) {
	ca, err := pkgcerts.LoadAuthorityDir(dir, pkgcerts.Options{
		ServiceName: defaultService,
		Namespace:   types.DefaultSystemNamespace,
	})
	if err != nil {
		if errors.Is(err, pkgcerts.ErrNoAuthority) {
			return nil, fmt.Errorf("no CA cached in %s; run `dinkle bootstrap` first: %w", dir, err)
		}
		return nil, err
	}
	return ca, nil
}

func clientSecretName(clientName string) string {
	return "dink-client-" + clientName
}
