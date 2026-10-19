package hostprofile

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestPartUUIDs(t *testing.T) {
	// / на LVM поверх LUKS, swap на отдельном разделе.
	raw := `{"mounts":[{"target":"/","device":{"kind":"lvm","lower":[{"kind":"luks","lower":[
		{"kind":"partition","partuuid":"aaaa"}]}]}}],
		"swaps":[{"path":"/dev/nvme0n1p3","device":{"kind":"partition","partuuid":"bbbb"}}]}`
	var p Profile
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	if got := p.PartUUIDs(); !slices.Equal(got, []string{"aaaa", "bbbb"}) {
		t.Fatal(got)
	}
}
