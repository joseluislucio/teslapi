//go:build dev

package main

import (
	"flag"
	"path/filepath"

	"teslapi/internal/config"
	"teslapi/internal/gadget"
	"teslapi/internal/nas"
	"teslapi/internal/presence"
	"teslapi/internal/snapshot"
	"teslapi/internal/sysupdate"
	"teslapi/internal/wifi"
)

var devSMBPort = flag.String("dev-smb-port", "", "puerto SMB alternativo para probar el destino sin un servidor real")

// devPlatform: controladores falsos para trabajar en el Mac. No entra en el binario de la Pi.
func devPlatform(o *opts) (platform, error) {
	if *devSMBPort != "" {
		nas.Port = *devSMBPort
	}
	o.state = filepath.Join(o.dev, "state")
	o.bootFile = filepath.Join(o.dev, "boot", "teslapi.txt")
	fake := &presence.Fake{Others: []presence.Sighting{
		{Address: "AA:BB:CC:00:00:01", Name: "", RSSI: -81},
		{Address: "AA:BB:CC:00:00:02", Name: "Tesla Model 3", RSSI: -54},
	}}
	return platform{scanner: fake, wifi: &wifi.Fake{Networks: []wifi.Network{
		{SSID: "Garaje", Strength: 82, Secure: true}, {SSID: "MOVISTAR_4F2A", Strength: 61, Secure: true},
		{SSID: "Cafeteria Invitados", Strength: 44}, {SSID: "DIRECT-roku-3B7", Strength: 28, Secure: true}}},
		gadget: &gadget.Fake{}, macSuffix: func() string { return "DEV0" },
		snap: &snapshot.Fake{Root: o.dev}, nas: &nas.Local{Path: filepath.Join(o.dev, "nas")},
		updates: &sysupdate.FakeRunner{Speed: 1},
		devCharger: func(c config.Charger, present bool) {
			if !present {
				fake.Set(nil)
				return
			}
			fake.Set(&presence.Sighting{Address: c.Address, Name: c.Name, RSSI: -62})
		},
	}, nil
}
