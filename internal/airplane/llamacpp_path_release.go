//go:build release

package airplane

func (Service) developmentLlamacppPath() (string, bool) {
	return "", false
}
