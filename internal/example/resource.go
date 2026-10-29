// Copyright 2020 Envoyproxy Authors
//
//   Licensed under the Apache License, Version 2.0 (the "License");
//   you may not use this file except in compliance with the License.
//   You may obtain a copy of the License at
//
//       http://www.apache.org/licenses/LICENSE-2.0
//
//   Unless required by applicable law or agreed to in writing, software
//   distributed under the License is distributed on an "AS IS" BASIS,
//   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//   See the License for the specific language governing permissions and
//   limitations under the License.

package example

import (
	"bytes"
	"encoding/json"
	"os"
	"time"

	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"

	cluster "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	core "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpoint "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	listener "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	route "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	basicauth "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/basic_auth/v3"
	router "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/router/v3"
	hcm "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"github.com/envoyproxy/go-control-plane/pkg/resource/v3"

	// Register the "@type" values used in the YAML file so protojson can decode them.
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/upstreams/http/v3"
)

const (
	ClusterName  = "example_proxy_cluster"
	RouteName    = "local_route"
	ListenerName = "listener_0"
	ListenerPort = 10000
	UpstreamHost = "www.envoyproxy.io"
	UpstreamPort = 80

	// Paths are relative to the go-control-plane root.
	SourceFile = "../out/control-plane complex.fixed.yaml" // the data to serve
	ConfigFile = "internal/config/config.yaml"             // the final config, rewritten on every push
)

func makeCluster(clusterName string) *cluster.Cluster {
	return &cluster.Cluster{
		Name:                 clusterName,
		ConnectTimeout:       durationpb.New(5 * time.Second),
		ClusterDiscoveryType: &cluster.Cluster_Type{Type: cluster.Cluster_LOGICAL_DNS},
		LbPolicy:             cluster.Cluster_ROUND_ROBIN,
		LoadAssignment:       makeEndpoint(clusterName),
		DnsLookupFamily:      cluster.Cluster_V4_ONLY,
	}
}

func makeEndpoint(clusterName string) *endpoint.ClusterLoadAssignment {
	return &endpoint.ClusterLoadAssignment{
		ClusterName: clusterName,
		Endpoints: []*endpoint.LocalityLbEndpoints{{
			LbEndpoints: []*endpoint.LbEndpoint{{
				HostIdentifier: &endpoint.LbEndpoint_Endpoint{
					Endpoint: &endpoint.Endpoint{
						Address: &core.Address{
							Address: &core.Address_SocketAddress{
								SocketAddress: &core.SocketAddress{
									Protocol: core.SocketAddress_TCP,
									Address:  UpstreamHost,
									PortSpecifier: &core.SocketAddress_PortValue{
										PortValue: UpstreamPort,
									},
								},
							},
						},
					},
				},
			}},
		}},
	}
}

// makeRoute puts the given routes first; the example's catch-all "/" stays last.
func makeRoute(routeName, clusterName string, routes ...*route.Route) *route.RouteConfiguration {
	return &route.RouteConfiguration{
		Name: routeName,
		VirtualHosts: []*route.VirtualHost{{
			Name:    "local_service",
			Domains: []string{"*"},
			Routes: append(routes, &route.Route{
				Match: &route.RouteMatch{
					PathSpecifier: &route.RouteMatch_Prefix{
						Prefix: "/",
					},
				},
				Action: &route.Route_Route{
					Route: &route.RouteAction{
						ClusterSpecifier: &route.RouteAction_Cluster{
							Cluster: clusterName,
						},
						HostRewriteSpecifier: &route.RouteAction_HostRewriteLiteral{
							HostRewriteLiteral: UpstreamHost,
						},
					},
				},
			}),
		}},
	}
}

func makeHTTPListener(listenerName, route string) *listener.Listener {
	routerConfig, _ := anypb.New(&router.Router{})
	// basic_auth is off by default; a route's BasicAuthPerRoute turns it on with its own users.
	// Envoy rejects an empty user list, so this one holds a placeholder.
	basicAuthConfig, _ := anypb.New(&basicauth.BasicAuth{Users: &core.DataSource{
		Specifier: &core.DataSource_InlineString{InlineString: "unused:{SHA}znUBAH8EplKeZQ8fGz/AWG0dlOs="},
	}})
	// HTTP filter configuration
	manager := &hcm.HttpConnectionManager{
		CodecType:  hcm.HttpConnectionManager_AUTO,
		StatPrefix: "http",
		RouteSpecifier: &hcm.HttpConnectionManager_Rds{
			Rds: &hcm.Rds{
				ConfigSource:    makeConfigSource(),
				RouteConfigName: route,
			},
		},
		HttpFilters: []*hcm.HttpFilter{{
			Name:       "envoy.filters.http.basic_auth",
			Disabled:   true,
			ConfigType: &hcm.HttpFilter_TypedConfig{TypedConfig: basicAuthConfig},
		}, {
			Name:       "http-router",
			ConfigType: &hcm.HttpFilter_TypedConfig{TypedConfig: routerConfig},
		}},
	}
	pbst, err := anypb.New(manager)
	if err != nil {
		panic(err)
	}

	return &listener.Listener{
		Name: listenerName,
		Address: &core.Address{
			Address: &core.Address_SocketAddress{
				SocketAddress: &core.SocketAddress{
					Protocol: core.SocketAddress_TCP,
					Address:  "0.0.0.0",
					PortSpecifier: &core.SocketAddress_PortValue{
						PortValue: ListenerPort,
					},
				},
			},
		},
		FilterChains: []*listener.FilterChain{{
			Filters: []*listener.Filter{{
				Name: "http-connection-manager",
				ConfigType: &listener.Filter_TypedConfig{
					TypedConfig: pbst,
				},
			}},
		}},
	}
}

func makeConfigSource() *core.ConfigSource {
	source := &core.ConfigSource{}
	source.ResourceApiVersion = resource.DefaultAPIVersion
	source.ConfigSourceSpecifier = &core.ConfigSource_ApiConfigSource{
		ApiConfigSource: &core.ApiConfigSource{
			TransportApiVersion:       resource.DefaultAPIVersion,
			ApiType:                   core.ApiConfigSource_GRPC,
			SetNodeOnFirstMessageOnly: true,
			GrpcServices: []*core.GrpcService{{
				TargetSpecifier: &core.GrpcService_EnvoyGrpc_{
					EnvoyGrpc: &core.GrpcService_EnvoyGrpc{ClusterName: "xds_cluster"},
				},
			}},
		},
	}
	return source
}

// GenerateSnapshot adds the routes, clusters, listeners and endpoints of SourceFile to the example's own.
func GenerateSnapshot(version string) (*cache.Snapshot, error) {
	f, err := loadFile()
	if err != nil {
		return nil, err
	}
	return cache.NewSnapshot(version,
		map[resource.Type][]types.Resource{
			resource.ClusterType:  append(f.clusters, makeCluster(ClusterName)),
			resource.RouteType:    {makeRoute(RouteName, ClusterName, f.routes...)},
			resource.ListenerType: append(f.listeners, makeHTTPListener(ListenerName, RouteName)),
			resource.EndpointType: f.endpoints,
		},
	)
}

// fileResources are the four lists read from SourceFile.
type fileResources struct {
	routes    []*route.Route
	clusters  []types.Resource
	listeners []types.Resource
	endpoints []types.Resource // upstream hosts of EDS clusters
}

// loadFile reads SourceFile, overwrites ConfigFile with it, and decodes each list into Envoy protos.
func loadFile() (fileResources, error) {
	var f fileResources
	var in struct {
		Routes    []any `yaml:"routes"`
		Clusters  []any `yaml:"clusters"`
		Listeners []any `yaml:"listeners"`
		Endpoints []any `yaml:"endpoints"`
	}
	raw, err := os.ReadFile(SourceFile)
	if err != nil {
		return f, err
	}
	if err := yaml.Unmarshal(raw, &in); err != nil {
		return f, err
	}

	// The final config: the four lists, empty ones as [].
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(in); err != nil {
		return f, err
	}
	if err := os.WriteFile(ConfigFile, out.Bytes(), 0o644); err != nil {
		return f, err
	}

	for _, item := range in.Routes {
		r := &route.Route{}
		if err := decode(item, r); err != nil {
			return f, err
		}
		if err := r.ValidateAll(); err != nil {
			return f, err
		}
		f.routes = append(f.routes, r)
	}
	if f.clusters, err = decodeAll(in.Clusters, func() proto.Message { return &cluster.Cluster{} }); err != nil {
		return f, err
	}
	if f.listeners, err = decodeAll(in.Listeners, func() proto.Message { return &listener.Listener{} }); err != nil {
		return f, err
	}
	f.endpoints, err = decodeAll(in.Endpoints, func() proto.Message { return &endpoint.ClusterLoadAssignment{} })
	return f, err
}

// decodeAll turns each YAML item into a new proto from newMsg and checks Envoy's field rules.
func decodeAll(items []any, newMsg func() proto.Message) ([]types.Resource, error) {
	var out []types.Resource
	for _, item := range items {
		m := newMsg()
		if err := decode(item, m); err != nil {
			return nil, err
		}
		if err := m.(interface{ ValidateAll() error }).ValidateAll(); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// decode turns one YAML item into an Envoy proto: YAML -> JSON -> proto.
func decode(item any, msg proto.Message) error {
	js, err := json.Marshal(item)
	if err != nil {
		return err
	}
	return protojson.Unmarshal(js, msg)
}
