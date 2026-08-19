package auth

import (
	"context"
	"fmt"

	"github.com/sysson/dink/internal/config"
	authv1 "github.com/sysson/dink/sdk/auth/v1"
)

type Chain struct {
	plugins []plugin
}

func NewChain(plugins []config.AuthPlugin) (*Chain, error) {
	c := &Chain{}
	for _, p := range plugins {
		built, err := newPlugin(p)
		if err != nil {
			return nil, fmt.Errorf("configuring auth plugin %q: %w", p.Name, err)
		}
		c.plugins = append(c.plugins, built)
	}
	return c, nil
}

func (c *Chain) Len() int {
	if c == nil {
		return 0
	}
	return len(c.plugins)
}

func (c *Chain) AuthZReq(ctx context.Context, req *authv1.AuthZReqRequest) (*authv1.AuthZReqResponse, error) {
	for _, p := range c.plugins {
		resp, err := p.client.AuthZReq(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("auth plugin %q: %w", p.name, err)
		}
		if !resp.GetAllow() {
			return resp, nil
		}
	}
	return &authv1.AuthZReqResponse{Allow: true}, nil
}

func (c *Chain) AuthZRes(ctx context.Context, req *authv1.AuthZResRequest) (*authv1.AuthZResResponse, error) {
	for _, p := range c.plugins {
		resp, err := p.client.AuthZRes(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("auth plugin %q: %w", p.name, err)
		}
		if !resp.GetAllow() {
			return resp, nil
		}
	}
	return &authv1.AuthZResResponse{Allow: true}, nil
}
