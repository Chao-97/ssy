package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/replicate/go/uuid"

	"github.com/replicate/cog/pkg/coglog"
	"github.com/replicate/cog/pkg/config"
	"github.com/replicate/cog/pkg/docker"
	"github.com/replicate/cog/pkg/global"
	coghttp "github.com/replicate/cog/pkg/http"
	"github.com/replicate/cog/pkg/image"
	"github.com/replicate/cog/pkg/registry"
	"github.com/replicate/cog/pkg/util/console"
)

// buildFullImageName constructs the full image name by prepending the appropriate registry host
// based on the MIRROR environment variable if the imageName doesn't already contain a registry host
func buildFullImageName(imageName string) string {
	// If the image name already contains a registry host (has a slash and domain-like prefix), return as-is
	if strings.Contains(imageName, "/") && (strings.Contains(imageName, ".") || strings.Contains(imageName, ":")) {
		return imageName
	}

	// Get the appropriate registry host based on MIRROR environment variable
	mirror := os.Getenv("MIRROR")
	var registryHost string
	if mirror == "" {
		mirror = "cn"
	}
	if mirror == "cn" {
		registryHost = global.ShengsuanRegistryHost
	} else {
		registryHost = global.ReplicateRegistryHost
	}

	// Prepend the registry host to the image name
	return fmt.Sprintf("%s/%s", registryHost, imageName)
}

type VerifyModel struct {
	Username  string `json:"user_name"`
	Modelname string `json:"model_name"`
}
type ModelResponse struct {
	Code    int    `json:"code"`
	Massage string `json:"msg"`
}

var pipelinesImage bool

func newPushCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "push [IMAGE]",

		Short: "Build and push model in current directory to a Docker registry",
		Example: `ssy push your-username/model-name:tag

The registry host will be automatically selected based on the MIRROR environment variable:
- MIRROR=cn: Uses registry.shengsuanyun.com
- Default: Uses 150605664230.dkr.ecr.us-east-1.amazonaws.com

You can also specify the full registry URL if needed:
ssy push registry.shengsuanyun.com/your-username/model-name:tag`,
		RunE: push,
		Args: cobra.MaximumNArgs(1),
	}
	addSecretsFlag(cmd)
	addNoCacheFlag(cmd)
	addSeparateWeightsFlag(cmd)
	addSchemaFlag(cmd)
	addUseCudaBaseImageFlag(cmd)
	addDockerfileFlag(cmd)
	addBuildProgressOutputFlag(cmd)
	addUseCogBaseImageFlag(cmd)
	addStripFlag(cmd)
	addPrecompileFlag(cmd)
	addFastFlag(cmd)
	addLocalImage(cmd)
	addConfigFlag(cmd)
	addPipelineImage(cmd)

	return cmd
}

func push(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	dockerClient, err := docker.NewClient(ctx)
	if err != nil {
		return err
	}

	client, err := coghttp.ProvideHTTPClient(ctx, dockerClient)
	if err != nil {
		return err
	}
	logClient := coglog.NewClient(client)
	logCtx := logClient.StartPush(buildLocalImage)

	cfg, projectDir, err := config.GetConfig(configFilename)
	if err != nil {
		logClient.EndPush(ctx, err, logCtx)
		return err
	}
	// In case one of `--x-fast` & `fast: bool` is set
	if cfg.Build.Fast {
		buildFast = cfg.Build.Fast
	}
	logCtx.Fast = buildFast
	logCtx.CogRuntime = cfg.Build.CogRuntime

	imageName := cfg.Image
	if len(args) > 0 {
		imageName = args[0]
	}

	if imageName == "" {
		err = fmt.Errorf("To push images, you must either set the 'image' option in ssy.yaml or pass an image name as an argument. For example, 'ssy push your-username/hotdog-detector'")
		logClient.EndPush(ctx, err, logCtx)
		return err
	}

	// Auto-prepend registry host if not already present
	fullImageName := buildFullImageName(imageName)
	console.Debugf("Original image name: %s, Full image name: %s", imageName, fullImageName)

	shengsuanPrefix := fmt.Sprintf("%s/", global.ShengsuanRegistryHost)
	if strings.HasPrefix(fullImageName, shengsuanPrefix) {
		// 从胜算云获取model是否存在
		var datas = strings.Split(fullImageName, "/")
		if ok, err := verifyModel(datas[1], datas[2]); err != nil {
			err = fmt.Errorf("Unable to find Shengsuan existing model for %s. Go to shengsuanyun.com and create a new model before pushing.", fullImageName)
			logClient.EndPush(ctx, err, logCtx)
			return err
		} else if !ok {
			err = fmt.Errorf("Unable to find Shengsuan existing model for %s. Go to shengsuanyun.com and create a new model before pushing.", fullImageName)
			logClient.EndPush(ctx, err, logCtx)
			return err
		}
	}

	if !strings.HasPrefix(fullImageName, shengsuanPrefix) && buildLocalImage {
		err = fmt.Errorf("Unable to push a local image model to a non shengsuan host, please disable the local image flag before pushing to this host.")
		logClient.EndPush(ctx, err, logCtx)
		return err
	}

	annotations := map[string]string{}
	buildID, err := uuid.NewV7()
	if err != nil {
		// Don't insert build ID but continue anyways
		console.Debugf("Failed to create build ID %v", err)
	} else {
		annotations["run.ssy.push_id"] = buildID.String()
	}

	startBuildTime := time.Now()
	registryClient := registry.NewRegistryClient()
	if err := image.Build(
		ctx,
		cfg,
		projectDir,
		fullImageName,
		buildSecrets,
		buildNoCache,
		buildSeparateWeights,
		buildUseCudaBaseImage,
		buildProgressOutput,
		buildSchemaFile,
		buildDockerfileFile,
		DetermineUseCogBaseImage(cmd),
		buildStrip,
		buildPrecompile,
		buildFast,
		annotations,
		buildLocalImage,
		dockerClient,
		registryClient,
		pipelinesImage); err != nil {
		logClient.EndPush(ctx, err, logCtx)
		return err
	}

	buildDuration := time.Since(startBuildTime)

	console.Infof("\nPushing image '%s'...", fullImageName)
	if buildFast {
		console.Info("Fast push enabled.")
	}

	err = docker.Push(ctx, fullImageName, buildFast, projectDir, dockerClient, docker.BuildInfo{
		BuildTime: buildDuration,
		BuildID:   buildID.String(),
		Pipeline:  pipelinesImage,
	}, client, cfg)
	if err != nil {
		if strings.Contains(err.Error(), "NAME_UNKNOWN") || strings.Contains(err.Error(), "404") {
			hostName := "Shengsuan"
			websiteHost := "shengsuan.com"
			err = fmt.Errorf("Unable to find existing %s model for %s. "+
				"Go to %s and create a new model before pushing."+
				"\n\n"+
				"If the model already exists, you may be getting this error "+
				"because you're not logged in as owner of the model. "+
				"This can happen if you did `sudo ssy login` instead of `ssy login` "+
				"or `sudo ssy push` instead of `ssy push`, "+
				"which causes Docker to use the wrong Docker credentials.",
				hostName, fullImageName, websiteHost)
			logClient.EndPush(ctx, err, logCtx)
			return err
		}
		err = fmt.Errorf("Failed to push image: %w", err)
		logClient.EndPush(ctx, err, logCtx)
		return err
	}

	console.Infof("Image '%s' pushed", fullImageName)
	if strings.HasPrefix(fullImageName, shengsuanPrefix) {
		shengsuanPage := fmt.Sprintf("https://%s", strings.Replace(fullImageName, global.ShengsuanRegistryHost, global.ShengsuanWebsiteHost, 1))
		console.Infof("\nRun your model on Shengsuan:\n    %s", shengsuanPage)
	}
	logClient.EndPush(ctx, nil, logCtx)

	return nil
}

// 验证token
func verifyModel(project string, imageName string) (bool, error) {
	url := "https://" + global.ShengsuanApiHost + "/v2/model/getmodel"
	modelNames := strings.Split(imageName, ":")
	var reqData VerifyModel
	reqData.Username = project
	reqData.Modelname = modelNames[0]
	bodyBytes, _ := json.Marshal(reqData)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return false, fmt.Errorf("Failed to create HTTP request: %w", err)
	}
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("Failed to send HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("Received non-OK HTTP status: %d", resp.StatusCode)
	}

	var modelResq ModelResponse
	if err := json.NewDecoder(resp.Body).Decode(&modelResq); err != nil {
		return false, fmt.Errorf("Failed to decode response JSON: %w", err)
	}

	if modelResq.Code != 0 {
		return false, fmt.Errorf("Get model Failed with code: %d", modelResq.Code)
	}

	return true, nil
}

func addPipelineImage(cmd *cobra.Command) {
	const pipeline = "x-pipeline"
	cmd.Flags().BoolVar(&pipelinesImage, pipeline, false, "Whether to use the experimental pipeline feature")
	_ = cmd.Flags().MarkHidden(pipeline)
}
