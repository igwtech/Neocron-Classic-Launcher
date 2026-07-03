// Package patchy is a native Go reimplementation of the official launcher's
// game patch format (the Rust `patchy.dll`), reverse-engineered in
// docs/RE_LAUNCHER.md §6.
//
// Format summary (all standard building blocks — nothing bespoke to crack):
//
//   - A "version" file is a container wrapping a Cap'n Proto message: a tag/
//     version string plus a list of file entries {path, sha-256, size,
//     file_index}. (patchy crates: capnp 0.20.6.)
//   - The version is signed with a *detached* signature file: RSA-2048,
//     PKCS#1 v1.5, SHA-256, verified against a public key embedded in the DLL —
//     extracted here as patchy_pubkey.der. (patchy crate: ring 0.17.14.)
//   - File payloads live in a "patch container", Brotli-compressed and indexed
//     by file_index. (patchy crate: brotli 2.5.1.)
//   - Repair = walk the install dir, SHA-256 each file, compare to the manifest
//     to find "wounds", then decompress+write the indexed payloads.
//
// Status: the cryptographic + compression primitives below are complete and
// tested. The Cap'n Proto manifest schema (field IDs) and the container header
// layout still need to be pinned from a real sample version file (the content
// URLs are non-public — see §6.4). Once available, implement ParseVersion and
// the container reader; the rest of this package already works.
package patchy

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"fmt"
	"io"

	"github.com/andybalholm/brotli"
)

//go:embed patchy_pubkey.der
var pubKeyDER []byte

// FileEntry is one file described by a version manifest.
type FileEntry struct {
	Path      string   // install-relative path
	SHA256    [32]byte // expected content hash
	Size      int64    // uncompressed size
	FileIndex int      // index into the patch container's payload list
}

// Version is a parsed, verified manifest.
type Version struct {
	Tag   string
	Files []FileEntry
}

// PublicKey returns the signing public key embedded in the official patcher.
// A faithful patcher must verify version manifests against this exact key.
func PublicKey() (*rsa.PublicKey, error) {
	k, err := x509.ParsePKCS1PublicKey(pubKeyDER)
	if err != nil {
		return nil, fmt.Errorf("patchy: embedded public key: %w", err)
	}
	return k, nil
}

// VerifyManifest checks a detached signature over the raw version-message bytes,
// exactly as patchy_verify_version does (RSA PKCS#1 v1.5 + SHA-256).
func VerifyManifest(messageBytes, signature []byte) error {
	key, err := PublicKey()
	if err != nil {
		return err
	}
	sum := sha256.Sum256(messageBytes)
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], signature); err != nil {
		return fmt.Errorf("patchy: signature verification failed: %w", err)
	}
	return nil
}

// Verify checks that data hashes to entry.SHA256 (patchy_verify_buffer parity).
func (e FileEntry) Verify(data []byte) bool {
	return sha256.Sum256(data) == e.SHA256
}

// DecompressPayload Brotli-decompresses one patch-container payload, matching
// the brotli crate the official patcher uses.
func DecompressPayload(compressed []byte) ([]byte, error) {
	out, err := io.ReadAll(brotli.NewReader(bytes.NewReader(compressed)))
	if err != nil {
		return nil, fmt.Errorf("patchy: brotli decode: %w", err)
	}
	return out, nil
}

// ParseVersion parses a version container into a Version.
//
// TODO(schema): implement once the Cap'n Proto manifest schema and container
// header are pinned from a real sample (docs/RE_LAUNCHER.md §6.4). The wire
// format is standard Cap'n Proto, so this becomes a capnp read against the
// recovered schema — no reverse-engineering of custom encoding required.
func ParseVersion(container []byte) (*Version, error) {
	return nil, fmt.Errorf("patchy: version container parsing not yet implemented (schema pending sample)")
}
