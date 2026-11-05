//go:build release

package airplane

import "errors"

func (Service) developmentGatewayCommand() (gatewayCommand, bool) {
	return gatewayCommand{}, false
}

func (Service) startDevelopmentGateway(gatewayCommand, []string) error {
	return errors.New("release build requires an exact qualified gateway pin")
}
