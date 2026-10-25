//go:build release

package airplane

func (Service) developmentGatewayCommand() (gatewayCommand, bool) {
	return gatewayCommand{}, false
}
