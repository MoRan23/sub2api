package codexnative

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	utls "github.com/refraction-networking/utls"
)

// ProfileVersion identifies the 2026-09-17 Codex CLI 0.154.0 observations.
// Only public negotiation parameters are stored, never captured randomness,
// ephemeral keys, session tickets, payloads, or credentials.
const ProfileVersion = "codex-cli-0.154.0-native-20260917-v1"

type profile struct {
	Platform      Platform
	Version       string
	RecordVersion uint16
	SessionIDSize int
	MinVersion    uint16
	MaxVersion    uint16
	Ciphers       []uint16
	Compression   []byte
	PointFormats  []byte
	Groups        []uint16
	Signatures    []uint16
	Extensions    []uint16
	Versions      []uint16
	KeyShares     []uint16
	PSKModes      []byte
	HeaderOrder   []string
}

// Each call allocates a complete profile: callers never share mutable slices or
// uTLS extension objects between handshakes.
func profileFor(platform Platform) (profile, error) {
	p := profile{Platform: platform, Version: ProfileVersion, RecordVersion: 0x0301,
		MinVersion: utls.VersionTLS12, MaxVersion: utls.VersionTLS12, Compression: []byte{0}, PointFormats: []byte{0},
		HeaderOrder: []string{"version", "x-codex-beta-features", "x-codex-window-id", "x-codex-turn-metadata"}}
	switch platform {
	case Windows:
		p.RecordVersion = 0x0303
		p.Ciphers = []uint16{49196, 49195, 49200, 49199, 49188, 49187, 49192, 49191, 49162, 49161, 49172, 49171, 157, 156, 61, 60, 53, 47}
		p.Groups = []uint16{29, 23, 24}
		p.Signatures = []uint16{2052, 2053, 2054, 1025, 1281, 513, 1027, 1283, 515, 514, 1537, 1539}
		p.Extensions = []uint16{0, 10, 11, 13, 35, 23, 65281}
		p.HeaderOrder = append(p.HeaderOrder, "x-openai-internal-codex-responses-lite")
	case Linux:
		p.MaxVersion = utls.VersionTLS13
		p.SessionIDSize = 32
		p.Ciphers = []uint16{4866, 4867, 4865, 49196, 49200, 159, 52393, 52392, 52394, 49195, 49199, 158, 49188, 49192, 107, 49187, 49191, 103, 49162, 49172, 57, 49161, 49171, 51, 157, 156, 61, 60, 53, 47}
		p.Groups = []uint16{4588, 29, 23, 30, 24, 25, 256, 257}
		p.Signatures = []uint16{2309, 2310, 2308, 1027, 1283, 1539, 2055, 2056, 2074, 2075, 2076, 2057, 2058, 2059, 2052, 2053, 2054, 1025, 1281, 1537, 771, 769, 770, 1026, 1282, 1538}
		p.Extensions = []uint16{65281, 0, 11, 10, 35, 22, 23, 13, 43, 45, 51}
		p.Versions = []uint16{utls.VersionTLS13, utls.VersionTLS12}
		p.KeyShares = []uint16{4588, 29}
		p.PSKModes = []byte{1}
	case MacOS:
		p.Ciphers = []uint16{255, 49196, 49195, 49188, 49187, 49162, 49161, 49160, 49200, 49199, 49192, 49191, 49172, 49171, 49170, 157, 156, 61, 60, 53, 47, 10}
		p.Groups = []uint16{23, 24, 25}
		p.Signatures = []uint16{1025, 513, 1281, 1537, 1027, 515, 1283, 1539}
		p.Extensions = []uint16{0, 10, 11, 13, 5, 18, 23}
	default:
		return profile{}, fmt.Errorf("codexnative: unsupported platform %q", platform)
	}
	p.HeaderOrder = append(p.HeaderOrder, "x-client-request-id", "session-id", "thread-id", "accept", "content-type", "authorization", "originator", "user-agent")
	return p, nil
}

func selection(platform Platform, matchedBy string) Selection {
	p, _ := profileFor(platform) // Only the three known platforms reach this helper.
	encoded, _ := json.Marshal(p)
	digest := sha256.Sum256(encoded)
	return Selection{Platform: platform, ProfileID: ProfileVersion + "/" + string(platform), Digest: hex.EncodeToString(digest[:]), MatchedBy: matchedBy}
}

func profileSpec(platform Platform) (*utls.ClientHelloSpec, error) {
	p, err := profileFor(platform)
	if err != nil {
		return nil, err
	}
	spec := &utls.ClientHelloSpec{CipherSuites: p.Ciphers, CompressionMethods: p.Compression, TLSVersMin: p.MinVersion, TLSVersMax: p.MaxVersion}
	for _, id := range p.Extensions {
		var extension utls.TLSExtension
		switch id {
		case 0:
			extension = &utls.SNIExtension{}
		case 5:
			extension = &utls.StatusRequestExtension{}
		case 10:
			curves := make([]utls.CurveID, len(p.Groups))
			for i, group := range p.Groups {
				curves[i] = utls.CurveID(group)
			}
			extension = &utls.SupportedCurvesExtension{Curves: curves}
		case 11:
			extension = &utls.SupportedPointsExtension{SupportedPoints: p.PointFormats}
		case 13:
			signatures := make([]utls.SignatureScheme, len(p.Signatures))
			for i, signature := range p.Signatures {
				signatures[i] = utls.SignatureScheme(signature)
			}
			extension = &utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: signatures}
		case 18:
			extension = &utls.SCTExtension{}
		case 22:
			// RFC 7366's empty encrypt_then_mac offer. uTLS does not implement
			// its CBC negotiation; the sampled common GCM suites work normally.
			extension = &utls.GenericExtension{Id: 22}
		case 23:
			extension = &utls.ExtendedMasterSecretExtension{}
		case 35:
			extension = &utls.SessionTicketExtension{}
		case 43:
			extension = &utls.SupportedVersionsExtension{Versions: p.Versions}
		case 45:
			extension = &utls.PSKKeyExchangeModesExtension{Modes: p.PSKModes}
		case 51:
			shares := make([]utls.KeyShare, len(p.KeyShares))
			for i, group := range p.KeyShares {
				shares[i] = utls.KeyShare{Group: utls.CurveID(group)}
			}
			extension = &utls.KeyShareExtension{KeyShares: shares}
		case 65281:
			extension = &utls.RenegotiationInfoExtension{Renegotiation: utls.RenegotiateNever}
		}
		spec.Extensions = append(spec.Extensions, extension)
	}
	return spec, nil
}
