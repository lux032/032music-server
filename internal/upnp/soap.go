// Package upnp drives UPnP/DLNA media renderers on the local network:
// SSDP discovery, AVTransport control and — for Sonos — the device-side
// play queue, so a pushed album keeps playing without any client online.
package upnp

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	svcAVTransport       = "urn:schemas-upnp-org:service:AVTransport:1"
	svcRenderingControl  = "urn:schemas-upnp-org:service:RenderingControl:1"
	svcGroupRendering    = "urn:schemas-upnp-org:service:GroupRenderingControl:1"
	svcContentDirectory  = "urn:schemas-upnp-org:service:ContentDirectory:1"
	svcZoneGroupTopology = "urn:schemas-upnp-org:service:ZoneGroupTopology:1"
)

// arg is one ordered SOAP argument; UPnP devices are picky about order.
type arg struct{ name, value string }

// Error is a UPnP fault (HTTP 500 + <UPnPError>) or a transport failure.
type Error struct {
	Action      string
	Code        int
	Description string
	Status      int
}

func (e *Error) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("upnp %s: error %d %s", e.Action, e.Code, e.Description)
	}
	return fmt.Sprintf("upnp %s: HTTP %d", e.Action, e.Status)
}

// UPnPCode returns the UPnP error code carried by err, or 0.
func UPnPCode(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return 0
}

type soapClient struct{ http *http.Client }

func newSOAPClient(client *http.Client) soapClient {
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	return soapClient{http: client}
}

// call invokes action on the service at controlURL and returns the
// response arguments by local element name.
func (c soapClient) call(ctx context.Context, controlURL, service, action string, args ...arg) (map[string]string, error) {
	var body bytes.Buffer
	body.WriteString(`<?xml version="1.0" encoding="utf-8"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:`)
	body.WriteString(action)
	body.WriteString(` xmlns:u="`)
	body.WriteString(service)
	body.WriteString(`">`)
	for _, a := range args {
		body.WriteString("<" + a.name + ">")
		_ = xml.EscapeText(&body, []byte(a.value))
		body.WriteString("</" + a.name + ">")
	}
	body.WriteString(`</u:` + action + `></s:Body></s:Envelope>`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, controlURL, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPACTION", `"`+service+"#"+action+`"`)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upnp %s: %w", action, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("upnp %s: %w", action, err)
	}
	if resp.StatusCode != http.StatusOK {
		e := &Error{Action: action, Status: resp.StatusCode}
		fault, _ := responseValues(raw, "UPnPError")
		if code, convErr := strconv.Atoi(strings.TrimSpace(fault["errorCode"])); convErr == nil {
			e.Code = code
			e.Description = fault["errorDescription"]
		}
		return nil, e
	}
	return responseValues(raw, action+"Response")
}

// responseValues collects the text of every direct child of the first
// element named wrapper.
func responseValues(raw []byte, wrapper string) (map[string]string, error) {
	d := xml.NewDecoder(bytes.NewReader(raw))
	values := map[string]string{}
	inside := false
	for {
		tok, err := d.Token()
		if err == io.EOF {
			if !inside {
				return values, fmt.Errorf("upnp: %s missing from response", wrapper)
			}
			return values, nil
		}
		if err != nil {
			return values, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if !inside {
				inside = t.Name.Local == wrapper
				continue
			}
			var text string
			if err := d.DecodeElement(&text, &t); err != nil {
				return values, err
			}
			values[t.Name.Local] = text
		case xml.EndElement:
			if inside && t.Name.Local == wrapper {
				return values, nil
			}
		}
	}
}

// formatDuration renders H:MM:SS as AVTransport Seek/REL_TIME expects.
func formatDuration(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	s := ms / 1000
	return fmt.Sprintf("%d:%02d:%02d", s/3600, (s%3600)/60, s%60)
}

// parseDuration reads H+:MM:SS(.fff); anything else (NOT_IMPLEMENTED, "")
// is 0.
func parseDuration(value string) int64 {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 3 {
		return 0
	}
	h, e1 := strconv.Atoi(parts[0])
	m, e2 := strconv.Atoi(parts[1])
	sec, e3 := strconv.ParseFloat(parts[2], 64)
	if e1 != nil || e2 != nil || e3 != nil {
		return 0
	}
	return int64(h)*3600000 + int64(m)*60000 + int64(sec*1000)
}

func xmlEscape(value string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}
