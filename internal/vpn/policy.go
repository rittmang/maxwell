package vpn

import (
	"context"

	"maxwell/internal/model"
)

type Signals struct {
	HasTunnelInterface    bool
	HasDefaultRoute       bool
	DefaultRouteInterface string
	DefaultInterfaceIP    string
	TunnelInterfaces      []string
	ActiveSSID            string
	PublicIP              string
	PublicASN             string
	OnHomeNetwork         bool
	PublicIPLooksHome     bool
	ASNLooksVPN           bool
}

type Detector interface {
	Check(context.Context) (Signals, error)
}

func Evaluate(signals Signals) model.VPNState {
	if signals.HasTunnelInterface && (!signals.PublicIPLooksHome || signals.ASNLooksVPN) {
		return model.VPNStateSafe
	}
	if !signals.HasTunnelInterface && signals.HasDefaultRoute {
		return model.VPNStateUnsafe
	}
	if !signals.HasTunnelInterface && (signals.OnHomeNetwork || signals.PublicIPLooksHome) {
		return model.VPNStateUnsafe
	}
	return model.VPNStateUnknown
}

type Gate struct {
	detector Detector
}

func NewGate(detector Detector) *Gate {
	return &Gate{detector: detector}
}

func (g *Gate) Status(ctx context.Context) (model.VPNState, Signals, error) {
	s, err := g.detector.Check(ctx)
	if err != nil {
		return model.VPNStateUnknown, s, err
	}
	return Evaluate(s), s, nil
}

// StaticDetector is useful for tests and local developer mode.
type StaticDetector struct {
	Signals Signals
	Err     error
}

func (d StaticDetector) Check(_ context.Context) (Signals, error) {
	return d.Signals, d.Err
}
