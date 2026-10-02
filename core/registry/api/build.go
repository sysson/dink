package api

import (
	"context"

	registryv1 "github.com/sysson/dink/core/registry/api/v1"
)

type BuildCredential struct {
	Username   string
	Password   string
	References []string
}

func (c *Client) IssueBuildCredential(ctx context.Context, names []string) (BuildCredential, error) {
	id, err := c.requestIdentity(ctx)
	if err != nil {
		return BuildCredential{}, err
	}
	response, err := c.rpc.IssueBuildCredential(ctx, &registryv1.IssueBuildCredentialRequest{Identity: id, Names: names})
	if err != nil {
		return BuildCredential{}, FromConnectError(err)
	}
	return BuildCredential{Username: response.GetUsername(), Password: response.GetPassword(), References: response.GetReferences()}, nil
}

func (c *Client) RevokeBuildCredential(ctx context.Context, username string) error {
	id, err := c.requestIdentity(ctx)
	if err != nil {
		return err
	}
	_, err = c.rpc.RevokeBuildCredential(ctx, &registryv1.RevokeBuildCredentialRequest{Identity: id, Username: username})
	return FromConnectError(err)
}
