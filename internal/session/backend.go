package session

import (
	"context"
	"fmt"

	"github.com/tomrford/gocan"
	"github.com/tomrford/gocan/drivers"
	"github.com/tomrford/gocan/drivers/virtual"
)

func Buses(context.Context, Empty) (Result, error) {
	channels, err := drivers.Discover()
	buses := []Result{{"interface": "virtual", "channel": "virtual:agent-can", "name": "In-process loopback", "supports_fd": true}}
	for _, channel := range channels {
		buses = append(buses, Result{"interface": channel.Driver(), "channel": channel.Identifier(), "name": channel.Name(),
			"supports_fd": channel.SupportsFD(), "externally_configured": channel.ExternallyConfigured()})
	}
	result := Result{"buses": buses}
	if err != nil {
		result["discovery_error"] = err.Error()
	}
	return result, nil
}

func openBus(ctx context.Context, capture *gocan.Capture, request ConnectRequest) (gocan.Bus, error) {
	if request.Channel == "virtual:agent-can" {
		if request.Bitrate != 0 || request.FDTiming != nil {
			return nil, fmt.Errorf("virtual CAN needs no bit timing")
		}
		return new(virtual.Network).Open(ctx, capture, virtual.Config{ID: 1, Name: "agent-can", ReceiveOwnMessages: true})
	}
	channels, err := drivers.Discover()
	for _, channel := range channels {
		if channel.Identifier() != request.Channel {
			continue
		}
		config := drivers.Config{ID: 1, Name: channel.Name(), Bitrate: request.Bitrate, External: channel.ExternallyConfigured()}
		if request.FDTiming != nil {
			config.FDTiming = request.FDTiming.native()
		}
		return drivers.Open(ctx, capture, channel, config)
	}
	if err != nil {
		return nil, fmt.Errorf("CAN channel %q not found; discovery reported: %w", request.Channel, err)
	}
	return nil, fmt.Errorf("CAN channel %q not found; use buses_list", request.Channel)
}
