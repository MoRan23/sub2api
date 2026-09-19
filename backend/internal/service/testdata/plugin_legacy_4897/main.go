package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
)

type legacyPlugin struct {
	pluginv1.UnimplementedTransportPluginServer
}

func (*legacyPlugin) GetInfo(context.Context, *pluginv1.GetInfoRequest) (*pluginv1.GetInfoResponse, error) {
	return &pluginv1.GetInfoResponse{PluginId: "test.legacy-4897", PluginVersion: "0.0.1", ProtocolVersion: pluginv1.ProtocolVersion, TransportApiVersion: pluginv1.TransportAPIVersion}, nil
}

func (*legacyPlugin) Health(context.Context, *pluginv1.HealthRequest) (*pluginv1.HealthResponse, error) {
	return &pluginv1.HealthResponse{Healthy: true, Message: "legacy-4897-health"}, nil
}

func (*legacyPlugin) ValidateConfig(_ context.Context, request *pluginv1.ValidateConfigRequest) (*pluginv1.ValidateConfigResponse, error) {
	return &pluginv1.ValidateConfigResponse{Valid: true, NormalizedConfigJson: request.ConfigJson}, nil
}

func (*legacyPlugin) ApplyConfig(context.Context, *pluginv1.ApplyConfigRequest) (*pluginv1.ApplyConfigResponse, error) {
	return &pluginv1.ApplyConfigResponse{Applied: true}, nil
}

func (*legacyPlugin) Forward(stream pluginv1.TransportPlugin_ForwardServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	start := first.GetStart()
	if start == nil {
		return fmt.Errorf("missing request start")
	}
	target, err := url.Parse(start.Url)
	if err != nil || target.Scheme != "http" || !net.ParseIP(target.Hostname()).IsLoopback() || start.ProxyUrl != "" {
		return fmt.Errorf("fixture only accepts direct loopback HTTP")
	}
	var body bytes.Buffer
	for {
		frame, err := stream.Recv()
		if err != nil {
			return err
		}
		if frame.GetBodyEnd() {
			break
		}
		if body.Len()+len(frame.GetBodyChunk()) > 1024*1024 {
			return fmt.Errorf("fixture request too large")
		}
		body.Write(frame.GetBodyChunk())
	}
	request, err := http.NewRequestWithContext(stream.Context(), start.Method, start.Url, &body)
	if err != nil {
		return err
	}
	request.Host = start.Host
	request.ContentLength = start.ContentLength
	for key, values := range start.Headers {
		request.Header[key] = append([]string(nil), values.Values...)
	}
	request.Header.Set("X-Legacy-SDK", "4897")
	request.Header.Set("X-Legacy-Account", strconv.FormatInt(start.AccountId, 10))
	request.Header.Set("X-Legacy-Platform", start.Platform)
	request.Header.Set("X-Legacy-Account-Type", start.AccountType)
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	headers := make(map[string]*pluginv1.HeaderValues, len(response.Header))
	for key, values := range response.Header {
		headers[key] = &pluginv1.HeaderValues{Values: append([]string(nil), values...)}
	}
	if err := stream.Send(&pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_Start{Start: &pluginv1.ForwardResponseStart{
		StatusCode: int32(response.StatusCode), Status: response.Status, Protocol: response.Proto,
		ProtocolMajor: int32(response.ProtoMajor), ProtocolMinor: int32(response.ProtoMinor), Headers: headers, ContentLength: response.ContentLength,
	}}}); err != nil {
		return err
	}
	buffer := make([]byte, 8192)
	var received int64
	for {
		n, err := response.Body.Read(buffer)
		if n > 0 {
			received += int64(n)
			if sendErr := stream.Send(&pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_BodyChunk{BodyChunk: buffer[:n]}}); sendErr != nil {
				return sendErr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	return stream.Send(&pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_End{End: &pluginv1.ForwardResponseEnd{BytesReceived: received}}})
}

func main() { pluginv1.Serve(&legacyPlugin{}) }
