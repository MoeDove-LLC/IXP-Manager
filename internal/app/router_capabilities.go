package app

import (
	"encoding/json"
	"os/exec"

	"github.com/moedove/ixp-manager/internal/routing"
)

func (a *App) routerValidatorBinary(platform string) string {
	var configured, fallback string
	switch platform {
	case "", "bird2":
		configured, fallback = a.birdBinary, "bird"
	case "bird3":
		configured, fallback = a.bird3Binary, "bird3"
	case "quagga":
		configured, fallback = a.quaggaBinary, "quagga-bgpd"
	case "frr":
		configured, fallback = a.frrBinary, "bgpd"
	case "openbgpd":
		configured, fallback = a.openBGPDBinary, "openbgpd"
	default:
		return ""
	}
	if configured != "" {
		return configured
	}
	return fallback
}

func (a *App) platformCapabilities() []Record {
	items := []Record{}
	for _, capability := range routing.Capabilities() {
		encoded, _ := json.Marshal(capability)
		var item Record
		_ = json.Unmarshal(encoded, &item)
		available := capability.Validate
		if capability.Platform == "bird1" {
			families := a.bird1CheckerAvailability()
			item["singleAddressFamily"] = true
			item["nativeCheckerFamilies"] = families
			available = families["ipv4"] == true && families["ipv6"] == true
		} else if capability.ValidationLevel == "native" {
			_, err := exec.LookPath(a.routerValidatorBinary(capability.Platform))
			available = err == nil
		}
		item["nativeCheckerAvailable"] = available
		items = append(items, item)
	}
	return items
}
