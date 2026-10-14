package styles

import (
	"image/color"
	"testing"
)

func rgb(c color.Color) (r, g, b uint8) {
	r16, g16, b16, _ := c.RGBA()
	return uint8(r16 >> 8), uint8(g16 >> 8), uint8(b16 >> 8)
}

func TestBlendEndpointsAndMidpoint(t *testing.T) {
	black := color.RGBA{0, 0, 0, 0xff}
	white := color.RGBA{255, 255, 255, 0xff}

	if r, g, b := rgb(blend(black, white, 0)); r != 0 || g != 0 || b != 0 {
		t.Errorf("t=0 should return the first color, got %d,%d,%d", r, g, b)
	}
	if r, g, b := rgb(blend(black, white, 1)); r != 255 || g != 255 || b != 255 {
		t.Errorf("t=1 should return the second color, got %d,%d,%d", r, g, b)
	}
	if r, _, _ := rgb(blend(black, white, 0.5)); r != 128 {
		t.Errorf("t=0.5 should be the midpoint, got %d", r)
	}
}

// Surface exists so a selected row or an input box can be told apart
// from the screen. A theme where it's identical to the background would
// make that highlight invisible, so check every built-in one.
func TestEveryThemeHasADistinctSurface(t *testing.T) {
	for name, p := range All {
		th := New(p)
		if th.Surface == nil || th.Success == nil {
			t.Errorf("%s: Surface/Success must always be set", name)
			continue
		}
		if name == System.Name {
			continue // terminal palette slots: no RGB to compare
		}
		sr, sg, sb := rgb(th.Surface)
		br, bg, bb := rgb(p.Background)
		if sr == br && sg == bg && sb == bb {
			t.Errorf("%s: Surface is identical to Background - a selected row would be invisible", name)
		}
	}
}
