package dockershim

import (
	"context"
	"fmt"
	"github.com/phosae/dockershim/server/exec"
	"net/http"
	"time"

	"github.com/blang/semver"
	dockertypes "github.com/docker/docker/api/types"
	"k8s.io/klog"

	runtimeapi "github.com/phosae/dockershim/api"

	"github.com/phosae/dockershim/server/streaming"

	"github.com/phosae/dockershim/libdocker"
)

const (
	dockerRuntimeName = "docker"
	kubeAPIVersion    = "0.1.0"
	// todo chx to image label key
	containerTypeLabelKey       = "io.kubernetes.docker.type"
	containerLogPathLabelKey    = "io.kubernetes.container.logpath"
	sandboxIDLabelKey           = "io.kubernetes.sandbox.id"
)

var internalLabelKeys = []string{containerTypeLabelKey, containerLogPathLabelKey, sandboxIDLabelKey}

// CRIService includes all methods necessary for a CRI server.
type CRIService interface {
	runtimeapi.RuntimeServiceServer
	runtimeapi.ImageServiceServer
	Start() error
}

// DockerService is an interface that embeds the new RuntimeService and
// ImageService interfaces.
type DockerService interface {
	CRIService

	// For serving streaming calls.
	http.Handler
}


// ClientConfig is parameters used to initialize docker client
type ClientConfig struct {
	DockerEndpoint            string
	RuntimeRequestTimeout     time.Duration
	ImagePullProgressDeadline time.Duration

	// Configuration for fake docker client
	EnableSleep       bool
	WithTraceDisabled bool
}

// NewDockerClientFromConfig create a docker client from given configure
// return nil if nil configure is given.
func NewDockerClientFromConfig(config *ClientConfig) libdocker.Interface {
	if config != nil {
		// Create docker client.
		client := libdocker.ConnectToDockerOrDie(
			config.DockerEndpoint,
			config.RuntimeRequestTimeout,
			config.ImagePullProgressDeadline,
		)
		return client
	}

	return nil
}

// NewDockerService creates a new `DockerService` struct.
// NOTE: Anything passed to DockerService should be eventually handled in another way when we switch to running the shim as a different process.
func NewDockerService(config *ClientConfig, podSandboxImage string, streamingConfig *streaming.Config,
	cgroupsName string, kubeCgroupDriver string, dockershimRootDir string, startLocalStreamingServer bool) (DockerService, error) {

	client := NewDockerClientFromConfig(config)

	c := client

	ds := &dockerService{
		client:          c,
		streamingRuntime: &streamingRuntime{
			client:      client,
			execHandler: exec.NativeExecHandler{},
		},

		startLocalStreamingServer: startLocalStreamingServer,
	}

	// check docker version compatibility.
	if err := ds.checkVersionCompatibility(); err != nil {
		return nil, err
	}

	// create streaming server if configured.
	if streamingConfig != nil {
		var err error
		ds.streamingServer, err = streaming.NewServer(*streamingConfig, ds.streamingRuntime)
		if err != nil {
			return nil, err
		}
	}

	return ds, nil
}

type dockerService struct {
	client           libdocker.Interface
	streamingRuntime *streamingRuntime
	streamingServer  streaming.Server
	startLocalStreamingServer bool

}
// Version returns the runtime name, runtime version and runtime API version
func (ds *dockerService) Version(_ context.Context, r *runtimeapi.VersionRequest) (*runtimeapi.VersionResponse, error) {
	v, err := ds.getDockerVersion()
	if err != nil {
		return nil, err
	}
	return &runtimeapi.VersionResponse{
		Version:           kubeAPIVersion,
		RuntimeName:       dockerRuntimeName,
		RuntimeVersion:    v.Version,
		RuntimeApiVersion: v.APIVersion,
	}, nil
}

// getDockerVersion gets the version information from docker.
func (ds *dockerService) getDockerVersion() (*dockertypes.Version, error) {
	v, err := ds.client.Version()
	if err != nil {
		return nil, fmt.Errorf("failed to get docker version: %v", err)
	}
	// Docker API version (e.g., 1.23) is not semver compatible. Add a ".0"
	// suffix to remedy this.
	v.APIVersion = fmt.Sprintf("%s.0", v.APIVersion)
	return v, nil
}

// Start initializes and starts components in dockerService.
func (ds *dockerService) Start() error {

	// Initialize the legacy cleanup flag.
	if ds.startLocalStreamingServer {
		go func() {
			if err := ds.streamingServer.Start(true); err != nil {
				klog.Fatalf("Streaming server stopped unexpectedly: %v", err)
			}
		}()
	}
	return nil
}

// Status returns the status of the runtime.
func (ds *dockerService) Status(_ context.Context, r *runtimeapi.StatusRequest) (*runtimeapi.StatusResponse, error) {
	runtimeReady := &runtimeapi.RuntimeCondition{
		Type:   runtimeapi.RuntimeReady,
		Status: true,
	}
	networkReady := &runtimeapi.RuntimeCondition{
		Type:   runtimeapi.NetworkReady,
		Status: true,
	}
	conditions := []*runtimeapi.RuntimeCondition{runtimeReady, networkReady}
	if _, err := ds.client.Version(); err != nil {
		runtimeReady.Status = false
		runtimeReady.Reason = "DockerDaemonNotReady"
		runtimeReady.Message = fmt.Sprintf("docker: failed to get docker version: %v", err)
	}

	status := &runtimeapi.RuntimeStatus{Conditions: conditions}
	return &runtimeapi.StatusResponse{Status: status}, nil
}

func (ds *dockerService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if ds.streamingServer != nil {
		ds.streamingServer.ServeHTTP(w, r)
	} else {
		http.NotFound(w, r)
	}
}

// checkVersionCompatibility verifies whether docker is in a compatible version.
func (ds *dockerService) checkVersionCompatibility() error {
	apiVersion, err := ds.getDockerAPIVersion()
	if err != nil {
		return err
	}

	minAPIVersion, err := semver.Parse(libdocker.MinimumDockerAPIVersion)
	if err != nil {
		return err
	}

	// Verify the docker version.
	result := apiVersion.Compare(minAPIVersion)
	if result < 0 {
		return fmt.Errorf("docker API version is older than %s", libdocker.MinimumDockerAPIVersion)
	}

	return nil
}

// getDockerAPIVersion gets the semver-compatible docker api version.
func (ds *dockerService) getDockerAPIVersion() (*semver.Version, error) {
	var dv *dockertypes.Version
	var err error

	dv, err = ds.getDockerVersion()

	if err != nil {
		return nil, err
	}

	apiVersion, err := semver.Parse(dv.APIVersion)
	if err != nil {
		return nil, err
	}
	return &apiVersion, nil
}
