package platform

import (
	"net"
	"strings"
)

func isLoopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || ip != nil && ip.IsLoopback()
}
