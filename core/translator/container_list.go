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
	workloads, err := listWorkloads(ctx, d.k8s, id.Namespace)
	if err != nil {
		return nil, err
	}
	sort.Slice(workloads, func(i, j int) bool {
		return workloads[i].CreationTimestamp.After(workloads[j].CreationTimestamp.Time)
	})
	result := make([]container.Summary, 0, len(workloads))
	for _, deployment := range workloads {
		inspect, err := d.inspectContainer(ctx, deployment)
		if err != nil {
			return nil, err
		}
		summary := containerSummary(deployment, inspect)
		if !matchesContainerListFilters(options.Filters, summary, inspect.Config) {
			continue
		}
		if !options.All && !inspect.State.Running {
			continue
		}
		result = append(result, summary)
		if options.Limit > 0 && len(result) == options.Limit {
			break
		}
	}
	return result, nil
}

func containerSummary(deployment *containerWorkload, inspect *container.InspectResponse) container.Summary {
	config := inspect.Config
	command := append(append([]string{}, config.Entrypoint...), config.Cmd...)
	state := inspect.State.Status
	status := string(state)
	switch state {
	case container.StateRunning:
		started := deployment.CreationTimestamp.Time
		if startedAt, err := time.Parse(time.RFC3339Nano, inspect.State.StartedAt); err == nil {
			started = startedAt
		}
		status = "Up " + time.Since(started).Round(time.Second).String()
		if inspect.State.Health != nil {
			switch inspect.State.Health.Status {
			case container.Starting:
				status += " (health: starting)"
			default:
				status += " (" + string(inspect.State.Health.Status) + ")"
			}
		}
	case container.StateRestarting:
		status = fmt.Sprintf("Restarting (%d)", inspect.State.ExitCode)
		if finishedAt, err := time.Parse(time.RFC3339Nano, inspect.State.FinishedAt); err == nil {
			status += " " + time.Since(finishedAt).Round(time.Second).String() + " ago"
		}
	case container.StateExited:
		status = fmt.Sprintf("Exited (%d)", inspect.State.ExitCode)
	}
	var health *container.HealthSummary
	if inspect.State.Health != nil {
		health = &container.HealthSummary{Status: inspect.State.Health.Status}
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
		Health:  health,
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
