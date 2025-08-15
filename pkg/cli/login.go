package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/replicate/cog/pkg/docker"
	"github.com/replicate/cog/pkg/global"
	"github.com/replicate/cog/pkg/util/console"
)

//	type VerifyResponse struct {
//		Username string `json:"username"`
//	}
type LoginResponse struct {
	Code int `json:"code"`
	Data struct {
		UserName    string `json:"user_name"`
		AccessToken string `json:"access_token"`
	} `json:"data"`
}

func newLoginCommand() *cobra.Command {
	var cmd = &cobra.Command{
		Use:        "login",
		SuggestFor: []string{"auth", "authenticate", "authorize"},
		Short:      "Log in to Shengsuan Docker registry",
		RunE:       login,
		Args:       cobra.MaximumNArgs(0),
	}

	cmd.Flags().Bool("token-stdin", false, "Pass login token on stdin instead of opening a browser. You can find your Shengsuan login token at https://shengsuan.com/auth/token")
	cmd.Flags().String("registry", global.ShengsuanRegistryHost, "Registry host")
	_ = cmd.Flags().MarkHidden("registry")

	return cmd
}

// ssy login 命令在这里调用,自动一个网页获取token粘贴在这里
func login(cmd *cobra.Command, args []string) error {
	registryHost, err := cmd.Flags().GetString("registry")
	if err != nil {
		return err
	}
	tokenStdin, err := cmd.Flags().GetBool("token-stdin")
	if err != nil {
		return err
	}

	var token string
	if tokenStdin {
		token, err = readTokenFromStdin()
		if err != nil {
			return err
		}
	} else {
		token, err = promptToken()
		if err != nil {
			return err
		}
	}
	token = strings.TrimSpace(token)

	username, accessToken, err := verifyToken(token)
	if err != nil {
		return err
	}

	ctx := cmd.Context()
	if err := docker.SaveLoginToken(ctx, registryHost, username, accessToken); err != nil {
		return err
	}

	// Extract display name based on username format
	var displayName string
	if strings.Contains(username, "+") {
		// Format: robot_username+token (for cn mirror)
		parts := strings.Split(username, "+")
		if len(parts) > 0 && strings.HasPrefix(parts[0], "robot_") {
			displayName = strings.TrimPrefix(parts[0], "robot_")
		} else {
			displayName = parts[0]
		}
	} else {
		// Direct username format (for other cases)
		displayName = username
	}

	console.Infof("You've successfully authenticated as %s! You can now use the '%s' registry.", displayName, registryHost)

	return nil
}

func readTokenFromStdin() (string, error) {
	tokenBytes, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("Failed to read token from stdin: %w", err)
	}
	return string(tokenBytes), nil
}

// 交互式获取token
func promptToken() (string, error) {
	console.Info("Please obtain your login token from https://console.shengsuanyun.com/user/keys")
	console.Info("After copying the token, paste it below and press Enter:")
	fmt.Print("Token: ")
	var token string
	_, err := fmt.Scanln(&token)
	if err != nil {
		return "", fmt.Errorf("Failed to read token from input: %w", err)
	}
	return token, nil
}

// 验证token
func verifyToken(token string) (string, string, error) {
	mirror := os.Getenv("MIRROR")
	if mirror == "" {
		mirror = "cn"
	}
	url := "https://" + global.ShengsuanApiHost + "/v2/user/login?mirror=" + mirror
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", "", fmt.Errorf("Failed to create HTTP request: %w", err)
	}

	req.Header.Set("Authorization", "Token "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("Failed to send HTTP request: %w", err)
	}
	defer resp.Body.Close()

	// 读取响应内容用于调试
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("Failed to read response body: %w", err)
	}

	console.Debugf("Response status: %d", resp.StatusCode)
	console.Debugf("Response body: %s", string(body))

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("Received non-OK HTTP status: %d, body: %s", resp.StatusCode, string(body))
	}

	var loginResp LoginResponse
	if err := json.Unmarshal(body, &loginResp); err != nil {
		return "", "", fmt.Errorf("Failed to decode response JSON: %w. Response body: %s", err, string(body))
	}

	if loginResp.Code != 0 {
		return "", "", fmt.Errorf("Login failed with code: %d", loginResp.Code)
	}

	return loginResp.Data.UserName, loginResp.Data.AccessToken, nil
}

func readTokenInteractively(registryHost string) (string, error) {
	url, err := getDisplayTokenURL(registryHost)
	if err != nil {
		return "", err
	}
	console.Infof("This command will authenticate Docker with Tenisy's '%s' Docker registry. You will need a Shengsuan account.", registryHost)
	console.Info("")

	// TODO(bfirsh): if you have defined a registry in ssy.yaml that is not registry.shengsuanyun.com, suggest to use 'docker login'

	console.Info("Hit enter to get started. A browser will open with an authentication token that you need to paste here.")
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		return "", err
	}

	console.Info("If it didn't open automatically, open this URL in a web browser:")
	console.Info(url)
	maybeOpenBrowser(url)

	console.Info("")
	console.Info("Once you've signed in, copy the authentication token from that web page, paste it here, then hit enter:")
	token, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", err
	}
	return token, nil
}

// 请求后端服务获取展示token的网页url
func getDisplayTokenURL(registryHost string) (string, error) {
	resp, err := http.Get(addressWithScheme(registryHost) + "/get/token")
	if err != nil {
		return "", fmt.Errorf("Failed to log in to %s: %w", registryHost, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("%s is not the Shengsuan registry\nPlease log in using 'docker login'", registryHost)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s returned HTTP status %d", registryHost, resp.StatusCode)
	}
	body := &struct {
		URL string `json:"url"`
	}{}
	fmt.Println(resp.Body)
	if err := json.NewDecoder(resp.Body).Decode(body); err != nil {
		return "", err
	}
	return body.URL, nil
}

func addressWithScheme(address string) string {
	if strings.Contains(address, "://") {
		return address
	}
	return "http://" + address
}

// 检测系统类型用对应命令打开相应网页
func maybeOpenBrowser(url string) {
	switch runtime.GOOS {
	case "linux":
		_ = exec.Command("xdg-open", url).Start()
	case "windows":
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		_ = exec.Command("open", url).Start()
	}
}
