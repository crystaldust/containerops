/*
Copyright 2016 - 2017 Huawei Technologies Co., Ltd. All rights reserved.

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

package handler

import (
	"fmt"
	"net/http"

	"github.com/Huawei/containerops/assembling/controller"
	"github.com/Huawei/containerops/common"
	log "github.com/Sirupsen/logrus"
	uuid "github.com/satori/go.uuid"
	macaron "gopkg.in/macaron.v1"

	_ "k8s.io/client-go/plugin/pkg/client/auth"
)

func BuildImageHandler(mctx *macaron.Context) (int, []byte) {
	// TODO image, namespace, registry, tag pattern validation with regex
	registry := mctx.Req.Request.FormValue("registry")
	namespace := mctx.Req.Request.FormValue("namespace")
	image := mctx.Req.Request.FormValue("image")
	tag := mctx.Req.Request.FormValue("tag")

	podClient, serviceClient, err := controller.InitK8SResourceInterfaces(common.Assembling.KubeConfig)
	if err != nil {
		log.Errorf("Failed to init k8s pod client: %s", err.Error())
		return http.StatusInternalServerError, []byte(err.Error())
	}

	buildId := uuid.NewV4().String()
	podName := fmt.Sprintf("containerops-build-pod-%s", buildId)
	serviceName := fmt.Sprintf("containerops-build-svc-%s", buildId)

	_, err = controller.CreatePod(podClient, podName, buildId)
	if err != nil {
		log.Errorf("Failed to create pod: %s", err.Error())
		return http.StatusInternalServerError, []byte("{}")
	}
	defer controller.DeletePod(podClient, podName)

	nodeBalancer, err := controller.CreateNodeBalancer(serviceClient, serviceName, buildId)
	if err != nil {
		log.Errorf("Failed to create pod: %s", err.Error())
		return http.StatusInternalServerError, []byte("{}")
	}

	servicePort := 2375
	defer controller.DeleteNodeBalancer(serviceClient, serviceName)

	serviceIP := nodeBalancer.Status.LoadBalancer.Ingress[0].IP
	dockerDaemonHost := fmt.Sprintf("%s:%d", serviceIP, servicePort)
	ctx, dockerClient := controller.InitDockerCli(dockerDaemonHost)

	tarfile, err := controller.CreateTarFile(mctx.Req.Request.Body)
	if err := controller.BuildImage(ctx, dockerClient, registry, namespace, image, tag, tarfile); err != nil {
		log.Errorf("Failed to build image: %s", err.Error())
		return http.StatusInternalServerError, []byte("{}")
	}

	// TODO Support pushing to registries that need authorization
	authStr, _ := controller.GenerateAuthStr("", "")

	if err := controller.PushImage(ctx, dockerClient, registry, namespace, image, tag, authStr); err != nil {
		log.Errorf("Failed to push image: %s", err.Error())
		return http.StatusInternalServerError, []byte("{}")
	}

	builtImage := fmt.Sprintf("%s/%s/%s:%s", registry, namespace, image, tag)
	return http.StatusOK, []byte(fmt.Sprintf("{\"endpoint\":\"%s\"}", builtImage))
}
