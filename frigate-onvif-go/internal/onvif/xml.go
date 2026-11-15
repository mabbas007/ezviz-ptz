package onvif

import (
	"encoding/xml"
	"strings"
	"time"
)

// Fault is a SOAP/ONVIF fault raised by a handler. Code uses the ONVIF error
// namespace prefix (for example "ter:NotAuthorized" or "ter:ActionNotSupported").
type Fault struct {
	Code    string
	Message string
}

func envelope(body string) []byte {
	var b strings.Builder
	b.Grow(len(body) + 512)
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<env:Envelope xmlns:env="` + envNS + `"`)
	b.WriteString(` xmlns:tds="` + tdsNS + `"`)
	b.WriteString(` xmlns:trt="` + trtNS + `"`)
	b.WriteString(` xmlns:tptz="` + tptzNS + `"`)
	b.WriteString(` xmlns:tt="` + ttNS + `"`)
	b.WriteString(` xmlns:ter="` + terNS + `">`)
	b.WriteString(`<env:Body>`)
	b.WriteString(body)
	b.WriteString(`</env:Body></env:Envelope>`)
	return []byte(b.String())
}

// faultEnvelope renders a spec-shaped SOAP 1.2 fault. ONVIF ter: codes are
// subcodes of env:Sender/env:Receiver; unlike the Python bridge we do not
// attach a bogus ActionNotSupported subcode to every fault.
func faultEnvelope(f *Fault) []byte {
	value, subcode := splitFaultCode(f.Code)

	var b strings.Builder
	b.WriteString(`<env:Fault><env:Code><env:Value>`)
	b.WriteString(escape(value))
	b.WriteString(`</env:Value>`)
	if subcode != "" {
		b.WriteString(`<env:Subcode><env:Value>`)
		b.WriteString(escape(subcode))
		b.WriteString(`</env:Value></env:Subcode>`)
	}
	b.WriteString(`</env:Code><env:Reason><env:Text xml:lang="en">`)
	b.WriteString(escape(f.Message))
	b.WriteString(`</env:Text></env:Reason></env:Fault>`)
	return envelope(b.String())
}

func splitFaultCode(code string) (value, subcode string) {
	switch code {
	case "env:Sender", "env:Receiver":
		return code, ""
	case "ter:NotAuthorized", "ter:InvalidArgVal", "ter:InvalidArgs", "ter:NoToken":
		return "env:Sender", code
	default:
		// ter:Action, ter:ActionNotSupported and other receiver-side faults.
		return "env:Receiver", code
	}
}

func escape(value string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}

// utcNow formats the current time as an xs:dateTime in UTC.
func utcNow() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05") + "Z"
}
