// Package command implements dinkle, the operator CLI that bootstraps dink's
// CA and Kubernetes namespaces and manages tenant and client certificates.
package command

import (
	"io"

	"github.com/sysson/dink/dinkle/certs"
	"github.com/sysson/dink/pkg/types"
	"github.com/urfave/cli/v3"
)

func New(stdout, stderr io.Writer) (*cli.Command, error) {
	defaultCertsDir, err := certs.DefaultDir()
	if err != nil {
		return nil, err
	}

	g := &globalOptions{certsDir: defaultCertsDir}

	return &cli.Command{
		Name:  "dinkle",
		Usage: "Bootstrap dink and manage tenants and client certificates",
		Description: "dinkle bootstraps dink's certificate authority and Kubernetes namespaces,\n" +
			"then manages the per-tenant namespaces and client certificates used to\n" +
			"authenticate docker clients against dink.",
		Writer:    stdout,
		ErrWriter: stderr,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "kubeconfig",
				Usage:       "Path to a kubeconfig; defaults to the ambient cluster configuration",
				Sources:     cli.EnvVars("KUBECONFIG"),
				Destination: &g.kubeConfig,
			},
			&cli.StringFlag{
				Name:        "certsDir",
				Usage:       "Directory used to cache the CA and issued certificates",
				Value:       defaultCertsDir,
				Sources:     cli.EnvVars(types.DefaultEnvPrefix + "_CERTS_DIR"),
				Destination: &g.certsDir,
			},
		},
		Commands: []*cli.Command{
			bootstrapCmd(g),
			tenantCmd(g),
			clientCmd(g),
		},
	}, nil
}
