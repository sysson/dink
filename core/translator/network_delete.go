package translator

import (
	"context"
	"fmt"

	"github.com/sysson/syskit/httpx"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (d *Docker) DeleteNetwork(ctx context.Context, nameOrID string) error {
	obj, err := d.findNetwork(ctx, nameOrID)
	if err != nil {
		return err
	}
	if _, builtin := builtinNetworkDrivers[networkFromObject(obj).Name]; builtin {
		return httpx.Forbidden(fmt.Errorf("network %s is a predefined network and cannot be removed", nameOrID))
	}
	inUse, err := d.networkInUse(ctx, obj)
	if err != nil {
		return err
	}
	if inUse {
		return httpx.Conflict(fmt.Errorf("network %s has active endpoints", nameOrID))
	}
	err = d.k8s.Dynamic.Resource(networkResource).Namespace(obj.GetNamespace()).Delete(ctx, obj.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: new(obj.GetUID())}})
	if apierrors.IsNotFound(err) {
		return httpx.NotFound(fmt.Errorf("network %s not found", nameOrID))
	}
	return err
}
