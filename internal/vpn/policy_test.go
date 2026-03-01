package vpn

import (
	"testing"

	"maxwell/internal/model"
)

func TestEvaluatePolicyMatrix(t *testing.T) {
	tests := []struct {
		name    string
		signals Signals
		want    model.VPNState
	}{
		{
			name:    "safe when tunnel active and public ip not home",
			signals: Signals{HasTunnelInterface: true, HasDefaultRoute: true, PublicIPLooksHome: false},
			want:    model.VPNStateSafe,
		},
		{
			name:    "unsafe when no tunnel and route present",
			signals: Signals{HasTunnelInterface: false, HasDefaultRoute: true},
			want:    model.VPNStateUnsafe,
		},
		{
			name:    "unsafe when no tunnel and public ip home",
			signals: Signals{HasTunnelInterface: false, HasDefaultRoute: true, PublicIPLooksHome: true},
			want:    model.VPNStateUnsafe,
		},
		{
			name:    "unknown when no route and no tunnel",
			signals: Signals{HasTunnelInterface: false, HasDefaultRoute: false, OnHomeNetwork: false, PublicIPLooksHome: false},
			want:    model.VPNStateUnknown,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(tc.signals)
			if got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}
