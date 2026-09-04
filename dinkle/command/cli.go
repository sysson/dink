// Package command implements dinkle, the operator CLI that bootstraps dink's
// CA and Kubernetes namespaces and manages tenant and client certificates.
package command

import (
	"io"

	"github.com/sysson/dink/pkg/types"
	"github.com/urfave/cli/v3"
)

func New(stdout, stderr io.Writer) (*cli.Command, error) {
	defaultCertsDir, err := defaultDir()
	if err != nil {
		return nil, err
	}

	o := &Options{certsDir: defaultCertsDir}

	return &cli.Command{
		Name:  "dinkle",
		Usage: "Manage dink's certificate authority, tenants and client certificates",
		Description: "dinkle generates and rotates dink's certificate authority, and manages\n" +
			"the per-tenant namespaces and client certificates used to authenticate\n" +
			"docker clients against dink. The CA is stored both locally and as a\n" +
			"cluster Secret so that `dinkle` commands give consistent results\n" +
			"regardless of which host they run on; see `dinkle ca sync`.",
		Writer:    stdout,
		ErrWriter: stderr,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "kubeconfig",
				Usage:       "Path to a kubeconfig; defaults to the ambient cluster configuration",
				Sources:     cli.EnvVars("KUBECONFIG"),
				Destination: &o.kubeConfig,
			},
			&cli.StringFlag{
				Name:        "certsDir",
				Usage:       "Directory used to cache the CA and issued certificates",
				Value:       defaultCertsDir,
				Sources:     cli.EnvVars(types.DefaultEnvPrefix + "_CERTS_DIR"),
				Destination: &o.certsDir,
			},
		},
		Commands: []*cli.Command{
			caCmd(o),
			tenantCmd(o),
			clientCmd(o),
		},
	}, nil
}
