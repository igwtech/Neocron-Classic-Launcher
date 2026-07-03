package patchy

import (
	"bytes"
	"testing"

	"github.com/andybalholm/brotli"
)

func TestEmbeddedPublicKeyIsRSA2048(t *testing.T) {
	k, err := PublicKey()
	if err != nil {
		t.Fatal(err)
	}
	if k.N.BitLen() != 2048 || k.E != 65537 {
		t.Fatalf("embedded key = %d-bit e=%d, want 2048-bit e=65537", k.N.BitLen(), k.E)
	}
}

func TestVerifyManifestRejectsForgery(t *testing.T) {
	// Without the private key we can't produce a valid signature; a bogus one
	// must be rejected (guards against an "accept anything" regression).
	if err := VerifyManifest([]byte("manifest bytes"), bytes.Repeat([]byte{0xAB}, 256)); err == nil {
		t.Fatal("VerifyManifest accepted a forged signature")
	}
}

func TestDecompressPayloadRoundTrip(t *testing.T) {
	want := []byte("neocron.exe\x00world.pak payload contents, repeated repeated repeated")
	var buf bytes.Buffer
	w := brotli.NewWriter(&buf)
	if _, err := w.Write(want); err != nil {
		t.Fatal(err)
	}
	w.Close()
	got, err := DecompressPayload(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("round-trip mismatch:\n got %q\nwant %q", got, want)
	}
}
