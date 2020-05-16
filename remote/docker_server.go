/*
Copyright 2016 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package remote

import (
	"fmt"

	"google.golang.org/grpc"
	runtimeapi "github.com/phosae/dockershim/api"
	"k8s.io/klog"
	"github.com/phosae/dockershim/service"
	"github.com/phosae/dockershim/util"
)

// maxMsgSize use 16MB as the default message size limit.
// grpc library default is 4MB
const maxMsgSize = 1024 * 1024 * 16

// DockerServer is the grpc server of dockershim.
type DockerServer struct {
	// endpoint is the endpoint to serve on.
	endpoint string
	// service is the docker service which implements runtime and image services.
	service dockershim.CRIService
	// server is the grpc server.
	server *grpc.Server
}

// NewDockerServer creates the dockershim grpc server.
func NewDockerServer(endpoint string, s dockershim.CRIService) *DockerServer {
	return &DockerServer{
		endpoint: endpoint,
		service:  s,
	}
}

// Start starts the dockershim grpc server.
func (s *DockerServer) Start() error {
	// Start the internal service.
	// 做两件事
	// 1. 启动一个 Local Stream Server(listen localhost:0) 处理 exec, attach, port-forward (crictl???)
	// 2. 启动 ContainerManager 做操作系统相关清理
	if err := s.service.Start(); err != nil {
		klog.Errorf("Unable to start docker service")
		return err
	}

	// 创建 dockershim grpc listener，listen on /var/run/dockershim.sock
	klog.V(2).Infof("Start dockershim grpc server")
	l, err := util.CreateListener(s.endpoint)
	if err != nil {
		return fmt.Errorf("failed to listen on %q: %v", s.endpoint, err)
	}
	// Create the grpc server and register runtime and image services.
	s.server = grpc.NewServer(
		grpc.MaxRecvMsgSize(maxMsgSize),
		grpc.MaxSendMsgSize(maxMsgSize),
	)

	// dockershim.CRIService.(runtimeapi.RuntimeServiceServer)
	runtimeapi.RegisterRuntimeServiceServer(s.server, s.service)
	// dockershim.CRIService.(runtimeapi.ImageServiceServer)
	runtimeapi.RegisterImageServiceServer(s.server, s.service)
	go func() {
		// 基于 dockershim grpc listener 启动 rpc 服务
		if err := s.server.Serve(l); err != nil {
			klog.Fatalf("Failed to serve connections: %v", err)
		}
	}()
	return nil
}
