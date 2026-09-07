package registry

import (
	"github.com/docker/oci"
	"github.com/docker/oci/ociserver"
)

type Registry struct {
	*ociserver.Server
}

func New(pers oci.Interface, cfg *ociserver.ServerConfig) (*Registry, error) {
	svr, err := ociserver.New(pers, cfg)
	if err != nil {
		return nil, err
	}
	return &Registry{
		Server: svr,
	}, nil
}
