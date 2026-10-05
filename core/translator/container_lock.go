package translator

import (
	"context"
	"fmt"
)

type containerOperationLock struct {
	token chan struct{}
	users int
}

// Key by immutable identity so names and IDs coordinate the same workload.
func (d *Docker) lockContainer(ctx context.Context, name string) (*containerWorkload, func(), error) {
	workload, err := d.findContainer(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	key := fmt.Sprintf("%s/%t/%s/%s", workload.Namespace, workload.OneShot, workload.Name, workload.UID)
	d.containerLocksMu.Lock()
	if d.containerLocks == nil {
		d.containerLocks = make(map[string]*containerOperationLock)
	}
	lock := d.containerLocks[key]
	if lock == nil {
		lock = &containerOperationLock{token: make(chan struct{}, 1)}
		d.containerLocks[key] = lock
	}
	lock.users++
	d.containerLocksMu.Unlock()
	releaseUser := func() {
		d.containerLocksMu.Lock()
		lock.users--
		if lock.users == 0 {
			delete(d.containerLocks, key)
		}
		d.containerLocksMu.Unlock()
	}
	select {
	case lock.token <- struct{}{}:
	case <-ctx.Done():
		releaseUser()
		return nil, nil, ctx.Err()
	}
	unlock := func() {
		<-lock.token
		releaseUser()
	}
	current, err := d.findContainer(ctx, name)
	if err != nil {
		unlock()
		return nil, nil, err
	}
	if current.Namespace != workload.Namespace || current.Name != workload.Name || current.UID != workload.UID || current.OneShot != workload.OneShot {
		unlock()
		return nil, nil, NotFound(fmt.Errorf("container %s no longer exists", name))
	}
	return current, unlock, nil
}
