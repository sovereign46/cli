//go:build release

// Release-build stubs for the test seams declared in testseams_dev.go.
// Every method returns "not handled" / "no override", so the release
// binary contains no S46_TEST_* string literals and never reads any
// such env var from the surrounding shell.

package airplane

func (Service) seamInstallOllama() (bool, error)                                   { return false, nil }
func (Service) seamPullModel() (bool, error)                                       { return false, nil }
func (Service) seamStartOllama() (bool, error)                                     { return false, nil }
func (Service) seamConfigureLaunchctl([]OllamaEnvSetting) (bool, error)            { return false, nil }
func (Service) seamStopLoadedModel() (bool, error)                                 { return false, nil }
func (Service) seamStartGateway() (bool, error)                                    { return false, nil }
func (Service) seamInstallGateway() (bool, error)                                  { return false, nil }
func (Service) seamOllamaRunning() (bool, bool)                                    { return false, false }
func (Service) seamModelDownloaded() (bool, bool)                                  { return false, false }
func (Service) seamModelProbe() (bool, string, bool)                               { return false, "", false }
func (Service) seamGatewayReady() (bool, bool)                                     { return false, false }
func (Service) seamGatewayResponding() (bool, bool)                                { return false, false }
func (Service) seamGatewayDownloadAvailable() (bool, bool)                         { return false, false }
func (Service) seamHomebrewAvailable() (bool, bool)                                { return false, false }
func (Service) seamOllamaPath() (string, bool, bool)                               { return "", false, false }
func (Service) seamGatewayBinary() (string, bool, bool)                            { return "", false, false }
func (Service) seamLaunchctlEnv() (map[string]string, bool, bool)                  { return nil, false, false }
func (Service) seamOllamaServeProcess() (ollamaProcess, bool, bool)                { return ollamaProcess{}, false, false }
func (Service) seamOllamaProcessEnv() (map[string]string, bool, bool)              { return nil, false, false }
func (Service) seamInstalledOllamaModels() ([]string, bool)                        { return nil, false }
func (Service) seamLoadedOllamaModels(string) ([]OllamaLoadedModel, bool)          { return nil, false }
func (Service) seamLoadedBackendModelContext() (int, bool)                         { return 0, false }
func (Service) seamMemoryBytes() (int64, bool)                                     { return 0, false }
func (Service) seamFreeDiskBytes() (int64, bool)                                   { return 0, false }
func (Service) seamShouldUseLaunchctlEnv() bool                                    { return false }
