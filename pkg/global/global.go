package global

var (
	Version                 = "dev"
	Commit                  = ""
	BuildTime               = "none"
	Debug                   = false
	ProfilingEnabled        = false
	ReplicateRegistryHost   = "registry.shengsuanyun.com/ssy"
	ReplicateWebsiteHost    = "replicate.com"
	ShengsuanRegistryHost   = "registry.shengsuanyun.com"
	ShengsuanApiHost        = "api.shengsuanyun.com"
	ShengsuanWebsiteHost    = "www.shengsuanyun.com"
	LabelNamespace          = "run.cog."
	CogBuildArtifactsFolder = ".cog"

	// SSY related constants
	SetDefaultARCH           = "ARG ARCH=amd64"
	ShengsuanOSSAddress      = "https://shengsuanyun.oss-cn-shanghai.aliyuncs.com"
	ShengsuanOSSBucketName   = "/ssy"
	ShengsuanOSSPyLibSrcName = "/ssy-0.16.1-py3-none-${ARCH}.whl"
	ShengsuanPyLibDistName   = "ssy-0.16.1.dev2-py3-none-any.whl"
	ShengsuanPyLibAddress    = ShengsuanOSSAddress + ShengsuanOSSBucketName + ShengsuanOSSPyLibSrcName
	AliyunIndexURL           = "https://mirrors.aliyun.com/pypi/simple"
)
