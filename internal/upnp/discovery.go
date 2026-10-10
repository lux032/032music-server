package upnp

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	ssdpAddr        = "239.255.255.250:1900"
	stMediaRenderer = "urn:schemas-upnp-org:device:MediaRenderer:1"
	stZonePlayer    = "urn:schemas-upnp-org:device:ZonePlayer:1"
)

// ssdpSearch multicasts M-SEARCH from every IPv4 interface (a multi-homed
// host would otherwise only search the default route) and returns the
// distinct LOCATION headers heard before timeout.
func ssdpSearch(ctx context.Context, timeout time.Duration) []string {
	var (
		mu        sync.Mutex
		locations = map[string]bool{}
		wg        sync.WaitGroup
	)
	deadline := time.Now().Add(timeout)
	for _, local := range searchAddresses() {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: local})
		if err != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer conn.Close()
			stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
			defer stop()
			target, _ := net.ResolveUDPAddr("udp4", ssdpAddr)
			for _, st := range []string{stMediaRenderer, stZonePlayer} {
				msg := "M-SEARCH * HTTP/1.1\r\nHOST: " + ssdpAddr + "\r\nMAN: \"ssdp:discover\"\r\nMX: 2\r\nST: " + st + "\r\n\r\n"
				for i := 0; i < 2; i++ {
					_, _ = conn.WriteToUDP([]byte(msg), target)
				}
			}
			_ = conn.SetReadDeadline(deadline)
			buf := make([]byte, 8192)
			for {
				n, _, err := conn.ReadFromUDP(buf)
				if err != nil {
					return
				}
				if loc := headerValue(string(buf[:n]), "LOCATION"); loc != "" {
					mu.Lock()
					locations[loc] = true
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	out := make([]string, 0, len(locations))
	for loc := range locations {
		out = append(out, loc)
	}
	sort.Strings(out)
	return out
}

// searchAddresses lists the IPv4 addresses of up, multicast-capable,
// non-loopback interfaces; nil (the OS default) when there are none.
func searchAddresses() []net.IP {
	var ips []net.IP
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagMulticast == 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok {
				if ip4 := ipNet.IP.To4(); ip4 != nil && !ip4.IsLinkLocalUnicast() {
					ips = append(ips, ip4)
				}
			}
		}
	}
	if len(ips) == 0 {
		return []net.IP{nil}
	}
	return ips
}

func headerValue(payload, name string) string {
	for _, line := range strings.Split(payload, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(key), name) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

type descDevice struct {
	DeviceType   string        `xml:"deviceType"`
	FriendlyName string        `xml:"friendlyName"`
	Manufacturer string        `xml:"manufacturer"`
	ModelName    string        `xml:"modelName"`
	UDN          string        `xml:"UDN"`
	RoomName     string        `xml:"roomName"`
	Services     []descService `xml:"serviceList>service"`
	Devices      []descDevice  `xml:"deviceList>device"`
}

type descService struct {
	ServiceType string `xml:"serviceType"`
	ControlURL  string `xml:"controlURL"`
}

type description struct {
	URLBase string     `xml:"URLBase"`
	Device  descDevice `xml:"device"`
}

func (d descDevice) findService(prefix string) (descService, bool) {
	for _, s := range d.Services {
		if strings.HasPrefix(s.ServiceType, prefix) {
			return s, true
		}
	}
	for _, child := range d.Devices {
		if s, ok := child.findService(prefix); ok {
			return s, true
		}
	}
	return descService{}, false
}

func fetchDescription(ctx context.Context, client *http.Client, location string) (description, error) {
	var desc description
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return desc, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return desc, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return desc, fmt.Errorf("description %s: HTTP %d", location, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return desc, err
	}
	return desc, xml.Unmarshal(raw, &desc)
}

func isSonos(desc description) bool {
	return strings.Contains(strings.ToLower(desc.Device.Manufacturer), "sonos") ||
		strings.HasPrefix(desc.Device.DeviceType, "urn:schemas-upnp-org:device:ZonePlayer")
}

// genericDevice turns a MediaRenderer description into a Device; ok is
// false when it has no AVTransport.
func genericDevice(location string, desc description) (Device, bool) {
	av, ok := desc.Device.findService("urn:schemas-upnp-org:service:AVTransport:")
	if !ok {
		return Device{}, false
	}
	base := desc.URLBase
	if base == "" {
		base = origin(location)
	}
	dev := Device{
		ID:             normalizeUUID(desc.Device.UDN),
		Name:           firstNonEmpty(desc.Device.FriendlyName, desc.Device.ModelName, hostOf(location)),
		Kind:           KindDLNA,
		Model:          desc.Device.ModelName,
		Manufacturer:   desc.Device.Manufacturer,
		Host:           hostOf(location),
		MemberCount:    1,
		baseURL:        origin(location),
		avTransportURL: resolve(base, av.ControlURL),
	}
	if rc, ok := desc.Device.findService("urn:schemas-upnp-org:service:RenderingControl:"); ok {
		dev.renderingControlURL = resolve(base, rc.ControlURL)
	}
	if dev.ID == "" {
		dev.ID = "dlna-" + strings.NewReplacer(".", "-", ":", "-").Replace(dev.Host)
	}
	return dev, true
}

func sonosDevice(location string, desc description) Device {
	return Device{
		ID:           normalizeUUID(desc.Device.UDN),
		Name:         firstNonEmpty(desc.Device.RoomName, desc.Device.FriendlyName),
		Kind:         KindSonos,
		Model:        desc.Device.ModelName,
		Manufacturer: desc.Device.Manufacturer,
		Host:         hostOf(location),
		MemberCount:  1,
		Queue:        true,
		baseURL:      origin(location),
	}
}

type zoneGroupState struct {
	Groups []struct {
		Coordinator string `xml:"Coordinator,attr"`
		Members     []struct {
			UUID      string `xml:"UUID,attr"`
			Location  string `xml:"Location,attr"`
			ZoneName  string `xml:"ZoneName,attr"`
			Invisible string `xml:"Invisible,attr"`
		} `xml:"ZoneGroupMember"`
	} `xml:"ZoneGroups>ZoneGroup"`
}

// sonosGroups resolves zone groups through ZoneGroupTopology so commands
// always target the group coordinator (members answer 800 otherwise).
func sonosGroups(ctx context.Context, soap soapClient, baseURL string, known map[string]Device) ([]Device, error) {
	res, err := soap.call(ctx, baseURL+"/ZoneGroupTopology/Control", svcZoneGroupTopology, "GetZoneGroupState")
	if err != nil {
		return nil, err
	}
	return parseZoneGroups(res["ZoneGroupState"], known)
}

func parseZoneGroups(stateXML string, known map[string]Device) ([]Device, error) {
	var state zoneGroupState
	if err := xml.Unmarshal([]byte(stateXML), &state); err != nil {
		return nil, err
	}
	// Newer firmware wraps the groups in <ZoneGroupState>; older sends
	// <ZoneGroups> as the root element.
	if len(state.Groups) == 0 {
		_ = xml.Unmarshal([]byte("<x>"+stateXML+"</x>"), &state)
	}
	var out []Device
	for _, group := range state.Groups {
		coordinator := group.Coordinator
		var names []string
		dev := Device{ID: coordinator, Kind: KindSonos, Queue: true}
		visible := 0
		for _, m := range group.Members {
			if m.UUID == coordinator {
				dev.baseURL = origin(m.Location)
				dev.Host = hostOf(m.Location)
				names = append([]string{m.ZoneName}, names...)
			} else if m.Invisible != "1" {
				names = append(names, m.ZoneName)
			}
			if m.Invisible != "1" {
				visible++
			}
		}
		if dev.baseURL == "" || len(names) == 0 {
			continue
		}
		if k, ok := known[coordinator]; ok {
			dev.Model, dev.Manufacturer = k.Model, k.Manufacturer
		}
		dev.Name = uniqueJoin(names)
		dev.MemberCount = max(visible, 1)
		out = append(out, dev)
	}
	return out, nil
}

func uniqueJoin(names []string) string {
	seen := map[string]bool{}
	var parts []string
	for _, n := range names {
		if n != "" && !seen[n] {
			seen[n] = true
			parts = append(parts, n)
		}
	}
	return strings.Join(parts, " + ")
}

func normalizeUUID(udn string) string {
	value := strings.TrimPrefix(strings.TrimSpace(udn), "uuid:")
	if i := strings.Index(value, "::"); i >= 0 {
		value = value[:i]
	}
	return value
}

func origin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func resolve(base, ref string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
