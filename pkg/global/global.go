package global

import "os"

var (
	Version                 = "dev"
	Commit                  = ""
	BuildTime               = "none"
	Debug                   = false
	ProfilingEnabled        = false
	ReplicateRegistryHost   = "r8.im"
	ReplicateWebsiteHost    = "replicate.com"
	ShengsuanRegistryHost   = "registry.shengsuanyun.com"
	ShengsuanApiHost        = "api.shengsuanyun.com"
	ShengsuanWebsiteHost    = "www.shengsuanyun.com"
	LabelNamespace          = "run.ssy."
	CogBuildArtifactsFolder = ".cog"
	CogBaseImageName        = "cog-base"

	// Supported registry hosts for authentication
	SupportedRegistries = []string{ReplicateRegistryHost, ShengsuanRegistryHost}

	// SSY related constants
	SetDefaultARCH           = "ARG ARCH=amd64"
	ShengsuanOSSAddress      = "https://shengsuanyun.oss-cn-shanghai.aliyuncs.com"
	ShengsuanOSSBucketName   = "/ssy"
	ShengsuanOSSPyLibSrcName = "/ssy-0.16.1.1-py3-none-any.whl"
	ShengsuanPyLibDistName   = "ssy-0.16.1.dev2-py3-none-any.whl"
	ShengsuanPyLibAddress    = ShengsuanOSSAddress + ShengsuanOSSBucketName + ShengsuanOSSPyLibSrcName
	AliyunIndexURL           = "https://mirrors.aliyun.com/pypi/simple"
)

// Initialize sets up global variables based on environment variables
func Initialize() {
	mirror := os.Getenv("MIRROR")

	if mirror == "cn" {
		// Use China mirror configuration
		ReplicateRegistryHost = "registry.cn-shanghai.aliyuncs.com/shengsuan"
		CogBaseImageName = "ssy-base"
		ShengsuanRegistryHost = "registry.shengsuanyun.com"
	} else {
		// Use default configuration
		ReplicateRegistryHost = "r8.im"
		CogBaseImageName = "cog-base"
		ShengsuanRegistryHost = "150605664230.dkr.ecr.us-east-1.amazonaws.com"
	}

	// Update SupportedRegistries after changing ReplicateRegistryHost
	SupportedRegistries = []string{ReplicateRegistryHost, ShengsuanRegistryHost}
}
