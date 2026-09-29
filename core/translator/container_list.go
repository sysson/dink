package translator

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/sysson/dink/core/identity"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (d *Docker) Containers(ctx context.Context, options *backend.ContainerListOptions) ([]container.Summary, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, Unauthenticated(fmt.Errorf("missing identity in context"))
	}
	if options == nil {
		options = &backend.ContainerListOptions{}
	}
	if options.Limit < 0 {
		return nil, InvalidArgument(fmt.Errorf("container list limit must not be negative"))
	}
	if err := validateContainerListFilters(options.Filters); err != nil {
		return nil, InvalidArgument(err)
	}
	deployments, err := d.k8s.AppsV1().Deployments(id.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, kubeError(err)
	}
	sort.Slice(deployments.Items, func(i, j int) bool {
		return deployments.Items[i].CreationTimestamp.After(deployments.Items[j].CreationTimestamp.Time)
	})
	result := make([]container.Summary, 0, len(deployments.Items))
	for index := range deployments.Items {
		deployment := &deployments.Items[index]
		inspect, err := d.inspectDeployment(ctx, deployment)
		if err != nil {
			return nil, err
		}
		summary := containerSummary(deployment, inspect)
		if !matchesContainerListFilters(options.Filters, summary, inspect.Config) {
			continue
		}
		if !options.All && summary.State != container.StateRunning {
			continue
		}
		result = append(result, summary)
		if options.Limit > 0 && len(result) == options.Limit {
			break
		}
	}
	return result, nil
}

func containerSummary(deployment *appsv1.Deployment, inspect *container.InspectResponse) container.Summary {
	config := inspect.Config
	command := append(append([]string{}, config.Entrypoint...), config.Cmd...)
	state := inspect.State.Status
	status := string(state)
	switch state {
	case container.StateRunning:
		status = "Up " + time.Since(deployment.CreationTimestamp.Time).Round(time.Second).String()
	case container.StateExited:
		status = fmt.Sprintf("Exited (%d)", inspect.State.ExitCode)
	}
	labels := config.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	ports := make([]container.PortSummary, 0)
	for port, bindings := range inspect.NetworkSettings.Ports {
		for _, binding := range bindings {
			publicPort, _ := strconv.ParseUint(binding.HostPort, 10, 16)
			ports = append(ports, container.PortSummary{PrivatePort: port.Num(), PublicPort: uint16(publicPort), Type: string(port.Proto()), IP: binding.HostIP})
		}
	}
	return container.Summary{
		ID:      inspect.ID,
		Names:   []string{inspect.Name},
		Image:   config.Image,
		ImageID: inspect.Image,
		Command: strings.Join(command, " "),
		Created: deployment.CreationTimestamp.Unix(),
		Ports:   ports,
		Labels:  labels,
		State:   state,
		Status:  status,
		HostConfig: struct {
			NetworkMode string            `json:",omitempty"`
			Annotations map[string]string `json:",omitempty"`
		}{NetworkMode: string(inspect.HostConfig.NetworkMode), Annotations: inspect.HostConfig.Annotations},
		NetworkSettings: &container.NetworkSettingsSummary{Networks: inspect.NetworkSettings.Networks},
		Mounts:          []container.MountPoint{},
	}
}

type containerListFilters interface {
	Validate(map[string]bool) error
	FuzzyMatch(string, string) bool
	Match(string, string) bool
	ExactMatch(string, string) bool
	MatchKVList(string, map[string]string) bool
}

func validateContainerListFilters(filters containerListFilters) error {
	allowed := map[string]bool{"ancestor": true, "id": true, "label": true, "name": true, "status": true}
	return filters.Validate(allowed)
}

func matchesContainerListFilters(filters containerListFilters, summary container.Summary, config *container.Config) bool {
	if !filters.FuzzyMatch("id", summary.ID) && !filters.FuzzyMatch("id", identity.Truncate(summary.ID)) {
		return false
	}
	if !filters.Match("name", strings.TrimPrefix(summary.Names[0], "/")) {
		return false
	}
	if !filters.ExactMatch("status", string(summary.State)) {
		return false
	}
	if !filters.MatchKVList("label", config.Labels) {
		return false
	}
	imageMatches := filters.FuzzyMatch("ancestor", summary.Image) || filters.FuzzyMatch("ancestor", summary.ImageID)
	return imageMatches
}
