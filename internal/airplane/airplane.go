package airplane

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sovereign46/cli/internal/strs"
)

const (
	ModeCloud                  = "cloud"
	ModeAirplane               = "airplane"
	Prefix                     = "[s46✈]"
	LocalGatewayURL            = "http://127.0.0.1:8080"
	LocalLlamacppURL           = "http://127.0.0.1:8081"
	LocalModelID               = "s46/devstral-small-2-24b"
	BackendModel               = "devstral-small-2:24b-instruct-2512-q4_K_M"
	GGUFModelFile              = "Devstral-Small-2-24B-Instruct-2512-Q4_K_M.gguf"
	GatewayBinaryName          = "s46-gateway"
	DefaultGatewayRepo         = "sovereign46/api"
	DefaultContextWindow       = 65536
	DefaultMaxTokens           = 4096
	DefaultKeepAlive           = "10m"
	DefaultGatewayWriteTimeout = "10m"
	DefaultNumParallel         = 1
	DefaultFlashAttention      = "on"
	DefaultKVCacheType         = "q8_0"
	DefaultGPULayers           = "99"
	MinMemoryBytes             = int64(32 * 1000 * 1000 * 1000)
	RecMemoryBytes             = int64(64 * 1000 * 1000 * 1000)
	MinDiskBytes               = int64(30 * 1000 * 1000 * 1000)
)

const (
	checkTimeout             = 2 * time.Second
	modelProbeTimeout        = 5 * time.Minute
	modelProbeNoticeAfter    = 2 * time.Second
	modelProbeProgressEvery  = time.Second
	modelProbeBodyLimit      = 4 * 1024
	gatewayRequestTimeout    = 30 * time.Second
	gatewaySourceInstallTime = 5 * time.Minute
)

type LogFile struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type Service struct {
	Env               map[string]string
	Stdin             io.Reader
	Stdout            io.Writer
	Stderr            io.Writer
	Progress          io.Writer
	LogPrefix         string
	Client            *http.Client
	CheckTimeout      time.Duration
	ModelProbeTimeout time.Duration
}

func (s Service) LogFiles() []LogFile {
	cache := cacheDir(s.Env)
	return []LogFile{
		{Name: "llamacpp", Path: filepath.Join(cache, "llamacpp.log")},
		{Name: "gateway", Path: filepath.Join(cache, "s46-gateway-airplane.log")},
	}
}

func (s Service) processEnv(extra ...string) []string {
	values := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	for key, value := range s.Env {
		values[key] = value
	}
	for _, entry := range extra {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	env := make([]string, 0, len(values))
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env
}

func cacheDir(env map[string]string) string {
	if value := strings.TrimSpace(strs.EnvValue(env, "S46_LOG_DIR")); value != "" {
		return value
	}
	if value := strings.TrimSpace(strs.EnvValue(env, "XDG_CACHE_HOME")); value != "" {
		return filepath.Join(value, "s46")
	}
	if home := homeDir(env); home != "" {
		return filepath.Join(home, ".cache", "s46")
	}
	return filepath.Join(os.TempDir(), "s46")
}

func dataDir(env map[string]string) string {
	if value := strings.TrimSpace(strs.EnvValue(env, "XDG_DATA_HOME")); value != "" {
		return value
	}
	if home := homeDir(env); home != "" {
		return filepath.Join(home, ".local", "share")
	}
	return os.TempDir()
}

func homeDir(env map[string]string) string {
	if value := strings.TrimSpace(strs.EnvValue(env, "HOME")); value != "" {
		return value
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return ""
}

func (s Service) httpClient() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{}
}

func (s Service) logPrefix() string {
	return strs.FirstNonEmpty(s.LogPrefix, Prefix)
}
