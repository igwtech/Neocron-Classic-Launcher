package patch

import (
	"strings"
	"testing"
)

func TestParseManifestStripsBOMAndLowercasesHash(t *testing.T) {
	// A BOM-prefixed manifest in the launcher's "HASH:path" format.
	data := "\uFEFF0269C37DD31AD0AACC9060AAA7261904:neocron.exe\n" +
		"ABCDEF0123456789:data/world.pak\n" +
		"\n" + // blank line ignored
		"deadBEEF:sub/dir/file.dll\n"
	entries, err := parseManifest(strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	if entries[0].Path != "neocron.exe" ||
		entries[0].Hash != "0269c37dd31ad0aacc9060aaa7261904" {
		t.Errorf("entry0 = %+v (BOM or case not normalized)", entries[0])
	}
	if entries[2].Path != "sub/dir/file.dll" || entries[2].Hash != "deadbeef" {
		t.Errorf("entry2 = %+v", entries[2])
	}
}
