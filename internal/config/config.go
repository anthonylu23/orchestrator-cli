package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/home"
	"gopkg.in/yaml.v3"
)

const DefaultBundleMaxSizeMB = 512

type Config struct {
	Job        JobConfig        `yaml:"job"`
	Data       DataConfig       `yaml:"data"`
	Staging    StagingConfig    `yaml:"staging"`
	Packaging  PackagingConfig  `yaml:"packaging"`
	Routing    RoutingConfig    `yaml:"routing"`
	Sizing     SizingConfig     `yaml:"sizing"`
	Hardware   HardwareConfig   `yaml:"hardware"`
	Mock       MockConfig       `yaml:"mock"`
	GCP        GCPConfig        `yaml:"gcp"`
	Lambda     LambdaConfig     `yaml:"lambda"`
	Hyperbolic HyperbolicConfig `yaml:"hyperbolic"`
	ChinaCloud ChinaCloudConfig `yaml:"china_cloud"`
}

type JobConfig struct {
	Name    string            `yaml:"name"`
	Script  string            `yaml:"script"`
	Image   string            `yaml:"image"`
	Command []string          `yaml:"command"`
	Args    []string          `yaml:"args"`
	Env     map[string]string `yaml:"env"`
	WorkDir string            `yaml:"work_dir"`
}

type DataConfig struct {
	Inputs []app.DataInput `yaml:"inputs"`
	Bundle BundleConfig    `yaml:"bundle"`
}

type PackagingConfig struct {
	Dockerfile string `yaml:"dockerfile"`
	Context    string `yaml:"context"`
	Image      string `yaml:"image"`
	Platform   string `yaml:"platform"`
}

type BundleConfig struct {
	MaxSizeMB                 int  `yaml:"max_size_mb"`
	RequireOverrideAboveLimit bool `yaml:"require_override_above_limit"`
}

type StagingConfig struct {
	CheckpointURIPrefix string `yaml:"checkpoint_uri_prefix"`
	DataURIPrefix       string `yaml:"data_uri_prefix"`
}

type RoutingConfig struct {
	Mode        string       `yaml:"mode"`
	Objective   string       `yaml:"objective"`
	Budget      BudgetConfig `yaml:"budget"`
	MaxAttempts int          `yaml:"max_attempts"`
}

type BudgetConfig struct {
	MaxRunCostUSD float64 `yaml:"max_run_cost_usd"`
}

type SizingConfig struct {
	Probe SizingProbeConfig `yaml:"probe"`
	Hints SizingHintsConfig `yaml:"hints"`
}

type SizingProbeConfig struct {
	Command []string `yaml:"command"`
	Output  string   `yaml:"output"`
}

type SizingHintsConfig struct {
	RequiredVRAMGB            float64 `yaml:"required_vram_gb"`
	DatasetSizeGB             float64 `yaml:"dataset_size_gb"`
	ModelParametersB          float64 `yaml:"model_parameters_b"`
	ModelArtifactGB           float64 `yaml:"model_artifact_gb"`
	BatchSize                 int     `yaml:"batch_size"`
	GradientAccumulationSteps int     `yaml:"gradient_accumulation_steps"`
	Precision                 string  `yaml:"precision"`
	Optimizer                 string  `yaml:"optimizer"`
	SequenceLength            int     `yaml:"sequence_length"`
	ImageWidth                int     `yaml:"image_width"`
	ImageHeight               int     `yaml:"image_height"`
	ExpectedSteps             int     `yaml:"expected_steps"`
}

type HardwareConfig struct {
	Constraints HardwareConstraintsConfig `yaml:"constraints"`
	Manual      ManualHardwareConfig      `yaml:"manual"`
}

type HardwareConstraintsConfig struct {
	MaxGPUs            int      `yaml:"max_gpus"`
	AllowedGPUFamilies []string `yaml:"allowed_gpu_families"`
	MinVRAMGBPerGPU    int      `yaml:"min_vram_gb_per_gpu"`
	Regions            []string `yaml:"regions"`
	AllowSpot          bool     `yaml:"allow_spot"`
	RequireOnDemand    bool     `yaml:"require_on_demand"`
}

type ManualHardwareConfig struct {
	Provider    string `yaml:"provider"`
	ShapeID     string `yaml:"shape_id"`
	MachineType string `yaml:"machine_type"`
	Region      string `yaml:"region"`
}

type MockConfig struct {
	Providers []MockProviderConfig `yaml:"providers"`
}

type GCPConfig struct {
	ProjectID                  string  `yaml:"project_id"`
	Location                   string  `yaml:"location"`
	OutputURIPrefix            string  `yaml:"output_uri_prefix"`
	MachineType                string  `yaml:"machine_type"`
	AcceleratorType            string  `yaml:"accelerator_type"`
	AcceleratorCount           int32   `yaml:"accelerator_count"`
	BootDiskType               string  `yaml:"boot_disk_type"`
	BootDiskSizeGB             int32   `yaml:"boot_disk_size_gb"`
	ServiceAccount             string  `yaml:"service_account"`
	Network                    string  `yaml:"network"`
	PollIntervalSeconds        int     `yaml:"poll_interval_seconds"`
	EstimateHourlyUSD          float64 `yaml:"estimate_hourly_usd"`
	ArtifactRegistryRepository string  `yaml:"artifact_registry_repository"`
}

type LambdaConfig struct {
	RegionName             string             `yaml:"region_name"`
	InstanceTypeName       string             `yaml:"instance_type_name"`
	SSHKeyName             string             `yaml:"ssh_key_name"`
	SSHPrivateKey          string             `yaml:"ssh_private_key"`
	ImageFamily            string             `yaml:"image_family"`
	RegistryAuth           RegistryAuthConfig `yaml:"registry_auth"`
	PollIntervalSeconds    int                `yaml:"poll_interval_seconds"`
	TerminateOnCompletion  *bool              `yaml:"terminate_on_completion"`
	KeepInstanceOnFailure  bool               `yaml:"keep_instance_on_failure"`
	APITimeoutSeconds      int                `yaml:"api_timeout_seconds"`
	SSHConnectTimeoutSecs  int                `yaml:"ssh_connect_timeout_seconds"`
	SSHReadyTimeoutSeconds int                `yaml:"ssh_ready_timeout_seconds"`
}

type HyperbolicConfig struct {
	VMConfigID             string             `yaml:"vm_config_id"`
	GPUCount               int                `yaml:"gpu_count"`
	GPUType                string             `yaml:"gpu_type"`
	SSHUser                string             `yaml:"ssh_user"`
	SSHPrivateKey          string             `yaml:"ssh_private_key"`
	RegistryAuth           RegistryAuthConfig `yaml:"registry_auth"`
	PollIntervalSeconds    int                `yaml:"poll_interval_seconds"`
	TerminateOnCompletion  *bool              `yaml:"terminate_on_completion"`
	KeepInstanceOnFailure  bool               `yaml:"keep_instance_on_failure"`
	APITimeoutSeconds      int                `yaml:"api_timeout_seconds"`
	SSHConnectTimeoutSecs  int                `yaml:"ssh_connect_timeout_seconds"`
	SSHReadyTimeoutSeconds int                `yaml:"ssh_ready_timeout_seconds"`
	EstimateHourlyUSD      float64            `yaml:"estimate_hourly_usd"`
	APIBaseURL             string             `yaml:"api_base_url"`
}

type RegistryAuthConfig struct {
	Server      string `yaml:"server"`
	UsernameEnv string `yaml:"username_env"`
	PasswordEnv string `yaml:"password_env"`
}

type ChinaCloudConfig struct {
	Common       ChinaCloudProviderConfig `yaml:"common"`
	AlibabaCloud ChinaCloudProviderConfig `yaml:"alibaba_cloud"`
	HuaweiCloud  ChinaCloudProviderConfig `yaml:"huawei_cloud"`
	TencentCloud ChinaCloudProviderConfig `yaml:"tencent_cloud"`
	TianyiCloud  ChinaCloudProviderConfig `yaml:"tianyi_cloud"`
	BaiduAICloud ChinaCloudProviderConfig `yaml:"baidu_ai_cloud"`
}

type ChinaCloudProviderConfig struct {
	Region                 string  `yaml:"region"`
	Zone                   string  `yaml:"zone"`
	InstanceType           string  `yaml:"instance_type"`
	ImageID                string  `yaml:"image_id"`
	VPCID                  string  `yaml:"vpc_id"`
	SubnetID               string  `yaml:"subnet_id"`
	SecurityGroupID        string  `yaml:"security_group_id"`
	SSHKeyName             string  `yaml:"ssh_key_name"`
	SSHPrivateKey          string  `yaml:"ssh_private_key"`
	SSHUser                string  `yaml:"ssh_user"`
	Endpoint               string  `yaml:"endpoint"`
	ProjectID              string  `yaml:"project_id"`
	AccountID              string  `yaml:"account_id"`
	SystemDiskCategory     string  `yaml:"system_disk_category"`
	SystemDiskSizeGB       int     `yaml:"system_disk_size_gb"`
	InternetBandwidthMbps  int     `yaml:"internet_bandwidth_mbps"`
	PollIntervalSeconds    int     `yaml:"poll_interval_seconds"`
	TerminateOnCompletion  *bool   `yaml:"terminate_on_completion"`
	KeepInstanceOnFailure  bool    `yaml:"keep_instance_on_failure"`
	APITimeoutSeconds      int     `yaml:"api_timeout_seconds"`
	SSHConnectTimeoutSecs  int     `yaml:"ssh_connect_timeout_seconds"`
	SSHReadyTimeoutSeconds int     `yaml:"ssh_ready_timeout_seconds"`
	EstimateHourlyUSD      float64 `yaml:"estimate_hourly_usd"`
}

type MockProviderConfig struct {
	Name           string              `yaml:"name"`
	HourlyCost     float64             `yaml:"hourly_cost"`
	FailureMode    string              `yaml:"failure_mode"`
	HardwareShapes []app.HardwareShape `yaml:"hardware_shapes"`
	Events         []MockEventConfig   `yaml:"events"`
}

type MockEventConfig struct {
	Type          string             `yaml:"type"`
	Step          *int64             `yaml:"step"`
	Split         string             `yaml:"split"`
	State         string             `yaml:"state"`
	CheckpointURI string             `yaml:"checkpoint_uri"`
	Message       string             `yaml:"message"`
	Metrics       map[string]float64 `yaml:"metrics"`
}

type TrainFlags struct {
	ConfigPath           string
	Provider             string
	Script               string
	Args                 []string
	AllowLargeDataBundle bool
	SwitchboardHome      string
}

type ResolvedTrainConfig struct {
	Provider                  string
	Job                       app.JobSpec
	Staging                   StagingConfig
	Packaging                 PackagingConfig
	Routing                   RoutingConfig
	Sizing                    SizingConfig
	Hardware                  HardwareConfig
	Mock                      MockConfig
	GCP                       GCPConfig
	Lambda                    LambdaConfig
	Hyperbolic                HyperbolicConfig
	ChinaCloud                ChinaCloudConfig
	BundleMaxSizeBytes        int64
	RequireOverrideAboveLimit bool
	AllowLargeDataBundle      bool
	SwitchboardHome           string
}

func LoadTrain(flags TrainFlags) (ResolvedTrainConfig, error) {
	cfg := Config{}
	configDir := ""
	if flags.ConfigPath != "" {
		loaded, err := LoadFile(flags.ConfigPath)
		if err != nil {
			return ResolvedTrainConfig{}, err
		}
		cfg = loaded
		configDir = filepath.Dir(flags.ConfigPath)
	}

	provider := cfgProviderDefault(flags.Provider)
	if provider == "" {
		provider = string(app.ProviderLocal)
	}

	job := app.JobSpec{
		Name:    cfg.Job.Name,
		Script:  cfg.Job.Script,
		Image:   cfg.Job.Image,
		Command: append([]string(nil), cfg.Job.Command...),
		Args:    append([]string(nil), cfg.Job.Args...),
		Env:     cloneMap(cfg.Job.Env),
		Data:    append([]app.DataInput(nil), cfg.Data.Inputs...),
		WorkDir: cfg.Job.WorkDir,
	}
	scriptFromFlag := flags.Script != ""
	if scriptFromFlag {
		job.Script = flags.Script
	}
	if len(flags.Args) > 0 {
		job.Args = append([]string(nil), flags.Args...)
	}
	if job.Script == "" && job.Image == "" {
		return ResolvedTrainConfig{}, errors.New("script or image is required")
	}
	if job.Name == "" {
		if job.Script != "" {
			job.Name = filepath.Base(job.Script)
		} else {
			job.Name = filepath.Base(job.Image)
		}
	}
	if job.Env == nil {
		job.Env = map[string]string{}
	}
	if job.WorkDir == "" {
		job.WorkDir = "."
	}
	if configDir != "" {
		if scriptFromFlag {
			originalScript := job.Script
			job = resolveJobPaths(job, configDir)
			job.Script = originalScript
		} else {
			job = resolveJobPaths(job, configDir)
		}
		cfg.Packaging = resolvePackagingPaths(cfg.Packaging, configDir)
		cfg.Sizing = resolveSizingPaths(cfg.Sizing, configDir)
		cfg.Lambda = resolveLambdaPaths(cfg.Lambda, configDir)
		cfg.Hyperbolic = resolveHyperbolicPaths(cfg.Hyperbolic, configDir)
		cfg.ChinaCloud = resolveChinaCloudPaths(cfg.ChinaCloud, configDir)
	}

	maxSizeMB := cfg.Data.Bundle.MaxSizeMB
	if maxSizeMB == 0 {
		maxSizeMB = DefaultBundleMaxSizeMB
	}
	requireOverride := cfg.Data.Bundle.RequireOverrideAboveLimit
	if flags.ConfigPath == "" || cfg.Data.Bundle.MaxSizeMB == 0 {
		requireOverride = true
	}

	resolvedHome, err := home.Resolve(flags.SwitchboardHome)
	if err != nil {
		return ResolvedTrainConfig{}, err
	}

	return ResolvedTrainConfig{
		Provider:                  provider,
		Job:                       job,
		Staging:                   cfg.Staging,
		Packaging:                 resolvePackaging(cfg.Packaging),
		Routing:                   resolveRouting(cfg.Routing),
		Sizing:                    cfg.Sizing,
		Hardware:                  cfg.Hardware,
		Mock:                      cfg.Mock,
		GCP:                       resolveGCP(cfg.GCP),
		Lambda:                    resolveLambda(cfg.Lambda),
		Hyperbolic:                resolveHyperbolic(cfg.Hyperbolic),
		ChinaCloud:                resolveChinaCloud(cfg.ChinaCloud),
		BundleMaxSizeBytes:        int64(maxSizeMB) * 1024 * 1024,
		RequireOverrideAboveLimit: requireOverride,
		AllowLargeDataBundle:      flags.AllowLargeDataBundle,
		SwitchboardHome:           resolvedHome,
	}, validateResolvedConfig(provider, job, cfg.Staging, resolvePackaging(cfg.Packaging), resolveGCP(cfg.GCP), resolveLambda(cfg.Lambda), resolveHyperbolic(cfg.Hyperbolic), resolveChinaCloud(cfg.ChinaCloud))
}

func resolvePackaging(packaging PackagingConfig) PackagingConfig {
	if packaging.Context == "" {
		packaging.Context = "."
	}
	return packaging
}

func resolveJobPaths(job app.JobSpec, baseDir string) app.JobSpec {
	job.Script = resolveLocalPath(job.Script, baseDir)
	job.WorkDir = resolveLocalPath(job.WorkDir, baseDir)
	for i := range job.Data {
		if isPathLikeSource(job.Data[i].Source) {
			job.Data[i].Source = resolveLocalPath(job.Data[i].Source, baseDir)
		}
	}
	return job
}

func resolvePackagingPaths(packaging PackagingConfig, baseDir string) PackagingConfig {
	packaging = resolvePackaging(packaging)
	packaging.Context = resolveLocalPath(packaging.Context, baseDir)
	packaging.Dockerfile = resolveLocalPath(packaging.Dockerfile, baseDir)
	return packaging
}

func resolveSizingPaths(sizing SizingConfig, baseDir string) SizingConfig {
	sizing.Probe.Output = resolveLocalPath(sizing.Probe.Output, baseDir)
	return sizing
}

func resolveLambdaPaths(lambda LambdaConfig, baseDir string) LambdaConfig {
	if strings.HasPrefix(lambda.SSHPrivateKey, "~/") {
		if homeDir, err := os.UserHomeDir(); err == nil {
			lambda.SSHPrivateKey = filepath.Join(homeDir, strings.TrimPrefix(lambda.SSHPrivateKey, "~/"))
		}
		return lambda
	}
	lambda.SSHPrivateKey = resolveLocalPath(lambda.SSHPrivateKey, baseDir)
	return lambda
}

func resolveHyperbolicPaths(hyperbolic HyperbolicConfig, baseDir string) HyperbolicConfig {
	if strings.HasPrefix(hyperbolic.SSHPrivateKey, "~/") {
		if homeDir, err := os.UserHomeDir(); err == nil {
			hyperbolic.SSHPrivateKey = filepath.Join(homeDir, strings.TrimPrefix(hyperbolic.SSHPrivateKey, "~/"))
		}
		return hyperbolic
	}
	hyperbolic.SSHPrivateKey = resolveLocalPath(hyperbolic.SSHPrivateKey, baseDir)
	return hyperbolic
}

func resolveChinaCloudPaths(china ChinaCloudConfig, baseDir string) ChinaCloudConfig {
	china.Common = resolveChinaCloudProviderPaths(china.Common, baseDir)
	china.AlibabaCloud = resolveChinaCloudProviderPaths(china.AlibabaCloud, baseDir)
	china.HuaweiCloud = resolveChinaCloudProviderPaths(china.HuaweiCloud, baseDir)
	china.TencentCloud = resolveChinaCloudProviderPaths(china.TencentCloud, baseDir)
	china.TianyiCloud = resolveChinaCloudProviderPaths(china.TianyiCloud, baseDir)
	china.BaiduAICloud = resolveChinaCloudProviderPaths(china.BaiduAICloud, baseDir)
	return china
}

func resolveChinaCloudProviderPaths(provider ChinaCloudProviderConfig, baseDir string) ChinaCloudProviderConfig {
	if strings.HasPrefix(provider.SSHPrivateKey, "~/") {
		if homeDir, err := os.UserHomeDir(); err == nil {
			provider.SSHPrivateKey = filepath.Join(homeDir, strings.TrimPrefix(provider.SSHPrivateKey, "~/"))
		}
		return provider
	}
	provider.SSHPrivateKey = resolveLocalPath(provider.SSHPrivateKey, baseDir)
	return provider
}

func resolveLocalPath(path string, baseDir string) string {
	if path == "" || filepath.IsAbs(path) || baseDir == "" {
		return path
	}
	return filepath.Clean(filepath.Join(baseDir, path))
}

func isPathLikeSource(source string) bool {
	parsed, err := url.Parse(source)
	if err != nil {
		return true
	}
	return parsed.Scheme == "" || len(parsed.Scheme) == 1 && !strings.Contains(source, "://")
}

func resolveRouting(routing RoutingConfig) RoutingConfig {
	if routing.Objective == "" {
		routing.Objective = "min_cost"
	}
	if routing.MaxAttempts == 0 {
		routing.MaxAttempts = 2
	}
	return routing
}

func resolveGCP(gcp GCPConfig) GCPConfig {
	if gcp.Location == "" {
		gcp.Location = "us-central1"
	}
	if gcp.MachineType == "" {
		gcp.MachineType = "n1-standard-4"
	}
	if gcp.BootDiskType == "" {
		gcp.BootDiskType = "pd-ssd"
	}
	if gcp.BootDiskSizeGB == 0 {
		gcp.BootDiskSizeGB = 100
	}
	if gcp.PollIntervalSeconds == 0 {
		gcp.PollIntervalSeconds = 30
	}
	return gcp
}

func resolveLambda(lambda LambdaConfig) LambdaConfig {
	if lambda.PollIntervalSeconds == 0 {
		lambda.PollIntervalSeconds = 30
	}
	if lambda.TerminateOnCompletion == nil {
		defaultTerminate := true
		lambda.TerminateOnCompletion = &defaultTerminate
	}
	if lambda.APITimeoutSeconds == 0 {
		lambda.APITimeoutSeconds = 30
	}
	if lambda.SSHConnectTimeoutSecs == 0 {
		lambda.SSHConnectTimeoutSecs = 10
	}
	if lambda.SSHReadyTimeoutSeconds == 0 {
		lambda.SSHReadyTimeoutSeconds = 600
	}
	return lambda
}

func resolveHyperbolic(hyperbolic HyperbolicConfig) HyperbolicConfig {
	if hyperbolic.VMConfigID == "" {
		hyperbolic.VMConfigID = "c6fd6253-cbb6-4ea8-a20c-47644b431f1c"
	}
	if hyperbolic.GPUCount == 0 {
		hyperbolic.GPUCount = 1
	}
	if hyperbolic.GPUType == "" {
		hyperbolic.GPUType = "H100-SXM5-80GB"
	}
	if hyperbolic.SSHUser == "" {
		hyperbolic.SSHUser = "ubuntu"
	}
	if hyperbolic.PollIntervalSeconds == 0 {
		hyperbolic.PollIntervalSeconds = 30
	}
	if hyperbolic.TerminateOnCompletion == nil {
		defaultTerminate := true
		hyperbolic.TerminateOnCompletion = &defaultTerminate
	}
	if hyperbolic.APITimeoutSeconds == 0 {
		hyperbolic.APITimeoutSeconds = 30
	}
	if hyperbolic.SSHConnectTimeoutSecs == 0 {
		hyperbolic.SSHConnectTimeoutSecs = 10
	}
	if hyperbolic.SSHReadyTimeoutSeconds == 0 {
		hyperbolic.SSHReadyTimeoutSeconds = 600
	}
	return hyperbolic
}

func resolveChinaCloud(china ChinaCloudConfig) ChinaCloudConfig {
	china.AlibabaCloud = resolveChinaCloudProvider(mergeChinaCloudProvider(china.Common, china.AlibabaCloud))
	china.HuaweiCloud = resolveChinaCloudProvider(mergeChinaCloudProvider(china.Common, china.HuaweiCloud))
	china.TencentCloud = resolveChinaCloudProvider(mergeChinaCloudProvider(china.Common, china.TencentCloud))
	china.TianyiCloud = resolveChinaCloudProvider(mergeChinaCloudProvider(china.Common, china.TianyiCloud))
	china.BaiduAICloud = resolveChinaCloudProvider(mergeChinaCloudProvider(china.Common, china.BaiduAICloud))
	return china
}

func mergeChinaCloudProvider(common ChinaCloudProviderConfig, provider ChinaCloudProviderConfig) ChinaCloudProviderConfig {
	out := common
	if provider.Region != "" {
		out.Region = provider.Region
	}
	if provider.Zone != "" {
		out.Zone = provider.Zone
	}
	if provider.InstanceType != "" {
		out.InstanceType = provider.InstanceType
	}
	if provider.ImageID != "" {
		out.ImageID = provider.ImageID
	}
	if provider.VPCID != "" {
		out.VPCID = provider.VPCID
	}
	if provider.SubnetID != "" {
		out.SubnetID = provider.SubnetID
	}
	if provider.SecurityGroupID != "" {
		out.SecurityGroupID = provider.SecurityGroupID
	}
	if provider.SSHKeyName != "" {
		out.SSHKeyName = provider.SSHKeyName
	}
	if provider.SSHPrivateKey != "" {
		out.SSHPrivateKey = provider.SSHPrivateKey
	}
	if provider.SSHUser != "" {
		out.SSHUser = provider.SSHUser
	}
	if provider.Endpoint != "" {
		out.Endpoint = provider.Endpoint
	}
	if provider.ProjectID != "" {
		out.ProjectID = provider.ProjectID
	}
	if provider.AccountID != "" {
		out.AccountID = provider.AccountID
	}
	if provider.SystemDiskCategory != "" {
		out.SystemDiskCategory = provider.SystemDiskCategory
	}
	if provider.SystemDiskSizeGB != 0 {
		out.SystemDiskSizeGB = provider.SystemDiskSizeGB
	}
	if provider.InternetBandwidthMbps != 0 {
		out.InternetBandwidthMbps = provider.InternetBandwidthMbps
	}
	if provider.PollIntervalSeconds != 0 {
		out.PollIntervalSeconds = provider.PollIntervalSeconds
	}
	if provider.TerminateOnCompletion != nil {
		out.TerminateOnCompletion = provider.TerminateOnCompletion
	}
	if provider.KeepInstanceOnFailure {
		out.KeepInstanceOnFailure = true
	}
	if provider.APITimeoutSeconds != 0 {
		out.APITimeoutSeconds = provider.APITimeoutSeconds
	}
	if provider.SSHConnectTimeoutSecs != 0 {
		out.SSHConnectTimeoutSecs = provider.SSHConnectTimeoutSecs
	}
	if provider.SSHReadyTimeoutSeconds != 0 {
		out.SSHReadyTimeoutSeconds = provider.SSHReadyTimeoutSeconds
	}
	if provider.EstimateHourlyUSD != 0 {
		out.EstimateHourlyUSD = provider.EstimateHourlyUSD
	}
	return out
}

func resolveChinaCloudProvider(provider ChinaCloudProviderConfig) ChinaCloudProviderConfig {
	if provider.SSHUser == "" {
		provider.SSHUser = "root"
	}
	if provider.PollIntervalSeconds == 0 {
		provider.PollIntervalSeconds = 30
	}
	if provider.TerminateOnCompletion == nil {
		defaultTerminate := true
		provider.TerminateOnCompletion = &defaultTerminate
	}
	if provider.APITimeoutSeconds == 0 {
		provider.APITimeoutSeconds = 30
	}
	if provider.SSHConnectTimeoutSecs == 0 {
		provider.SSHConnectTimeoutSecs = 10
	}
	if provider.SSHReadyTimeoutSeconds == 0 {
		provider.SSHReadyTimeoutSeconds = 600
	}
	return provider
}

func validateResolvedConfig(provider string, job app.JobSpec, staging StagingConfig, packaging PackagingConfig, gcp GCPConfig, lambda LambdaConfig, hyperbolic HyperbolicConfig, china ChinaCloudConfig) error {
	if err := validateStagingConfig(staging); err != nil {
		return err
	}
	return validateProviderConfig(provider, job, packaging, gcp, lambda, hyperbolic, china)
}

func validateStagingConfig(staging StagingConfig) error {
	for name, value := range map[string]string{
		"staging.checkpoint_uri_prefix": staging.CheckpointURIPrefix,
		"staging.data_uri_prefix":       staging.DataURIPrefix,
	} {
		if value == "" {
			continue
		}
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("%s must be an object-store URI prefix", name)
		}
		switch parsed.Scheme {
		case "s3", "gs":
		default:
			return fmt.Errorf("%s only supports s3:// or gs:// prefixes today", name)
		}
	}
	return nil
}

func validateProviderConfig(provider string, job app.JobSpec, packaging PackagingConfig, gcp GCPConfig, lambda LambdaConfig, hyperbolic HyperbolicConfig, china ChinaCloudConfig) error {
	switch provider {
	case "gcp":
		return validateGCPConfig(job, packaging, gcp)
	case "lambda":
		return validateLambdaConfig(job, packaging, lambda)
	case "hyperbolic":
		return validateHyperbolicConfig(job, packaging, hyperbolic)
	case "alibaba-cloud":
		return validateChinaCloudConfig(provider, job, china.AlibabaCloud)
	case "huawei-cloud":
		return validateChinaCloudConfig(provider, job, china.HuaweiCloud)
	case "tencent-cloud":
		return validateChinaCloudConfig(provider, job, china.TencentCloud)
	case "tianyi-cloud":
		return validateChinaCloudConfig(provider, job, china.TianyiCloud)
	case "baidu-ai-cloud":
		return validateChinaCloudConfig(provider, job, china.BaiduAICloud)
	default:
		return nil
	}
}

func validateGCPConfig(job app.JobSpec, packaging PackagingConfig, gcp GCPConfig) error {
	var reasons []string
	if job.Image == "" && !canPackageForGCP(job, packaging, gcp) {
		reasons = append(reasons, "gcp provider requires job.image or packaging config for job.script")
	}
	if gcp.ProjectID == "" {
		reasons = append(reasons, "gcp.project_id is required")
	}
	if gcp.Location == "" {
		reasons = append(reasons, "gcp.location is required")
	}
	if gcp.OutputURIPrefix == "" {
		reasons = append(reasons, "gcp.output_uri_prefix is required")
	}
	if len(reasons) > 0 {
		return errors.New(strings.Join(reasons, "; "))
	}
	return nil
}

func validateLambdaConfig(job app.JobSpec, packaging PackagingConfig, lambda LambdaConfig) error {
	var reasons []string
	if job.Image == "" && !canPackageWithExplicitImage(job, packaging) {
		reasons = append(reasons, "lambda provider requires job.image or packaging.image for job.script")
	}
	if job.Script != "" && !canPackageWithExplicitImage(job, packaging) {
		reasons = append(reasons, "lambda provider v1 does not package local scripts; provide job.image")
	}
	if lambda.RegionName == "" {
		reasons = append(reasons, "lambda.region_name is required")
	}
	if lambda.InstanceTypeName == "" {
		reasons = append(reasons, "lambda.instance_type_name is required")
	}
	if lambda.SSHKeyName == "" {
		reasons = append(reasons, "lambda.ssh_key_name is required")
	}
	if lambda.SSHPrivateKey == "" {
		reasons = append(reasons, "lambda.ssh_private_key is required")
	}
	reasons = append(reasons, validateRegistryAuthConfig("lambda", lambda.RegistryAuth)...)
	if len(reasons) > 0 {
		return errors.New(strings.Join(reasons, "; "))
	}
	return nil
}

func validateHyperbolicConfig(job app.JobSpec, packaging PackagingConfig, hyperbolic HyperbolicConfig) error {
	var reasons []string
	if job.Image == "" && !canPackageWithExplicitImage(job, packaging) {
		reasons = append(reasons, "hyperbolic provider requires job.image or packaging.image for job.script")
	}
	if job.Script != "" && !canPackageWithExplicitImage(job, packaging) {
		reasons = append(reasons, "hyperbolic provider v1 does not package local scripts; provide job.image")
	}
	if hyperbolic.VMConfigID == "" {
		reasons = append(reasons, "hyperbolic.vm_config_id is required")
	}
	if hyperbolic.GPUCount <= 0 {
		reasons = append(reasons, "hyperbolic.gpu_count must be greater than 0")
	}
	if hyperbolic.SSHPrivateKey == "" {
		reasons = append(reasons, "hyperbolic.ssh_private_key is required")
	}
	reasons = append(reasons, validateRegistryAuthConfig("hyperbolic", hyperbolic.RegistryAuth)...)
	if len(reasons) > 0 {
		return errors.New(strings.Join(reasons, "; "))
	}
	return nil
}

func validateRegistryAuthConfig(provider string, auth RegistryAuthConfig) []string {
	if auth.Server == "" && auth.UsernameEnv == "" && auth.PasswordEnv == "" {
		return nil
	}
	var reasons []string
	prefix := provider + ".registry_auth"
	if auth.Server == "" {
		reasons = append(reasons, prefix+".server is required when registry auth is configured")
	}
	if auth.UsernameEnv == "" {
		reasons = append(reasons, prefix+".username_env is required when registry auth is configured")
	}
	if auth.PasswordEnv == "" {
		reasons = append(reasons, prefix+".password_env is required when registry auth is configured")
	}
	if auth.UsernameEnv != "" && os.Getenv(auth.UsernameEnv) == "" {
		reasons = append(reasons, fmt.Sprintf("%s.username_env %s is empty or unset", prefix, auth.UsernameEnv))
	}
	if auth.PasswordEnv != "" && os.Getenv(auth.PasswordEnv) == "" {
		reasons = append(reasons, fmt.Sprintf("%s.password_env %s is empty or unset", prefix, auth.PasswordEnv))
	}
	return reasons
}

func validateChinaCloudConfig(provider string, job app.JobSpec, cfg ChinaCloudProviderConfig) error {
	var reasons []string
	if job.Image == "" {
		reasons = append(reasons, provider+" provider requires job.image")
	}
	if job.Script != "" {
		reasons = append(reasons, provider+" provider v1 does not package local scripts; provide job.image")
	}
	if cfg.Region == "" {
		reasons = append(reasons, "china_cloud."+providerConfigKey(provider)+".region is required")
	}
	if cfg.InstanceType == "" {
		reasons = append(reasons, "china_cloud."+providerConfigKey(provider)+".instance_type is required")
	}
	if cfg.ImageID == "" {
		reasons = append(reasons, "china_cloud."+providerConfigKey(provider)+".image_id is required")
	}
	if cfg.VPCID == "" {
		reasons = append(reasons, "china_cloud."+providerConfigKey(provider)+".vpc_id is required")
	}
	if cfg.SubnetID == "" {
		reasons = append(reasons, "china_cloud."+providerConfigKey(provider)+".subnet_id is required")
	}
	if provider == "huawei-cloud" && cfg.ProjectID == "" && cfg.AccountID == "" {
		reasons = append(reasons, "china_cloud.huawei_cloud.project_id is required")
	}
	if cfg.SecurityGroupID == "" {
		reasons = append(reasons, "china_cloud."+providerConfigKey(provider)+".security_group_id is required")
	}
	if cfg.SSHKeyName == "" {
		reasons = append(reasons, "china_cloud."+providerConfigKey(provider)+".ssh_key_name is required")
	}
	if cfg.SSHPrivateKey == "" {
		reasons = append(reasons, "china_cloud."+providerConfigKey(provider)+".ssh_private_key is required")
	}
	if len(reasons) > 0 {
		return errors.New(strings.Join(reasons, "; "))
	}
	return nil
}

func providerConfigKey(provider string) string {
	return strings.ReplaceAll(provider, "-", "_")
}

func canPackageForGCP(job app.JobSpec, packaging PackagingConfig, gcp GCPConfig) bool {
	if job.Script == "" {
		return false
	}
	if packaging.Image != "" {
		return true
	}
	return gcp.ProjectID != "" && gcp.Location != "" && gcp.ArtifactRegistryRepository != ""
}

func canPackageWithExplicitImage(job app.JobSpec, packaging PackagingConfig) bool {
	return job.Script != "" && packaging.Image != ""
}

func LoadFile(path string) (Config, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(content, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

func cfgProviderDefault(flagProvider string) string {
	if flagProvider != "" {
		return flagProvider
	}
	return ""
}

func cloneMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
