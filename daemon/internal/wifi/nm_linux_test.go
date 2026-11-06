//go:build linux

package wifi

import "testing"

func TestParseScan(t *testing.T) {
	out := "Garaje:82:WPA2\nCasa\\:5G:60:WPA2 WPA3\n:40:WPA2\nGaraje:30:WPA2\nInvitados:55:\n"
	got := parseScan(out)
	want := []Network{{"Garaje", 82, true}, {"Casa:5G", 60, true}, {"Invitados", 55, false}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestKeyfileEscape(t *testing.T) {
	cases := map[string]string{`simple`: `simple`, `a\b`: `a\\b`, " sp": `\ssp`, "a#b;c=d": "a#b;c=d", "x\ny": `x\ny`}
	for in, want := range cases {
		if got := keyfileEscape(in); got != want {
			t.Errorf("%q → %q, esperado %q", in, got, want)
		}
	}
}
