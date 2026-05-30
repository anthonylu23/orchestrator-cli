package hyperbolic

import (
	"strconv"
	"strings"

	"github.com/anthonylu23/switchboard-cli/internal/credentials"
)

const ProviderName = "hyperbolic"

const defaultVMConfigID = "c6fd6253-cbb6-4ea8-a20c-47644b431f1c"

type Config struct {
	VMConfigID               string
	GPUCount                 int
	GPUType                  string
	SSHUser                  string
	SSHPrivateKey            string
	RegistryAuth             RegistryAuth
	PollIntervalSeconds      int
	TerminateOnCompletion    bool
	TerminateOnCompletionSet bool
	KeepInstanceOnFailure    bool
	APITimeoutSeconds        int
	SSHConnectTimeoutSecs    int
	SSHReadyTimeoutSeconds   int
	EstimateHourlyUSD        float64
	BaseURL                  string
	Credentials              credentials.Resolver
}

type RegistryAuth struct {
	Server   string
	Username string
	Password string
}

type VirtualMachineOption struct {
	GPUCount    int     `json:"gpuCount"`
	CostPerHour float64 `json:"costPerHour"`
}

type VirtualMachineRentalRequest struct {
	ConfigID string `json:"configId"`
	GPUCount int    `json:"gpuCount"`
}

type VirtualMachineRental struct {
	ID          int          `json:"id"`
	ExternalID  string       `json:"externalId"`
	CostPerHour int          `json:"costPerHour"`
	Status      string       `json:"status"`
	Meta        InstanceMeta `json:"meta"`
}

func (r VirtualMachineRental) RefID() string {
	if r.ID != 0 {
		return strconv.Itoa(r.ID)
	}
	return r.ExternalID
}

type Instance struct {
	ID             int          `json:"id"`
	ExternalID     string       `json:"externalId"`
	CostPerHour    int          `json:"costPerHour"`
	Status         string       `json:"status"`
	RentalProvider string       `json:"rentalProvider"`
	StartedAt      string       `json:"startedAt"`
	TerminatedAt   *string      `json:"terminatedAt"`
	Meta           InstanceMeta `json:"meta"`
}

type InstanceMeta struct {
	Name       string             `json:"name"`
	Type       string             `json:"type,omitempty"`
	PublicIP   string             `json:"public_ip,omitempty"`
	InternalIP string             `json:"internal_ip,omitempty"`
	GPUCount   int                `json:"gpu_count,omitempty"`
	Resources  *InstanceResources `json:"resources,omitempty"`
	SSHCommand string             `json:"ssh_command,omitempty"`
	Username   string             `json:"username,omitempty"`
}

type InstanceResources struct {
	RAMGB     int            `json:"ram_gb"`
	StorageGB int            `json:"storage_gb"`
	VCPUCount int            `json:"vcpu_count"`
	GPUs      map[string]GPU `json:"gpus"`
}

type GPU struct {
	Count int `json:"count"`
}

func (i Instance) RefID() string {
	if i.ID != 0 {
		return strconv.Itoa(i.ID)
	}
	return i.ExternalID
}

func (i Instance) PublicIP() string {
	if i.Meta.PublicIP != "" {
		return i.Meta.PublicIP
	}
	_, host := parseSSHCommand(i.Meta.SSHCommand)
	return host
}

func (i Instance) User(defaultUser string) string {
	if i.Meta.Username != "" {
		return i.Meta.Username
	}
	user, _ := parseSSHCommand(i.Meta.SSHCommand)
	if user != "" {
		return user
	}
	if defaultUser != "" {
		return defaultUser
	}
	return "ubuntu"
}

func (i Instance) GPUCount(defaultCount int) int {
	if i.Meta.GPUCount > 0 {
		return i.Meta.GPUCount
	}
	if i.Meta.Resources != nil {
		total := 0
		for _, gpu := range i.Meta.Resources.GPUs {
			total += gpu.Count
		}
		if total > 0 {
			return total
		}
	}
	return defaultCount
}

func parseSSHCommand(command string) (string, string) {
	fields := strings.Fields(command)
	for _, field := range fields {
		if strings.Contains(field, "@") && !strings.HasPrefix(field, "-") {
			user, host, ok := strings.Cut(field, "@")
			if ok {
				host = strings.Trim(host, " ")
				host = strings.TrimPrefix(host, "[")
				host = strings.TrimSuffix(host, "]")
				return user, host
			}
		}
	}
	return "", ""
}
