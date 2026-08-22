package auth

import (
	"fmt"
)

type AuthPlugin struct {
	Name string
	Path string
}

type AuthChain struct {
	plugins []plugin
}

func NewChain(plugins []AuthPlugin) (*AuthChain, error) {
	c := &AuthChain{}
	for _, p := range plugins {
		built, err := newPlugin(p)
		if err != nil {
			return nil, fmt.Errorf("configuring auth plugin %q: %w", p.Name, err)
		}
		c.plugins = append(c.plugins, built)
	}
	return c, nil
}

func (c *AuthChain) Len() int {
	if c == nil {
		return 0
	}
	return len(c.plugins)
}
