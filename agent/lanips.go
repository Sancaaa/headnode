package agent

import (
	"net"
	"strings"
)

// lanIPs lists the IPv4 addresses of interfaces that are up, except loopback
// and link-local (169.254.x.x), which are not useful to an admin. The same on
// every operating system, so it lives here and not in a per-OS file.
func lanIPs() []string {
	ips := []string{} // not nil, so the JSON is [] and not null
	ifaces, err := net.Interfaces()
	if err != nil {
		return ips
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || isVirtualInterface(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			if ip4 := ipNet.IP.To4(); ip4 != nil && !ip4.IsLinkLocalUnicast() {
				ips = append(ips, ip4.String())
			}
		}
	}
	if len(ips) > maxLANIPs {
		ips = ips[:maxLANIPs]
	}
	return ips
}

// maxLANIPs caps the list: it is shown on one line per host, and a machine
// with this many addresses is hiding the useful ones among virtual networks.
const maxLANIPs = 6

// virtualPrefixes are interface names of networks that exist only inside the
// machine (containers, virtual machines, WSL): their addresses (172.17.0.1,
// 192.168.x.1 ...) are not where anyone reaches the host. Compared in lower
// case. Tailscale and VPN interfaces are not here on purpose: they are real
// addresses an admin may well use.
var virtualPrefixes = []string{
	"docker", "br-", "veth", "virbr", "cni", "flannel", "cali", "lxc", "lxd",
	"vethernet", "vmware", "vmnet", "virtualbox", "vboxnet", "hyper-v", "wsl",
}

func isVirtualInterface(name string) bool {
	name = strings.ToLower(name)
	for _, prefix := range virtualPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
