package agent

import "net"

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
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
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
	return ips
}
