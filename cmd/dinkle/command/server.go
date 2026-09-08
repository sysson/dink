package command

import (
	"context"
	"fmt"
	"net"

	"github.com/sysson/dink/core/types"
	"github.com/urfave/cli/v3"
)

type serverOptions struct {
	systemNamespace  string
	caSecretName     string
	serverSecretName string
	serviceName      string
	clusterDomain    string
	extraDNSNames    []string
	extraIPs         []string
	validExtraIPs    []net.IP
	apply            bool
}

func serverCmd(o *Options) *cli.Command {
	return &cli.Command{
		Name:  "server",
		Usage: "Manage server certificates signed by dink's CA",
		Commands: []*cli.Command{
			serverIssueCmd(o),
		},
	}
}

func serverIssueCmd(o *Options) *cli.Command {
	s := &serverOptions{}
	return &cli.Command{
		Name:  "issue",
		Usage: "Issue a server certificate for a dink-managed service",
		Description: "Loads the existing CA from --certsDir or the cluster CA Secret, then\n" +
			"issues a server certificate for --serviceName and stores it locally and,\n" +
			"by default, in the cluster as --serverSecretName.",
		Flags: serverFlags(s),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return newService(o, &caOptions{}).IssueServer(ctx, s, cmd.Writer)
		},
	}
}

func serverFlags(s *serverOptions) []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:        "systemNamespace",
			Usage:       "dink's system namespace",
			Value:       defaultSystemNamespace,
			Sources:     cli.EnvVars(types.DefaultEnvPrefix + "_K8S_SYSTEM_NAMESPACE"),
			Destination: &s.systemNamespace,
		},
		&cli.StringFlag{
			Name:        "caSecretName",
			Usage:       "Name of the CA Secret",
			Value:       defaultCASecretName,
			Destination: &s.caSecretName,
		},
		&cli.StringFlag{
			Name:        "serverSecretName",
			Usage:       "Name of the server TLS Secret to write",
			Value:       defaultServerSecretName,
			Destination: &s.serverSecretName,
		},
		&cli.StringFlag{
			Name:        "serviceName",
			Usage:       "Service name used for the server certificate SANs",
			Value:       defaultServiceName,
			Destination: &s.serviceName,
		},
		&cli.StringFlag{
			Name:        "clusterDomain",
			Usage:       "Cluster DNS domain",
			Value:       "cluster.local",
			Destination: &s.clusterDomain,
		},
		&cli.StringSliceFlag{
			Name:        "dnsName",
			Usage:       "Additional DNS SAN for the server certificate; repeatable",
			Destination: &s.extraDNSNames,
		},
		&cli.StringSliceFlag{
			Name:        "ip",
			Usage:       "Additional IP SAN for the server certificate; repeatable",
			Destination: &s.extraIPs,
		},
		&cli.BoolFlag{
			Name:        "apply",
			Usage:       "Apply the server keypair to the cluster as a Secret",
			Value:       true,
			Destination: &s.apply,
		},
	}
}

func (s *serverOptions) validate() error {
	if s.systemNamespace == "" {
		return fmt.Errorf("systemNamespace is required")
	}
	if s.caSecretName == "" {
		return fmt.Errorf("caSecretName is required")
	}
	if s.serverSecretName == "" {
		return fmt.Errorf("serverSecretName is required")
	}
	if s.serviceName == "" {
		return fmt.Errorf("serviceName is required")
	}
	if s.clusterDomain == "" {
		return fmt.Errorf("clusterDomain is required")
	}
	ips := make([]net.IP, 0, len(s.extraIPs))
	for _, raw := range s.extraIPs {
		ip := net.ParseIP(raw)
		if ip == nil {
			return fmt.Errorf("invalid IP address %q", raw)
		}
		ips = append(ips, ip)
	}
	s.validExtraIPs = ips
	return nil
}
