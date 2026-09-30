package swarm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	registrytypes "github.com/moby/moby/api/types/registry"
	swarmtypes "github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client/pkg/versions"
	"github.com/moby/moby/v2/daemon/server/backend"
	"github.com/moby/moby/v2/daemon/server/swarmbackend"
	"github.com/sysson/dink/core/version"
	"github.com/sysson/dink/pkg/filters"
	"github.com/sysson/syskit/httpx"
)

func (sr *swarmRouter) initCluster(w http.ResponseWriter, r *http.Request) error {
	var request swarmtypes.InitRequest
	if err := httpx.ParseJSON(r, &request); err != nil {
		return httpx.BadRequest(err)
	}
	nodeID, err := sr.translator.Init(r.Context(), request)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, nodeID)
}

func (sr *swarmRouter) joinCluster(w http.ResponseWriter, r *http.Request) error {
	var request swarmtypes.JoinRequest
	if err := httpx.ParseJSON(r, &request); err != nil {
		return httpx.BadRequest(err)
	}
	return sr.translator.Join(r.Context(), request)
}

func (sr *swarmRouter) leaveCluster(w http.ResponseWriter, r *http.Request) error {
	force, err := queryBool(r, "force")
	if err != nil {
		return err
	}
	return sr.translator.Leave(r.Context(), force)
}

func (sr *swarmRouter) inspectCluster(w http.ResponseWriter, r *http.Request) error {
	cluster, err := sr.translator.Inspect(r.Context())
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, cluster)
}

func (sr *swarmRouter) getUnlockKey(w http.ResponseWriter, r *http.Request) error {
	key, err := sr.translator.GetUnlockKey(r.Context())
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, &swarmtypes.UnlockKeyResponse{UnlockKey: key})
}

func (sr *swarmRouter) updateCluster(w http.ResponseWriter, r *http.Request) error {
	var spec swarmtypes.Spec
	if err := httpx.ParseJSON(r, &spec); err != nil {
		return httpx.BadRequest(err)
	}
	objectVersion, err := queryVersion(r, "swarm")
	if err != nil {
		return err
	}
	var flags swarmbackend.UpdateFlags
	for _, entry := range []struct {
		key    string
		target *bool
	}{
		{"rotateWorkerToken", &flags.RotateWorkerToken},
		{"rotateManagerToken", &flags.RotateManagerToken},
		{"rotateManagerUnlockKey", &flags.RotateManagerUnlockKey},
	} {
		value, err := queryBool(r, entry.key)
		if err != nil {
			return err
		}
		*entry.target = value
	}
	return sr.translator.Update(r.Context(), objectVersion, spec, flags)
}

func (sr *swarmRouter) unlockCluster(w http.ResponseWriter, r *http.Request) error {
	var request swarmtypes.UnlockRequest
	if err := httpx.ParseJSON(r, &request); err != nil {
		return httpx.BadRequest(err)
	}
	return sr.translator.UnlockSwarm(r.Context(), request)
}

func (sr *swarmRouter) getServices(w http.ResponseWriter, r *http.Request) error {
	options := swarmbackend.ServiceListOptions{}
	if err := decodeFilters(r, &options.Filters); err != nil {
		return err
	}
	// The status query parameter was added in API 1.41.
	if !versions.LessThan(version.VersionFromRequest(r), "1.41") {
		status, err := queryBool(r, "status")
		if err != nil {
			return err
		}
		options.Status = status
	}
	services, err := sr.translator.GetServices(r.Context(), options)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, services)
}

func (sr *swarmRouter) getService(w http.ResponseWriter, r *http.Request) error {
	insertDefaults, err := queryBool(r, "insertDefaults")
	if err != nil {
		return err
	}
	service, err := sr.translator.GetService(r.Context(), r.PathValue("id"), insertDefaults)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, service)
}

func (sr *swarmRouter) createService(w http.ResponseWriter, r *http.Request) error {
	var spec swarmtypes.ServiceSpec
	if err := httpx.ParseJSON(r, &spec); err != nil {
		return httpx.BadRequest(err)
	}
	response, err := sr.translator.CreateService(r.Context(), spec, r.Header.Get(registrytypes.AuthHeader), false)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusCreated, response)
}

func (sr *swarmRouter) updateService(w http.ResponseWriter, r *http.Request) error {
	var spec swarmtypes.ServiceSpec
	if err := httpx.ParseJSON(r, &spec); err != nil {
		return httpx.BadRequest(err)
	}
	objectVersion, err := queryVersion(r, "service")
	if err != nil {
		return err
	}
	options := swarmbackend.ServiceUpdateOptions{
		EncodedRegistryAuth: r.Header.Get(registrytypes.AuthHeader),
		Rollback:            r.URL.Query().Get("rollback"),
	}
	if value := r.URL.Query().Get("registryAuthFrom"); value != "" {
		switch source := swarmtypes.RegistryAuthSource(value); source {
		case swarmtypes.RegistryAuthFromSpec, swarmtypes.RegistryAuthFromPreviousSpec:
			options.RegistryAuthFrom = source
		default:
			return httpx.BadRequest(fmt.Errorf("invalid registryAuthFrom %q", value))
		}
	}
	response, err := sr.translator.UpdateService(r.Context(), r.PathValue("id"), objectVersion, spec, options, false)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, response)
}

func (sr *swarmRouter) removeService(w http.ResponseWriter, r *http.Request) error {
	if err := sr.translator.RemoveService(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusOK)
	return nil
}

func (sr *swarmRouter) getServiceLogs(w http.ResponseWriter, r *http.Request) error {
	return sr.swarmLogs(w, r, &backend.LogSelector{Services: []string{r.PathValue("id")}})
}

func (sr *swarmRouter) getTaskLogs(w http.ResponseWriter, r *http.Request) error {
	return sr.swarmLogs(w, r, &backend.LogSelector{Tasks: []string{r.PathValue("id")}})
}

func (sr *swarmRouter) getNodes(w http.ResponseWriter, r *http.Request) error {
	options := swarmbackend.NodeListOptions{}
	if err := decodeFilters(r, &options.Filters); err != nil {
		return err
	}
	nodes, err := sr.translator.GetNodes(r.Context(), options)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, nodes)
}

func (sr *swarmRouter) getNode(w http.ResponseWriter, r *http.Request) error {
	node, err := sr.translator.GetNode(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, node)
}

func (sr *swarmRouter) removeNode(w http.ResponseWriter, r *http.Request) error {
	force, err := queryBool(r, "force")
	if err != nil {
		return err
	}
	return sr.translator.RemoveNode(r.Context(), r.PathValue("id"), force)
}

func (sr *swarmRouter) updateNode(w http.ResponseWriter, r *http.Request) error {
	var spec swarmtypes.NodeSpec
	if err := httpx.ParseJSON(r, &spec); err != nil {
		return httpx.BadRequest(err)
	}
	objectVersion, err := queryVersion(r, "node")
	if err != nil {
		return err
	}
	return sr.translator.UpdateNode(r.Context(), r.PathValue("id"), objectVersion, spec)
}

func (sr *swarmRouter) getTasks(w http.ResponseWriter, r *http.Request) error {
	options := swarmbackend.TaskListOptions{}
	if err := decodeFilters(r, &options.Filters); err != nil {
		return err
	}
	tasks, err := sr.translator.GetTasks(r.Context(), options)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, tasks)
}

func (sr *swarmRouter) getTask(w http.ResponseWriter, r *http.Request) error {
	task, err := sr.translator.GetTask(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, task)
}

func (sr *swarmRouter) getSecrets(w http.ResponseWriter, r *http.Request) error {
	options := swarmbackend.SecretListOptions{}
	if err := decodeFilters(r, &options.Filters); err != nil {
		return err
	}
	secrets, err := sr.translator.GetSecrets(r.Context(), options)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, secrets)
}

func (sr *swarmRouter) createSecret(w http.ResponseWriter, r *http.Request) error {
	var spec swarmtypes.SecretSpec
	if err := httpx.ParseJSON(r, &spec); err != nil {
		return httpx.BadRequest(err)
	}
	id, err := sr.translator.CreateSecret(r.Context(), spec)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusCreated, &swarmtypes.SecretCreateResponse{ID: id})
}

func (sr *swarmRouter) removeSecret(w http.ResponseWriter, r *http.Request) error {
	if err := sr.translator.RemoveSecret(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (sr *swarmRouter) getSecret(w http.ResponseWriter, r *http.Request) error {
	secret, err := sr.translator.GetSecret(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, secret)
}

func (sr *swarmRouter) updateSecret(w http.ResponseWriter, r *http.Request) error {
	var spec swarmtypes.SecretSpec
	if err := httpx.ParseJSON(r, &spec); err != nil {
		return httpx.BadRequest(err)
	}
	objectVersion, err := queryVersion(r, "secret")
	if err != nil {
		return err
	}
	return sr.translator.UpdateSecret(r.Context(), r.PathValue("id"), objectVersion, spec)
}

func (sr *swarmRouter) getConfigs(w http.ResponseWriter, r *http.Request) error {
	options := swarmbackend.ConfigListOptions{}
	if err := decodeFilters(r, &options.Filters); err != nil {
		return err
	}
	configs, err := sr.translator.GetConfigs(r.Context(), options)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, configs)
}

func (sr *swarmRouter) createConfig(w http.ResponseWriter, r *http.Request) error {
	var spec swarmtypes.ConfigSpec
	if err := httpx.ParseJSON(r, &spec); err != nil {
		return httpx.BadRequest(err)
	}
	id, err := sr.translator.CreateConfig(r.Context(), spec)
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusCreated, &swarmtypes.ConfigCreateResponse{ID: id})
}

func (sr *swarmRouter) removeConfig(w http.ResponseWriter, r *http.Request) error {
	if err := sr.translator.RemoveConfig(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (sr *swarmRouter) getConfig(w http.ResponseWriter, r *http.Request) error {
	config, err := sr.translator.GetConfig(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return httpx.WriteJSON(w, http.StatusOK, config)
}

func (sr *swarmRouter) updateConfig(w http.ResponseWriter, r *http.Request) error {
	var spec swarmtypes.ConfigSpec
	if err := httpx.ParseJSON(r, &spec); err != nil {
		return httpx.BadRequest(err)
	}
	objectVersion, err := queryVersion(r, "config")
	if err != nil {
		return err
	}
	return sr.translator.UpdateConfig(r.Context(), r.PathValue("id"), objectVersion, spec)
}

// decodeFilters routes Docker's filter arguments through JSON because the
// concrete filter type lives in an internal Moby package.
func decodeFilters(r *http.Request, target any) error {
	parsed, err := filters.FromJSON(r.URL.Query().Get("filters"))
	if err != nil {
		return httpx.BadRequest(err)
	}
	encoded, err := filters.ToJSON(parsed)
	if err != nil {
		return httpx.BadRequest(err)
	}
	if encoded == "" {
		encoded = "{}"
	}
	if err := json.Unmarshal([]byte(encoded), target); err != nil {
		return httpx.BadRequest(err)
	}
	return nil
}

func queryVersion(r *http.Request, kind string) (uint64, error) {
	raw := r.URL.Query().Get("version")
	parsed, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, httpx.BadRequest(fmt.Errorf("invalid %s version %q", kind, raw))
	}
	return parsed, nil
}

func queryBool(r *http.Request, key string) (bool, error) {
	value := r.URL.Query().Get(key)
	if value == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, httpx.BadRequest(fmt.Errorf("invalid %s value %q", key, value))
	}
	return parsed, nil
}
