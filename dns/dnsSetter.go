package dns

import (
	"net"
	"os/exec"
	"runtime"
	"strings"
)

type DNSSetter struct {
	networkInterfaces []string
}

func NewDNSSetter() (*DNSSetter, error) {
	d := &DNSSetter{
		networkInterfaces: make([]string, 0),
	}
	err := d.getNetworkInterfaces()
	if err != nil {
		return nil, err
	}
	return d, nil
}

func (d *DNSSetter) setDNS(newDNS []string, interfaceName string) error {
	var cmd *exec.Cmd

	//if runtime.GOOS == "windows" {
	//	// Windows 需要为每个DNS设置单独的命令
	//
	//	for index, dns := range newDNS {
	//		netshType := "add"
	//		if index == 0 {
	//			netshType = "set"
	//		}
	//
	//		cmd = exec.Command("netsh", "interface", "ip", netshType, "dns", "static", interfaceName, dns)
	//		if err := cmd.Run(); err != nil {
	//			return err
	//		}
	//	}
	//} else
	if runtime.GOOS == "darwin" {
		cmdArgs := append([]string{"-setdnsservers", interfaceName}, newDNS...)
		cmd = exec.Command("networksetup", cmdArgs...)
		if err := cmd.Run(); err != nil {
			return err
		}
	}
	return nil
}

func (d *DNSSetter) restore(interfaceName string) error {
	var cmd *exec.Cmd

	//if runtime.GOOS == "windows" {
	//	cmd = exec.Command("netsh", "interface", "ip", "set", "dnsservers", interfaceName, "source =dhcp")
	//	if err := cmd.Run(); err != nil {
	//		return err
	//	}
	//} else
	if runtime.GOOS == "darwin" {
		cmd = exec.Command("networksetup", "-setdnsservers", interfaceName, "Empty")
		if err := cmd.Run(); err != nil {
			return err
		}
	}
	return nil
}

// GetDNS 获取指定接口的当前DNS配置
func (d *DNSSetter) getDNS(interfaceName string) ([]string, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command("networksetup", "-getdnsservers", interfaceName)
	} else {
		return nil, nil
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}

	return d.parseDNSOutput(string(output)), nil
}

func (d *DNSSetter) parseDNSOutput(output string) []string {
	var dnsServers []string
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "DNS Servers") || strings.Contains(line, "There aren't any DNS Servers") {
			continue
		}
		if netIP := net.ParseIP(line); netIP != nil {
			dnsServers = append(dnsServers, line)
		}
	}
	return dnsServers
}

func (d *DNSSetter) getNetworkServiceInfo(serviceName string) (map[string]string, error) {
	cmd := exec.Command("networksetup", "-getinfo", serviceName)
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	info := make(map[string]string)
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		parts := strings.Split(line, ": ")
		if len(parts) == 2 {
			info[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}

	return info, nil
}

// getNetworkInterfaces 获取系统上的网络接口列表
func (d *DNSSetter) getNetworkInterfaces() error {
	d.networkInterfaces = make([]string, 0)
	var cmd *exec.Cmd
	//if runtime.GOOS == "windows" {
	//	cmd = exec.Command("cmd", "/c", "chcp", "65001", ">nul", "&", "netsh", "interface", "show", "interface")
	//	output, err := cmd.CombinedOutput()
	//	if err != nil {
	//		return err
	//	}
	//	lines := strings.Split(string(output), "\n")
	//	for _, line := range lines {
	//		line = strings.TrimSpace(line)
	//		if line != "" && strings.Contains(line, "Connected") && strings.Contains(line, "Dedicated") {
	//			re := regexp.MustCompile(`\s+`)
	//			v := re.Split(line, -1)
	//			if len(v) == 4 {
	//				d.networkInterfaces = append(d.networkInterfaces, v[3])
	//			}
	//
	//		}
	//	}
	//
	//} else
	if runtime.GOOS == "darwin" {
		cmd = exec.Command("networksetup", "-listallnetworkservices")
		output, err := cmd.CombinedOutput()
		if err != nil {
			return err
		}
		lines := strings.Split(string(output), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" && !strings.Contains(line, "*") {
				s, r := d.getNetworkServiceInfo(line)
				if r == nil {
					if v, ok := s["Subnet mask"]; ok && v != "" {
						d.networkInterfaces = append(d.networkInterfaces, line)
					}

				}

			}
		}
	}

	return nil
}

// SetDNSForAllInterfaces 修改所有网络接口的DNS服务器地址
func (d *DNSSetter) SetDNSForAllInterfaces(newDNS []string) error {

	for _, iface := range d.networkInterfaces {
		if err := d.setDNS(newDNS, iface); err != nil {
			return err
		}
	}

	return nil
}

// restoreDNS 还原DNS设置
func (d *DNSSetter) RestoreAllInterfacesDNS() error {
	for _, v := range d.networkInterfaces {
		if err := d.restore(v); err != nil {
			return err
		}
	}
	return nil
}
