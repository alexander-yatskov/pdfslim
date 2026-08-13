package pdfconfig

import (
	"sync"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

var disableConfigDir sync.Once

// New returns an offline configuration without creating data in the user's
// profile. pdfcpu stores its config path globally, so initialize it once.
func New() *model.Configuration {
	disableConfigDir.Do(api.DisableConfigDir)
	conf := model.NewDefaultConfiguration()
	conf.Offline = true
	return conf
}
