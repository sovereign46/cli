//go:build release

package airplane

func (Service) seamInstallLlamacpp() (bool, error)         { return false, nil }
func (Service) seamInstallHuggingFaceCLI() (bool, error)   { return false, nil }
func (Service) seamPullModel() (bool, error)               { return false, nil }
func (Service) seamStartLlamacpp() (bool, error)           { return false, nil }
func (Service) seamStartGateway() (bool, error)            { return false, nil }
func (Service) seamInstallGateway() (bool, error)          { return false, nil }
func (Service) seamLlamacppRunning() (bool, bool)          { return false, false }
func (Service) seamModelDownloaded() (bool, bool)          { return false, false }
func (Service) seamModelProbe() (bool, string, bool)       { return false, "", false }
func (Service) seamGatewayReady() (bool, bool)             { return false, false }
func (Service) seamGatewayResponding() (bool, bool)        { return false, false }
func (Service) seamGatewayDownloadAvailable() (bool, bool) { return false, false }
func (Service) seamHomebrewAvailable() (bool, bool)        { return false, false }
func (Service) seamLlamacppPath() (string, bool, bool)     { return "", false, false }
func (Service) seamHuggingFaceCLIPath() (string, bool, bool) {
	return "", false, false
}
func (Service) seamGatewayBinary() (string, bool, bool) { return "", false, false }
func (Service) seamLlamacppServeProcess() (llamacppProcess, bool, bool) {
	return llamacppProcess{}, false, false
}
func (Service) seamAdvertisedLlamacppModels() ([]string, bool) { return nil, false }
func (Service) seamMemoryBytes() (int64, bool)                 { return 0, false }
func (Service) seamFreeDiskBytes() (int64, bool)               { return 0, false }
