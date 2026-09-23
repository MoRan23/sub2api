package service

// Turn metadata is also used as an HTTP header, including when carried in WS JSON.
// Keep RawMessage fields and the fork's HTML-escaping policy intact while making
// every encoded carrier printable ASCII, including DEL (0x7f).
func marshalCodexTurnMetadata(metadata any) ([]byte, error) {
	raw, err := marshalJSONWithoutHTMLEscape(metadata)
	if err != nil {
		return nil, err
	}
	return []byte(escapeNonASCIIJSON(raw)), nil
}
