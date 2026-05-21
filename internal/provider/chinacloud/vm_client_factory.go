package chinacloud

import "net/http"

func newVMClient(def Definition, config VMProviderConfig, httpClient *http.Client) VMClient {
	switch def.Name {
	case "alibaba-cloud":
		return newAlibabaClient(AlibabaConfig{
			RegionID:                 config.Region,
			ZoneID:                   config.Zone,
			InstanceType:             config.InstanceType,
			ImageID:                  config.ImageID,
			SecurityGroupID:          config.SecurityGroupID,
			VSwitchID:                config.SubnetID,
			KeyPairName:              config.SSHKeyName,
			SSHUser:                  config.SSHUser,
			SSHPrivateKey:            config.SSHPrivateKey,
			SystemDiskCategory:       config.SystemDiskType,
			SystemDiskSizeGB:         config.SystemDiskSizeGB,
			InternetMaxBandwidthOut:  config.InternetBandwidthMbps,
			PollIntervalSeconds:      config.PollIntervalSeconds,
			SSHConnectTimeoutSecs:    config.SSHConnectTimeoutSecs,
			SSHReadyTimeoutSeconds:   config.SSHReadyTimeoutSeconds,
			TerminateOnCompletion:    config.TerminateOnCompletion,
			TerminateOnCompletionSet: config.TerminateOnCompletionSet,
			KeepInstanceOnFailure:    config.KeepInstanceOnFailure,
			EstimateHourlyUSD:        config.EstimateHourlyUSD,
			Endpoint:                 config.Endpoint,
			APITimeoutSeconds:        config.APITimeoutSeconds,
			Credentials:              config.Credentials,
			HardwareShapes:           config.HardwareShapes,
		})
	case "huawei-cloud":
		return newHuaweiClient(HuaweiConfig{
			Region:                   config.Region,
			Zone:                     config.Zone,
			ProjectID:                config.ProjectOrAccount,
			FlavorRef:                config.InstanceType,
			ImageRef:                 config.ImageID,
			VPCID:                    config.VPCID,
			SubnetID:                 config.SubnetID,
			SecurityGroupID:          config.SecurityGroupID,
			KeyName:                  config.SSHKeyName,
			SSHUser:                  config.SSHUser,
			SSHPrivateKey:            config.SSHPrivateKey,
			RootVolumeType:           config.SystemDiskType,
			RootVolumeSizeGB:         config.SystemDiskSizeGB,
			PollIntervalSeconds:      config.PollIntervalSeconds,
			SSHConnectTimeoutSecs:    config.SSHConnectTimeoutSecs,
			SSHReadyTimeoutSeconds:   config.SSHReadyTimeoutSeconds,
			TerminateOnCompletion:    config.TerminateOnCompletion,
			TerminateOnCompletionSet: config.TerminateOnCompletionSet,
			KeepInstanceOnFailure:    config.KeepInstanceOnFailure,
			EstimateHourlyUSD:        config.EstimateHourlyUSD,
			Endpoint:                 config.Endpoint,
			APITimeoutSeconds:        config.APITimeoutSeconds,
			Credentials:              config.Credentials,
			HardwareShapes:           config.HardwareShapes,
		})
	case "tencent-cloud":
		return TencentVMClient{
			Endpoint:    config.Endpoint,
			Region:      config.Region,
			Credentials: config.Credentials,
			Client:      httpClient,
		}
	case "tianyi-cloud":
		return TianyiVMClient{
			Endpoint:    config.Endpoint,
			RegionID:    config.Region,
			AZName:      config.Zone,
			Credentials: config.Credentials,
			Client:      httpClient,
		}
	case "baidu-ai-cloud":
		return BaiduVMClient{
			Endpoint:    config.Endpoint,
			Credentials: config.Credentials,
			Client:      httpClient,
		}
	default:
		return nil
	}
}
