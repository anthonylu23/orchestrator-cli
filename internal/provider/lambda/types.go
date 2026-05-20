package lambda

type InstanceTypeSpecs struct {
	VCPUs      int `json:"vcpus"`
	MemoryGiB  int `json:"memory_gib"`
	StorageGiB int `json:"storage_gib"`
	GPUs       int `json:"gpus"`
}

type InstanceType struct {
	Name              string            `json:"name"`
	Description       string            `json:"description"`
	GPUDescription    string            `json:"gpu_description"`
	PriceCentsPerHour int               `json:"price_cents_per_hour"`
	Specs             InstanceTypeSpecs `json:"specs"`
}

type Region struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type InstanceTypesItem struct {
	InstanceType                 InstanceType `json:"instance_type"`
	RegionsWithCapacityAvailable []Region     `json:"regions_with_capacity_available"`
}

type Instance struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	IP           string       `json:"ip"`
	PrivateIP    string       `json:"private_ip"`
	Status       string       `json:"status"`
	Region       Region       `json:"region"`
	InstanceType InstanceType `json:"instance_type"`
}

type LaunchInstanceRequest struct {
	RegionName       string              `json:"region_name"`
	InstanceTypeName string              `json:"instance_type_name"`
	SSHKeyNames      []string            `json:"ssh_key_names"`
	Hostname         string              `json:"hostname,omitempty"`
	Name             string              `json:"name,omitempty"`
	Image            map[string]string   `json:"image,omitempty"`
	UserData         string              `json:"user_data,omitempty"`
	Tags             []RequestedTagEntry `json:"tags,omitempty"`
}

type RequestedTagEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type lambdaErrorResponse struct {
	Error lambdaAPIError `json:"error"`
}

type lambdaAPIError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion"`
}

type apiError struct {
	StatusCode int
	Code       string
	Message    string
	Suggestion string
}

func (e *apiError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Code != "" {
		return e.Code
	}
	return "lambda api error"
}
