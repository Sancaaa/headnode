package agent

import "testing"

func TestVirtualInterfacesAreLeftOut(t *testing.T) {
	for name, want := range map[string]bool{
		"docker0":                  true,
		"br-3f2a9c1d":              true,
		"veth12ab":                 true,
		"virbr0":                   true,
		"vEthernet (WSL)":          true,
		"vEthernet (Default Switch)": true,
		"VMware Network Adapter VMnet8": true,
		"VirtualBox Host-Only Network": true,
		"eth0":                     false,
		"ens18":                    false,
		"Wi-Fi":                    false,
		"Ethernet":                 false,
		"tailscale0":               false, // a real address an admin may use
		"Tailscale":                false,
	} {
		if got := isVirtualInterface(name); got != want {
			t.Errorf("isVirtualInterface(%q) = %v, want %v", name, got, want)
		}
	}
}
