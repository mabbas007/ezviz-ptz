package onvif

import "testing"

func TestNumberRejectsMissingAndNonFinite(t *testing.T) {
	root, err := parseNode([]byte(`<Velocity><PanTilt x="NaN" y="0.5"/>` +
		`<Zoom x="Inf"/></Velocity>`))
	if err != nil {
		t.Fatalf("parseNode: %v", err)
	}

	if got := number(root, "PanTilt", "x"); got != 0 {
		t.Fatalf(`number(PanTilt, x="NaN") = %v, want 0`, got)
	}
	if got := number(root, "PanTilt", "y"); got != 0.5 {
		t.Fatalf(`number(PanTilt, y) = %v, want 0.5`, got)
	}
	if got := number(root, "Zoom", "x"); got != 0 {
		t.Fatalf(`number(Zoom, x="Inf") = %v, want 0`, got)
	}
	if got := number(root, "Missing", "x"); got != 0 {
		t.Fatalf("number(missing) = %v, want 0", got)
	}
}
