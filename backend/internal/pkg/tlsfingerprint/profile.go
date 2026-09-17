package tlsfingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
)

// Clone returns an independent profile snapshot for a transport's lifetime.
func (p *Profile) Clone() *Profile {
	if p == nil {
		return nil
	}
	clone := *p
	clone.CipherSuites = slices.Clone(p.CipherSuites)
	clone.Curves = slices.Clone(p.Curves)
	clone.PointFormats = slices.Clone(p.PointFormats)
	clone.SignatureAlgorithms = slices.Clone(p.SignatureAlgorithms)
	clone.ALPNProtocols = slices.Clone(p.ALPNProtocols)
	clone.SupportedVersions = slices.Clone(p.SupportedVersions)
	clone.KeyShareGroups = slices.Clone(p.KeyShareGroups)
	clone.PSKModes = slices.Clone(p.PSKModes)
	clone.Extensions = slices.Clone(p.Extensions)
	return &clone
}

// CacheKey identifies the complete profile, including ordered TLS parameters.
// Struct JSON encoding is deterministic and preserves array order; the Profile
// contains only supported primitive fields, so marshaling cannot fail.
func (p *Profile) CacheKey() string {
	encoded, _ := json.Marshal(p)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
