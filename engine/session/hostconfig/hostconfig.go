package hostconfig

import (
	context "context"

	"github.com/dagger/dagger/util/grpcutil"
	grpc "google.golang.org/grpc"
)

// WellKnownHostConfigs maps well-known config names to paths relative
// to the user's home directory. SDK modules can request these files
// via ModuleSource.hostConfigFile(name).
var WellKnownHostConfigs = map[string]string{
	"maven-settings":    ".m2/settings.xml",
	"gradle-properties": ".gradle/gradle.properties",
	"pip-config":        ".config/pip/pip.conf",
	"npmrc":             ".npmrc",
}

type HostConfigAttachable struct {
	UnimplementedHostConfigServer
}

func NewHostConfigAttachable() HostConfigAttachable {
	return HostConfigAttachable{}
}

func (s HostConfigAttachable) Register(srv *grpc.Server) {
	RegisterHostConfigServer(srv, &s)
}

type HostConfigProxy struct {
	client HostConfigClient
}

func NewHostConfigProxy(client HostConfigClient) HostConfigProxy {
	return HostConfigProxy{client: client}
}

func (p HostConfigProxy) Register(server *grpc.Server) {
	RegisterHostConfigServer(server, p)
}

func (p HostConfigProxy) GetFile(ctx context.Context, req *HostConfigRequest) (*HostConfigResponse, error) {
	return p.client.GetFile(grpcutil.IncomingToOutgoingContext(ctx), req)
}
