package server

import (
	"context"
	"net/http"

	"github.com/sysson/dink/internal/k8s"
)

type Server struct {
	server     *http.Server
	kubeClient *k8s.KubeClient
}

func New(k *k8s.KubeClient) *Server {
	return &Server{
		kubeClient: k,
		server:     &http.Server{},
	}
}

func (s *Server) ListenAndServe() error {
	return s.server.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}
